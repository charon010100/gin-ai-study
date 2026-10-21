// Package handler 存放 HTTP 处理器。
//
// 本步的处理器刻意做得极薄：部署这一步的重点不在业务，
// 而在「进程能不能被正确探测、被正确关停」。
package handler

import (
	"context"
	"net/http"
	"time"

	"ginlesson/step10/internal/model"
	"ginlesson/step10/internal/store"
	"github.com/gin-gonic/gin"
)

// Handler 持有依赖。用结构体而不是包级全局变量，
// 是为了让「依赖从哪来」在 main 里一眼可见。
type Handler struct {
	store   *store.Store
	version string
}

// New 创建处理器。
func New(st *store.Store, version string) *Handler {
	return &Handler{store: st, version: version}
}

// Live 是存活探针（liveness probe）。
//
// 它**只证明一件事**：HTTP 服务还能响应。不查数据库、不查 Redis、不查磁盘。
//
// 这一点必须守住。如果 liveness 里去 ping 数据库，那么数据库抖动 10 秒，
// 编排系统就会认为「这个容器坏了」，把它杀掉重启；重启后的新容器依然连不上数据库，
// 又被杀掉……几十个实例同时这样循环，本来只是数据库慢一下，最后变成
// 全站不可用 + 数据库被重启的连接风暴压垮。业内把它叫做 restart storm。
//
// 结论：liveness 只回答「进程要不要被重启」，答案是「除非它真挂了，否则不要」。
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready 是就绪探针（readiness probe）。
//
// 它回答的是完全不同的问题：**现在可以把流量交给这个实例吗？**
// 所以这里要检查依赖。依赖不可用时返回 503，负载均衡会把实例摘掉，
// 但**不会重启它** —— 数据库恢复后实例自动重新加入。
//
// 两个探针分开的意义就在于此：一个管「重启」，一个管「摘流量」。
// 合成一个端点，就必然要在「数据库抖一下就重启」和「数据库挂了还转发流量」
// 之间二选一，两个都是错的。
func (h *Handler) Ready(c *gin.Context) {
	// 每个依赖单独超时，避免一个慢依赖拖住整个探针。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	checks := make(map[string]string, 2)
	healthy := true

	if err := h.store.PingMySQL(ctx); err != nil {
		checks["mysql"] = "down"
		healthy = false
	} else {
		checks["mysql"] = "ok"
	}

	if err := h.store.PingRedis(ctx); err != nil {
		checks["redis"] = "down"
		healthy = false
	} else {
		checks["redis"] = "ok"
	}

	// 注意：响应里只暴露「哪个依赖挂了」，不暴露具体的错误信息。
	// 完整的错误写日志即可 —— 数据库错误信息里常带主机名、端口、库名，
	// 这些对攻击者是有价值的信息。
	status := http.StatusOK
	overall := "ok"
	if !healthy {
		status = http.StatusServiceUnavailable
		overall = "unavailable"
	}

	c.JSON(status, gin.H{
		"status":  overall,
		"checks":  checks,
		"version": h.version,
	})
}

// ListArticles 返回最新的 20 篇文章。
//
// 存在的意义是让「迁移是否真的执行过」有一个可验证的出口：
// 如果迁移没跑，这个接口会直接 500 并报 table doesn't exist，
// 而不是让服务看起来一切正常却什么都不工作。
func (h *Handler) ListArticles(c *gin.Context) {
	var list []model.Article

	// WithContext 必须传：它把 HTTP 请求的取消信号和超时传递给数据库驱动。
	// 少了它，客户端断开后查询仍会跑完，白白占用数据库连接。
	//
	// 用 err := ... 而不是 if err := db.Find(&list).Error; err != nil 的写法，
	// 是为了让错误处理显式地发生在这个函数里，而不是被忽略。
	db := h.store.DB.WithContext(c.Request.Context())
	if err := db.Order("created_at DESC").Limit(20).Find(&list).Error; err != nil {
		// 不要把 err.Error() 返回给客户端：里面可能含表名、SQL 片段。
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询文章失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"list": list})
}
