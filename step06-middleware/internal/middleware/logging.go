package middleware

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// ClientIP 解析客户端 IP 并写入 context。
//
// 为什么单独一个中间件，而不是在 handler 里调 c.ClientIP()？
//
// 因为「是否信任 X-Forwarded-For」是一个**部署决策**，不是业务决策。
// 而且它直接影响安全：
//
//	如果服务直接暴露在公网（没有反向代理），那么客户端可以随便伪造
//	X-Forwarded-For: 1.2.3.4。此时如果限流/黑名单按这个头来判断，
//	攻击者换个头就绕过了 —— 等于没有限流。
//
//	只有当服务**明确位于可信代理之后**（Nginx/LB 会覆盖这个头），
//	信任它才是安全的。
//
// 所以用一个参数显式表达这件事，而不是默默相信。
func ClientIP(trustProxy bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		// c.ClientIP() 会读取 X-Forwarded-For / X-Real-IP（如果配置了可信代理），
		// 否则回退到 TCP 对端地址。
		ip := c.ClientIP()

		if !trustProxy {
			// 不信任代理头时，只认 TCP 对端地址。
			// RemoteIP() 直接取连接的远端地址，客户端无法伪造。
			ip = c.RemoteIP()
		}

		c.Request = c.Request.WithContext(withClientIP(c.Request.Context(), ip))
		c.Next()
	}
}

// Logger 是访问日志中间件。
//
// 为什么不用 gin 自带的 gin.Logger()？
//
// 因为它的格式是固定的，改不了。而你需要：
//   - 带上 request_id（串联同一次请求的日志）
//   - 输出结构化日志（JSON，便于采集到 ELK/Loki 后按字段检索）
//   - 按状态码分级（4xx 是正常业务，5xx 才是故障）
//   - 过滤探活接口的噪音（否则日志里 99% 是 /healthz）
//
// 自己写一个，代码不到 40 行，但把上面四件事都解决了。
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		// c.Next() 执行后续所有中间件和 handler。
		//
		// 这是中间件最核心的机制：Next() **之前**的代码在请求进入时执行，
		// Next() **之后**的代码在响应写完之后执行。
		//
		// 所以「计算耗时」必须放在 Next 之后——因为 Next 返回时，
		// handler 才刚执行完。
		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		reqID := RequestIDFrom(c.Request.Context())

		// 用 log.Printf 是为了不引入日志库依赖（Step 9/10 会换成 zap）。
		// 真实项目应该输出 JSON 结构化日志。
		switch {
		case status >= 500:
			// 5xx：服务端自己的问题，需要警觉。
			log.Printf("[ERROR] %s %s | %d | %v | %s | rid=%s",
				method, path, status, latency.Round(time.Microsecond), c.ClientIP(), reqID)
		case status >= 400:
			// 4xx：客户端的问题，属于正常业务流转，不需要告警。
			log.Printf("[WARN ] %s %s | %d | %v | %s | rid=%s",
				method, path, status, latency.Round(time.Microsecond), c.ClientIP(), reqID)
		default:
			// 探活接口会被频繁调用，降到 debug 语义（这里用 INFO 但带标记，
			// 真实项目应该走独立的日志级别，避免淹没正常日志）。
			if isHealthPath(path) {
				return
			}
			log.Printf("[INFO ] %s %s | %d | %v | %s | rid=%s",
				method, path, status, latency.Round(time.Microsecond), c.ClientIP(), reqID)
		}
	}
}

func isHealthPath(path string) bool {
	return path == "/healthz" || path == "/readyz"
}
