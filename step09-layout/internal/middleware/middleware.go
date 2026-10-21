// Package middleware 提供 Gin 中间件。
//
// 这里的每个中间件都通过构造函数接收依赖（比如 *logger.Logger），
// 而不是在中间件内部 new 或者引用包级变量。
// 目的一致：让依赖方向可见，让测试可以替换。
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/gin-gonic/gin"

	"ginlesson/step09/pkg/logger"
	"ginlesson/step09/pkg/response"
)

// RequestID 给每个请求分配唯一 ID，写入 context 和响应头。
//
// 为什么需要它：一个请求会经过中间件 → handler → service → repository，
// 每一步都可能打日志。没有统一 ID 时，并发高了你无法把
// 「这条 SQL 慢」和「这个用户报错」对应起来。
//
// 实现上优先复用上游传来的 X-Request-ID（网关/负载均衡通常会带），
// 但要校验格式：直接把用户可控的字符串写进日志会造成日志注入
// （往 header 里塞换行符就能伪造日志行）。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := sanitize(c.GetHeader("X-Request-ID"))
		if id == "" {
			id = newID()
		}
		// 存入 gin.Context，供 response 包读取。
		c.Set(response.ContextKeyRequestID, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// AccessLog 记录访问日志。
func AccessLog(l *logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		// 探活接口的高频日志会淹没有效信息，降级为 Debug。
		if path == "/healthz" || path == "/readyz" {
			l.Debug("request %s %s %d %v", c.Request.Method, path, c.Writer.Status(), time.Since(start))
			return
		}

		msg := "request %s %s status=%d elapsed=%v ip=%s"
		args := []any{c.Request.Method, path, c.Writer.Status(), time.Since(start), c.ClientIP()}
		switch {
		case c.Writer.Status() >= 500:
			l.Error(msg, args...)
		case c.Writer.Status() >= 400:
			l.Warn(msg, args...)
		default:
			l.Info(msg, args...)
		}
	}
}

// sanitize 只允许字母、数字、连字符、下划线，长度 ≤ 64。
// 不合法就返回空串，由调用方重新生成。
func sanitize(raw string) string {
	if raw == "" || len(raw) > 64 {
		return ""
	}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return ""
		}
	}
	return raw
}

// newID 生成 16 字节随机十六进制串。
//
// 不用 uuid 库是为了避免为本项目唯一的 UUID 用途引入一个依赖；
// 教学项目里少一个依赖就少一个 go.sum 维护负担。
// 真实项目若需要标准格式，换 uuid.NewString() 即可。
func newID() string {
	var b [16]byte
	// 用 crypto/rand 而不是 math/rand：
	// request_id 会出现在日志里，可预测的值容易被伪造/碰撞。
	if _, err := rand.Read(b[:]); err != nil {
		// 理论上不会失败（读 /dev/urandom）。真失败了也不该让请求失败，
		// 退化为时间戳即可 —— 唯一性下降，但服务可用。
		return "ts-" + time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
