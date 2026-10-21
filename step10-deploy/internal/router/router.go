// Package router 只做 URL 到处理器的映射，外加一层极薄的访问日志。
package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"ginlesson/step10/internal/handler"
	"github.com/gin-gonic/gin"
)

// trustedProxies 是允许提供 X-Forwarded-For 的网段。
//
// 只信任内网：本项目的 nginx 与 app 在同一个 compose 网络里（172.16.0.0/12 范围内），
// 其余来源的转发头一律忽略，用 TCP 对端地址。
//
// 为什么必须显式配置：ClientIP() 的结果会被限流、黑名单、审计日志使用。
// 如果无条件信任 X-Forwarded-For，攻击者为每个请求伪造一个新 IP 就能绕过限流。
// 这类安全相关的行为不该依赖库的默认值——默认值会随版本变化，而你的安全假设不会。
var trustedProxies = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// New 构造 gin 引擎。
//
// 注意这里用的是 gin.New() 而不是 gin.Default()，原因有两个：
//   - gin.Default() 自带的 Logger 往标准输出写纯文本，无法接入结构化的 slog；
//   - 自带 Logger 无法排除 /healthz、/readyz 这类高频探针请求。
//     探针每隔几秒一次，会把真正有价值的访问日志淹没。
func New(h *handler.Handler) (*gin.Engine, error) {
	engine := gin.New()

	// SetTrustedProxies 的参数在编译期就是常量，正常不会失败；
	// 一旦失败说明网段写错了，必须让启动失败，而不是带着错误的安全配置跑起来。
	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return nil, fmt.Errorf("设置可信代理网段失败: %w", err)
	}

	// Recovery 必须保留。它把 panic 限制在单次请求内；
	// 没有它，一个 nil 解引用就会让整个进程退出，所有在线用户一起断线。
	// 这里用 gin.Recovery() 的默认行为（返回 500 + 打印堆栈）。
	engine.Use(gin.Recovery())

	// 访问日志放第二位：它在 Recovery 之后，所以能记录到
	// Recovery 处理完的 500 响应（而不是漏掉这次请求）。
	engine.Use(accessLog())

	// 探活端点不放在 /api 前缀下：它们是基础设施关注的事，不是业务 API，
	// 也不应该随接口版本升级而变化。K8s、docker-compose、负载均衡都按固定路径探测。
	engine.GET("/healthz", h.Live)
	engine.GET("/readyz", h.Ready)

	v1 := engine.Group("/api/v1")
	{
		v1.GET("/articles", h.ListArticles)
	}

	// 统一 404 返回 JSON，避免客户端拿到 gin 默认的纯文本，
	// 从而让「解析 JSON」在前端可以无条件执行。
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "接口不存在"})
	})

	return engine, nil
}

// skipLogPaths 是不需要记访问日志的路径。
//
// 用 map 而不是 if 链，方便将来加。判断逻辑独立成函数，
// 是为了让「哪些路径被静音」这件事在代码里可读。
var skipLogPaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
}

// accessLog 记录结构化访问日志。
//
// 只记录 method/path/status/耗时/客户端 IP。
// 刻意不记录 Query 与请求体：查询参数里可能有邮箱、token，
// 请求体里可能有密码 —— 日志系统通常权限更宽、留存更久，
// 把敏感数据写进去等于扩大了泄露面。
func accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		if _, skip := skipLogPaths[path]; skip {
			return
		}

		status := c.Writer.Status()
		attrs := []any{
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"elapsed_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
		}

		// 5xx 用 Error 级别：它是需要人介入的信号。
		// 4xx 用 Warn：客户端问题，但频繁的 4xx 也可能说明前端有 bug。
		switch {
		case status >= 500:
			slog.Error("请求失败", attrs...)
		case status >= 400:
			slog.Warn("请求返回异常状态", attrs...)
		default:
			slog.Info("请求完成", attrs...)
		}
	}
}
