// Package middleware 提供自定义中间件。
//
// 为什么要把中间件单独成包？
//
// 因为中间件是「横切关注点」：日志、鉴权、限流、CORS 这些东西和具体业务无关，
// 却要作用在每一个请求上。它们的共同特点是：
//
//   - 不应该出现在 handler 里（handler 只该关心业务）
//   - 需要被多个路由复用
//   - 需要能够独立测试
//
// 放在 internal/middleware 里，router 包按需组合，职责就很清晰了。
package middleware

import (
	"context"

	"github.com/gin-gonic/gin"
)

// contextKey 是本包私有的 context key 类型。
//
// 为什么自定义类型而不是直接用 string？
//
// 因为 context.WithValue 的 key 是 any。如果所有包都用 "request_id" 这个字符串，
// 那么只要有一个包写入同名 key，就会静默覆盖别人的值——而且谁都不会报错。
//
// 用**未导出的自定义类型**做 key，可以保证：
//   - 外部包拿不到这个类型，无法构造同名 key（不会冲突）
//   - 编译器帮你检查类型（不会有拼写错误）
//
// 这是 Go 官方推荐的 context 用法，也是 `go vet` 会提醒的点。
type contextKey struct{ name string }

var (
	requestIDKey = &contextKey{name: "request_id"}
	clientIPKey  = &contextKey{name: "client_ip"}
)

// adminGinKey 是往 gin.Context 里存管理员用的 key。
//
// 这里用普通字符串而不是自定义类型，是因为 gin 的 c.Set/c.Get 本身就用
// map[string]any 存储——它的 key 天生就是字符串，无法享受自定义类型的隔离。
// 所以 gin 侧靠「命名约定」避免冲突（加项目前缀），context 侧则用类型隔离。
const adminGinKey = "ginblog:admin_user"

// RequestIDFrom 从请求上下文取 request_id。
//
// 为什么读 context 而不是 c.Get("request_id")？
//
// 因为 context 可以被传递给任何函数（包括不依赖 gin 的底层函数），
// 而 c.Get 只有拿到 *gin.Context 的地方才能用。日志工具、数据库层、
// 业务函数通常只接收 context.Context，用 context 传递才能贯穿全链路。
//
// 本教程两者都演示了：request_id 走 context（因为要跨层），
// admin_user 走 c.Get（因为只在 handler 里用）。
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// ClientIPFrom 从请求上下文取客户端 IP。
func ClientIPFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(clientIPKey).(string); ok {
		return v
	}
	return ""
}

// AdminFrom 从 gin 上下文取当前登录用户。
//
// 用 c.Get 而不是 context，因为只有 handler 需要它。
// 返回 (string, bool)：调用方必须显式处理「不存在」的情况，
// 而不是拿到空字符串继续用（那会把「没登录」和「用户名为空」混为一谈）。
func AdminFrom(c *gin.Context) (string, bool) {
	v, ok := c.Get(adminGinKey)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// SetAdmin 供鉴权中间件把当前用户写入上下文。
func SetAdmin(c *gin.Context, name string) {
	c.Set(adminGinKey, name)
}

// withRequestID / withClientIP 是本包内部写 context 的地方。
// 不导出，是为了保证「只有本包的中间件能写这两个 key」，
// 避免业务代码到处 WithValue 导致数据来源不明。
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func withClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}
