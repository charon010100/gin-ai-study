// Package cache 是 Redis 客户端，实现 service.CachePort。
//
// 注意依赖方向：cache 包 import service 吗？不。
// 它只是「碰巧拥有」和 CachePort 相同签名的方法，
// Go 的隐式接口让它可以被直接赋给 service.CachePort。
//
// 这就叫「结构类型」（structural typing）：
// 实现方不需要知道接口的存在，接口也不需要知道实现方的存在。
// 好处是 service 包可以定义自己需要的窄接口，
// 而不用去引用一个臃肿的 cache 包。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client 是缓存客户端。
//
// 设计前提：缓存是可选组件。所有方法都不返回「Redis 不可用」这类错误，
// 连不上就当作未命中 —— 否则一次 Redis 重启会让全站 500。
type Client struct {
	rdb    *redis.Client
	prefix string
}

// New 建立连接。
//
// 连不上也返回可用的 Client（所有操作会快速失败并降级），
// 只有参数明显非法时才返回 error。
func New(ctx context.Context, addr, password string, db int, prefix string) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})

	c := &Client{rdb: rdb, prefix: prefix}

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		log.Printf("[WARN] Redis 不可用，已降级为无缓存模式: %v", err)
	}
	return c
}

// Close 关闭连接池。
func (c *Client) Close() error { return c.rdb.Close() }

// GetJSON 读取并反序列化。错误返回值只表示「缓存内容坏了」，
// 不表示「Redis 不可用」。
func (c *Client) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, err := c.rdb.Get(ctx, c.prefix+key).Bytes()
	if err != nil {
		// redis.Nil 是「键不存在」，属于正常情况。
		// 写成 err != nil 就报错，会让每个冷启动请求都 500。
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		// 其他错误（连不上、超时）也当未命中，但要记一笔。
		log.Printf("[WARN] 缓存读取降级: %v", err)
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		// 结构不兼容（通常是发版后旧缓存残留），删掉当未命中。
		_ = c.rdb.Del(ctx, c.prefix+key).Err()
		return false, nil
	}
	return true, nil
}

// SetJSON 写入并设置 TTL。
func (c *Client) SetJSON(ctx context.Context, key string, val any, ttlSeconds int) {
	raw, err := json.Marshal(val)
	if err != nil {
		return
	}
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if err := c.rdb.Set(ctx, c.prefix+key, raw, ttl).Err(); err != nil {
		log.Printf("[WARN] 缓存写入降级: %v", err)
	}
}

// Del 删除指定 key。
func (c *Client) Del(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	prefixed := make([]string, len(keys))
	for i, k := range keys {
		prefixed[i] = c.prefix + k
	}
	if err := c.rdb.Del(ctx, prefixed...).Err(); err != nil {
		log.Printf("[WARN] 缓存删除降级: %v", err)
	}
}

// DelByPrefix 按前缀批量删除。
//
// 用 SCAN 而不是 KEYS：
//   KEYS 会遍历整个 keyspace 并阻塞 Redis 的单线程事件循环，
//   在有几万 key 的实例上执行一次就能让所有客户端超时。
//   SCAN 是游标式增量遍历，虽然可能返回重复值，但不会阻塞。
//
// 注意：即使这样，这个操作也不便宜。真实项目里
// 「列表缓存失效」应该用版本号方案，而不是批量删 key。
func (c *Client) DelByPrefix(ctx context.Context, prefix string) {
	full := c.prefix + prefix
	iter := c.rdb.Scan(ctx, 0, full+"*", 100).Iterator()
	var batch []string
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= 100 {
			if err := c.rdb.Del(ctx, batch...).Err(); err != nil {
				log.Printf("[WARN] 按前缀删除降级: %v", err)
				return
			}
			batch = batch[:0]
		}
	}
	if err := iter.Err(); err != nil {
		log.Printf("[WARN] 按前缀扫描降级: %v", err)
		return
	}
	if len(batch) > 0 {
		if err := c.rdb.Del(ctx, batch...).Err(); err != nil {
			log.Printf("[WARN] 按前缀删除降级: %v", err)
		}
	}
}

// 关于「Client 必须满足 service.CachePort」的断言：
//
// 这里不能写 `var _ service.CachePort = (*Client)(nil)` ——
// 那会让 cache import service，而正确的方向是
// service 定义接口、cache 提供实现，两者互不 import。
//
// 这类跨包断言应该放在装配处（cmd/server/main.go 里同时 import 两者），
// 见那里的 var _ service.CachePort = (*cache.Client)(nil)。
