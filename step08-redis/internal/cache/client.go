// Package cache 封装 Redis，并刻意做成「Redis 挂了也不会把错误抛给上层」。
//
// 这个包的设计前提是：**缓存是可选组件**。
// 它只影响快慢，不该影响功能是否可用。所以这里所有缓存读写方法的签名都不返回
// 「Redis 不可用」这类错误 —— 未命中、连接失败、超时，对调用方都表现为
// 「这次没命中」，于是自然回源 DB。
//
// 结果是：缓存不可用时接口变慢，但不会 500。
// 这是 Step 8 最重要的一个决策，细节见 NOTES。
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client 是带降级的 Redis 封装。
type Client struct {
	rdb *redis.Client
	// prefix 给所有 key 加统一前缀。
	// 同一台 Redis 常被多个应用共用，没有前缀时一次 key 冲突
	// 就足以让两边一起出错。
	prefix string
}

// Options 是连接参数。
type Options struct {
	Addr     string
	Password string
	DB       int
	// Prefix 建议以冒号结尾，如 "blog:"。
	Prefix string
	// DialTimeout 给 3 秒这类短值。连不上就快速降级，
	// 不要在「Redis 挂掉」时把每个请求都卡住 30 秒 —— 那等于全站变慢。
	DialTimeout time.Duration
}

// Connect 建立连接。
//
// 注意这里**不会**因为连不上就返回 fatal：连不上时返回一个「永远未命中」的
// Client 和 nil error，让应用照常启动。
// 只有显式的健康检查（Health）才会暴露 Redis 的真实状态。
//
// 为什么这样设计：如果 Redis 连不上就不许启动，那么一次缓存重启
// 会导致所有应用实例启动失败 —— 缓存故障被放大成服务不可用。
func Connect(ctx context.Context, opt Options) (*Client, error) {
	if opt.DialTimeout <= 0 {
		opt.DialTimeout = 3 * time.Second
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:        opt.Addr,
		Password:    opt.Password,
		DB:          opt.DB,
		DialTimeout: opt.DialTimeout,
		// 读写超时也要短。Redis 卡住时，请求应很快放弃并回源 DB，
		// 而不是挂在那里等。
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolSize:     20,
	})

	c := &Client{rdb: rdb, prefix: opt.Prefix}

	pingCtx, cancel := context.WithTimeout(ctx, opt.DialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		log.Printf("[WARN] Redis 连接失败（将降级为无缓存模式，接口会变慢但可用）: %v", err)
		return c, nil
	}
	log.Printf("Redis 连接就绪: %s", opt.Addr)
	return c, nil
}

// Close 关闭连接池。
func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// Health 供就绪探针使用。只有它会暴露 Redis 的真实状态。
func (c *Client) Health(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return errors.New("Redis 未初始化")
	}
	return c.rdb.Ping(ctx).Err()
}

// Key 生成带前缀的 key。
func (c *Client) Key(parts ...string) string {
	return c.prefix + strings.Join(parts, ":")
}

// ---------- 缓存读写（永不返回「Redis 坏了」） ----------

// GetJSON 读取并反序列化到 dst。
//
// 返回的 error 只有一种含义：**缓存内容本身有问题**，而不是 Redis 不可用。
// Redis 层面的失败一律当作未命中（返回 false, nil）。
func (c *Client) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		// 这里就是那个著名的坑：redis.Nil 表示「键不存在」。
		// 它是 error 类型的值，但语义上完全正常。
		//
		// 如果写成 `if err != nil { return err }`，那么**每一个冷启动请求**
		// （缓存还没填满时）、每一篇新文章第一次被访问，都会返回错误并映射成 500。
		// 现象是「服务刚上线时全是 500，跑一会儿自己就好了」—— 极难定位。
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		// 其他错误（连接断开、超时）也当作未命中，但记一笔便于发现 Redis 挂了。
		logDegraded(err)
		return false, nil
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		// 反序列化失败通常是「发版后旧缓存结构与新代码不兼容」。
		// 删掉这个脏 key 并当成未命中，比让请求持续 500 合理得多。
		_ = c.rdb.Del(ctx, key).Err()
		return false, nil
	}
	return true, nil
}

// SetJSON 序列化后写入。
//
// ttl 为 0 表示不过期，但本包所有调用方都应传正数：
// 无过期的缓存 key 会永久占内存，且永远不刷新（数据永久陈旧）。
func (c *Client) SetJSON(ctx context.Context, key string, val any, ttl time.Duration) {
	raw, err := json.Marshal(val)
	if err != nil {
		return
	}
	if err := c.rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		logDegraded(err)
	}
}

// Del 删除缓存。写路径上必须调用。
//
// 删除失败同样只记日志：DB 已经改成功了，因为缓存删除失败就让请求报错，
// 相当于把缓存提升成了强依赖 —— 那正是要避免的。
func (c *Client) Del(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		logDegraded(err)
	}
}

// Incr 自增并返回新值与是否成功。
//
// Redis 不可用时返回 (0, false)，调用方应跳过这个功能而不是报错。
func (c *Client) Incr(ctx context.Context, key string) (int64, bool) {
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		logDegraded(err)
		return 0, false
	}
	return n, true
}

// ---------- key 生成 ----------

// PostKey 文章详情缓存。
func (c *Client) PostKey(slug string) string {
	return c.Key("post", slug)
}

// PostListKey 文章列表缓存。
//
// 这里把查询条件做了哈希，而不是直接拼可读字符串。
//
// 原因：keyword 是用户输入，可能含冒号、空格、超长中文。
// 直接拼进 key 会超出长度上限（512 字节）并混入分隔符，
// 导致不同查询条件生成相同 key（读到别人的缓存）。
// 代价是 key 不可读 —— 这是「可读性」与「正确性」的取舍，
// 只要 key 包含用户输入，就毫不犹豫选正确性。
func (c *Client) PostListKey(p ListKeyParams) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "status=%s|kw=%s|page=%d|size=%d", p.Status, p.Keyword, p.Page, p.PageSize)
	return c.Key("postlist", fmt.Sprintf("%x", h.Sum64()))
}

// ListKeyParams 是列表缓存的 key 维度。
//
// 独立成类型而不是直接收若干参数：将来加「按标签筛选」时
// 只需要改这里和哈希逻辑，所有调用点不用动。
type ListKeyParams struct {
	Status   string
	Keyword  string
	Page     int
	PageSize int
}

// ---------- 内部工具 ----------

// degradedLog 控制降级日志的频率。
//
// 不加抑制的话，Redis 挂掉期间每个请求都会打一行日志，
// 几秒钟就能刷满磁盘 —— 一次缓存故障演变成磁盘写满，
// 这是真实发生过的事故模式。
//
// 用 mutex 而不是 sync.Once：需要「每 10 秒允许一次」的滑动抑制，
// 而不是只允许一次。用 atomic.Int64 存时间戳也可以，但 mutex 更直观。
type degradedLog struct {
	mu   sync.Mutex
	last time.Time
}

func (d *degradedLog) warn(err error) {
	// 计数与「是否打日志」分开：即使日志被抑制到 10 秒一次，
	// 降级次数本身仍然要精确累计，否则无法判断 Redis 到底恢复了没有。
	degradedCount.Add(1)

	d.mu.Lock()
	now := time.Now()
	if now.Sub(d.last) < 10*time.Second {
		d.mu.Unlock()
		return
	}
	d.last = now
	d.mu.Unlock()

	log.Printf("[WARN] Redis 操作降级（10 秒内只提示一次）: %v", err)
}

var (
	degraded      degradedLog
	degradedCount atomic.Int64
)

func logDegraded(err error) { degraded.warn(err) }

// DegradedCount 返回累计发生的降级次数，供 /healthz 或调试接口观察。
// 这个数字持续增长就说明 Redis 实际不可用，而接口看起来还是好的。
func DegradedCount() int64 { return degradedCount.Load() }
