// 本文件属于 cache 包，实现基于 Redis 的固定窗口限流「算法」。
//
// 注意本文件不 import gin —— 它只回答「这个维度还能不能放行」，
// 不知道也不关心请求是怎么进来的。把 Gin 中间件放在
// internal/ratelimit 包里，这样缓存层保持与 HTTP 无关。
//
// 为什么必须用 Redis 而不是在进程内记 map[string]int：
//
// 内存版限流器只在**单实例**下有效。一旦部署两个实例（滚动发布时
// 必然出现短暂的双实例并存），攻击者打到哪个实例就单独计数，
// 实际允许的请求量变成 limit × 实例数。
//
// 更糟的是滚动发布期间：老实例的计数在新实例里不存在，限流瞬间放开。
// 这类问题在「压测通过、上线出事」的案例里非常常见。
package cache

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"
)

// rateLimitScript 做原子的「自增 + 首次设置过期」。
//
// 为什么不能拆成 INCR 然后 EXPIRE 两行：
//
//	INCR key          -> 1
//	<进程崩溃 / 网络中断>
//	EXPIRE key 60     -> 没执行
//
// 这个 key 就永远不会过期，计数器永久累加。该 IP 从此被永久限流，
// 直到你手动去 Redis 删 key。现象是「某个用户永远 429」，
// 而他本人什么也没做错。
//
// 用 Lua 把两步合成一次往返，既解决原子性，也少一次网络 RTT。
const rateLimitScript = `
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return current
`

// RateLimiter 是固定窗口限流器。
//
// 为什么是固定窗口而不是滑动窗口：固定窗口实现简单、一次往返，
// 足够挡住脚本狂刷。已知缺陷是窗口边界处的突发
// （59 秒和 61 秒各打 limit 次，两秒内实际通过了 2×limit 次）。
// 若不可接受，应换滑动窗口（用 ZSET 存每次请求时间戳，
// 代价是内存与复杂度都上升）。教学项目选简单的那条路。
type RateLimiter struct {
	client *Client
	// window 与 limit 共同定义策略：window 内最多 limit 次。
	window time.Duration
	limit  int64
	// keyPrefix 区分不同用途的限流（评论、登录、搜索各自独立）。
	keyPrefix string
}

// NewRateLimiter 构造限流器。
func NewRateLimiter(c *Client, keyPrefix string, window time.Duration, limit int64) *RateLimiter {
	return &RateLimiter{client: c, window: window, limit: limit, keyPrefix: keyPrefix}
}

// Result 是限流的判定结果。
//
// 用一个结构体而不是 (allowed bool, redisOK bool) 两个返回值：
// 调用方（中间件）需要同时处理「是否放行」「是否降级」「剩余多久」，
// 布尔返回值一多，调用点很快就会写成 `if !a && b || c` 这种读不懂的条件。
type Result struct {
	Allowed bool
	// RedisAvailable 为 false 表示这次判定降级了（Redis 不可用）。
	RedisAvailable bool
	// Current 是当前窗口内已计数的请求数，便于把 X-RateLimit-* 头填完整。
	Current int64
}

// Allow 判断某个维度的请求是否放行。
func (l *RateLimiter) Allow(ctx context.Context, dimension string) Result {
	key := l.client.Key("rl", l.keyPrefix, dimension)

	res, err := l.client.rdb.Eval(ctx, rateLimitScript, []string{key}, l.window.Milliseconds()).Result()
	if err != nil {
		// 关键决策：Redis 不可用时限流放行，而不是拒绝所有请求。
		//
		// 两种选择各自的理由：
		//
		//   fail-closed（拒绝）：限流保护不被绕过。但 Redis 一挂，
		//     全站评论、登录全部不可用 —— 一次缓存故障被放大成服务故障。
		//
		//   fail-open（放行）：服务保持可用。代价是故障期间可能被刷。
		//
		// 对个人博客，「被刷一阵」远好于「读者完全不能评论」，所以选 fail-open。
		// 如果这是支付或登录接口，答案可能相反。
		// 这类决策没有统一正确答案，重要的是**你明确选过、并知道代价**。
		logDegraded(err)
		return Result{Allowed: true, RedisAvailable: false}
	}

	n, err := toInt64(res)
	if err != nil {
		log.Printf("[WARN] 限流脚本返回值异常: %v", err)
		return Result{Allowed: true, RedisAvailable: false}
	}
	return Result{Allowed: n <= l.limit, RedisAvailable: true, Current: n}
}

// Window 返回窗口长度，供中间件填 Retry-After。
func (l *RateLimiter) Window() time.Duration { return l.window }

// Limit 返回阈值。
func (l *RateLimiter) Limit() int64 { return l.limit }

// toInt64 把 Lua 脚本的返回值转成 int64。
//
// Redis 的 Lua 数值统一是 integer，go-redis 通常解码成 int64，
// 这里做一次兼容以防脚本将来改成返回字符串。
func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, fmt.Errorf("限流脚本返回值类型不符合预期: %T", v)
	}
}
