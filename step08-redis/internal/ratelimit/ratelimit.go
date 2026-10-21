// Package ratelimit 把 cache 包的限流算法接到 Gin 上。
//
// 为什么单独成包，而不是把中间件直接写在 cache 里：
// 缓存层不应该 import gin。一旦 import 了，它就从「可复用的基础设施」
// 变成「只能给 HTTP 用的组件」——将来定时任务、消息消费者也想限流时
// 就会发现它拖着一整个 Web 框架。
//
// 依赖方向应该是：HTTP 层 → 基础设施层，不能反过来。
package ratelimit

import (
	"strconv"

	"ginlesson/step08/internal/cache"
	"github.com/gin-gonic/gin"
)

// Middleware 按客户端 IP 限流。
//
// 参数 ipp 取自 gin 的 ClientIP（它根据 X-Forwarded-For / X-Real-IP 推导）。
// 注意：只有服务确实在可信反向代理之后，这个值才可信。
// 直接暴露在公网时，攻击者伪造该头即可绕过按 IP 限流。
func Middleware(l *cache.RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		res := l.Allow(c.Request.Context(), c.ClientIP())

		// 无论放行与否都回填这几个头，客户端才能主动退避而不是反复撞墙。
		// 只回 429 不给任何提示，会让客户端立刻重试，反而加重压力。
		c.Header("X-RateLimit-Limit", strconv.FormatInt(l.Limit(), 10))
		c.Header("X-RateLimit-Remaining", strconv.FormatInt(maxInt64(0, l.Limit()-res.Current), 10))

		if !res.Allowed {
			c.Header("Retry-After", strconv.Itoa(int(l.Window().Seconds())))
			c.AbortWithStatusJSON(429, gin.H{
				"error": "请求过于频繁，请稍后再试",
			})
			return
		}

		// 降级时通过响应头透出，便于实验时观察。
		// 真实项目里更常见的是打指标而不是加响应头（响应头会暴露内部状态）。
		if !res.RedisAvailable {
			c.Header("X-RateLimit-Degraded", "1")
		}
		c.Next()
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
