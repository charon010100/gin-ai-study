package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS 处理跨域请求。
//
// 这个中间件的代码很短，但里面的每一个判断都是踩坑换来的。
// 建议配合 NOTES.md 的对照表一起看。
func CORS(allowedOrigins []string) gin.HandlerFunc {
	// 预先把白名单放进 map，避免每个请求都遍历切片。
	allowed := make(map[string]struct{}, len(allowedOrigins))
	wildcard := false
	for _, o := range allowedOrigins {
		if o == "*" {
			wildcard = true
		}
		allowed[o] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// 没有 Origin 头，说明不是跨域请求（同源请求或非浏览器客户端）。
		// 直接放行，不需要任何 CORS 头。
		if origin == "" {
			c.Next()
			return
		}

		// ---------- 关键判断：是否允许这个来源 ----------
		//
		// 两种情况允许：
		//   - 配了通配符 *
		//   - origin 在白名单里
		_, ok := allowed[origin]

		switch {
		case wildcard:
			// 注意：通配符只能配 `Allow-Origin: *`。
			// 如果还需要传 Cookie（凭证），**不能用 ***——
			// 浏览器会直接拒绝：`Access-Control-Allow-Origin: *`
			// 与 `credentials: 'include'` 不能共存。
			c.Header("Access-Control-Allow-Origin", "*")

		case ok:
			// 必须**回显具体 origin**，而不是写 *。
			// 这是携带凭证时唯一可行的做法。
			c.Header("Access-Control-Allow-Origin", origin)
			// 加了 Vary，告诉缓存服务器「响应内容取决于 Origin」。
			//
			// 少了这个头会有严重问题：CDN 可能把 A 站的响应缓存下来，
			// 然后返回给 B 站——于是 B 站拿到了 A 站的
			// Access-Control-Allow-Origin 头，被浏览器拒绝，
			// 表现为「有时候跨域成功，有时候失败」。
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")

		default:
			// 不在白名单：不设置任何 CORS 头，让浏览器自己拦截。
			//
			// 注意这里**不要**返回 403。
			// 为什么？因为 CORS 是浏览器端的限制，服务端返回 403 反而
			// 会让真正的请求被业务逻辑处理之前就失败，
			// 而浏览器端本来就会拦截响应。
			// 更实际的理由：返回 403 会暴露「这个来源被拒绝了」这一信息。
			c.Next()
			return
		}

		// ---------- 预检请求 ----------
		//
		// 对于「非简单请求」（PUT/DELETE、自定义头、Content-Type: application/json），
		// 浏览器会先发一个 OPTIONS 请求，问「我能不能这样发」。
		// 这个请求不会到你的 handler，必须在这里直接返回 204。
		if c.Request.Method == http.MethodOptions {
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Request-ID")
			// 预检结果可以缓存多久（秒）。10 分钟能显著减少 OPTIONS 请求。
			c.Header("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// SecurityHeaders 设置基础安全响应头。
//
// 这些头的成本几乎为零，但能防住一整类低级攻击。
// 对"只返回 JSON"的 API 来说收益有限，但如果你的服务同时也提供 HTML 页面
// （本教程的 Step 5 就是），它们就很必要了。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()

		// 禁止浏览器"猜"内容类型。
		//
		// 不加这个头时，浏览器可能把 `text/plain` 的响应当成 HTML 执行，
		// 于是用户上传的文件（如果被访问）可能变成 XSS 载体。
		h.Set("X-Content-Type-Options", "nosniff")

		// 禁止被 iframe 嵌入，防点击劫持。
		h.Set("X-Frame-Options", "DENY")

		// 跨站跳转时不发送完整 URL（可能含 token 等敏感查询参数）。
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// 强制 HTTPS（只在 HTTPS 站点上有意义，本地 http 会被忽略）。
		// includeSubDomains 要求所有子域都支持 HTTPS，配错了很难回退，所以保守起见没加。
		if c.Request.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		c.Next()
	}
}

// NormalizeOrigin 把配置里的 origin 统一格式，便于比较。
//
// 这个函数存在的理由是一个很常见的配置错误：
// 配置文件里写成 "http://localhost:3000/"（末尾多了斜杠），
// 而浏览器发来的 Origin 头是 "http://localhost:3000"（**没有**末尾斜杠）。
// 两者字符串比较永远不相等，于是 CORS 永远失败，
// 而且报错信息是浏览器端的「已被 CORS 策略阻止」，看不出原因。
func NormalizeOrigin(origin string) string {
	return strings.TrimSuffix(strings.TrimSpace(origin), "/")
}
