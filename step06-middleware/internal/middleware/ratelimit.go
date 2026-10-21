package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter 是一个基于「令牌桶」的进程内限流器。
//
// 令牌桶相比「固定窗口计数」好在哪：
//
//	固定窗口：每分钟最多 60 次。问题是用户可以在 12:00:59 打 60 次，
//	          12:01:00 再打 60 次 —— 两秒内 120 次，窗口边界形同虚设。
//	令牌桶：  桶容量 burst，每秒回填 rate 个令牌。任何时间点都平滑受限，
//	          并且允许短时突发（burst），更符合真实流量特征。
//
// 参数怎么选：rate=1, burst=5 表示「平均每秒 1 次，允许突然连发 5 次」。
// 对评论、登录这类接口，这个量级合适。
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	rate     float64 // 每秒回填多少令牌
	burst    float64 // 桶容量，也就是允许的最大突发次数
	idleTTL  time.Duration
	lastGC   time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter 创建限流器。
func NewRateLimiter(rate, burst float64) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   burst,
		// 一个 key 超过这么久没被访问，就可以把它从 map 里删掉。
		// 没有这个清理，map 会随着不同 IP 的累积无限增长 ——
		// 这是一个真实的内存泄漏，而且很难在测试里发现。
		idleTTL: 10 * time.Minute,
		lastGC:  time.Now(),
	}
}

// Allow 判断某个 key 当前是否允许通过，并消耗一个令牌。
//
// 注意整个方法在锁内完成：读取令牌、计算回填、扣减，必须是一个原子操作。
// 如果拆成「先读后写」两段加锁，两个并发请求会同时读到「有 1 个令牌」，
// 于是都放行 —— 限流就失效了。
func (l *RateLimiter) Allow(key string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.gcLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	// 按经过的时间回填令牌，但不超过桶容量。
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

// gcLocked 清理长时间不活跃的 key。调用方必须已持有锁。
func (l *RateLimiter) gcLocked(now time.Time) {
	// 不需要每次请求都扫一遍 map，那样 O(n) 的开销会拖慢所有请求。
	// 每分钟扫一次足够。
	if now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now

	for k, b := range l.buckets {
		if now.Sub(b.last) > l.idleTTL {
			delete(l.buckets, k)
		}
	}
}

// Middleware 返回限流中间件。
//
// keyBy 决定「按什么维度限流」。常见选择：
//
//	按 IP        —— 防匿名刷接口，但 NAT 后的正常用户会互相影响
//	按用户 ID    —— 更精确，但需要先登录（仅适合已鉴权的接口）
//	按 IP + 路径 —— 最常用，避免「访问首页太频繁」把评论接口也限了
func (l *RateLimiter) Middleware(keyBy func(c *gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.Allow(keyBy(c)) {
			// 429 Too Many Requests 是标准状态码。
			// 配合 Retry-After 头告诉客户端多久后可以重试——
			// 这是有礼貌的做法，客户端可以据此退避，而不是盲目重试。
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    1429,
				"message": "操作过于频繁，请稍后再试",
				"data":    nil,
			})
			return
		}
		c.Next()
	}
}

// KeyByIP 按客户端 IP 限流。
func KeyByIP(c *gin.Context) string {
	return ClientIPFrom(c.Request.Context())
}

// KeyByIPAndPath 按 IP + 路径限流（推荐的默认策略）。
func KeyByIPAndPath(c *gin.Context) string {
	return ClientIPFrom(c.Request.Context()) + "|" + c.FullPath()
}

// 关于「进程内限流」的局限（本步必须说清楚）：
//
// 这个限流器把计数放在**本进程的内存**里。单实例部署时它是正确的。
// 但一旦部署多个实例（负载均衡后面 3 个 Pod），每个实例各算各的：
// 用户实际能发 3 倍的请求量，而且请求被轮询到不同实例时行为不一致。
//
// 正确的多实例方案是把计数放到共享存储（Redis），
// 用 INCR + EXPIRE 或者 Lua 脚本保证原子性。这是 Step 8 的内容。
//
// 这也是一个通用的判断方法：
//   「状态存在进程内存里」→ 只能单实例
//   「状态存在共享存储里」→ 可以水平扩展
