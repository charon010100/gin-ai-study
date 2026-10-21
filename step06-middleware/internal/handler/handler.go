// Package handler 存放 HTTP 处理器。
//
// 本步的 handler 特别简单——因为「横切关注点」（日志、限流、CORS）
// 都已经被中间件接管了。handler 只需要关心业务。
//
// 这本身就是中间件存在的意义：让 handler 变干净。
package handler

import (
	"net/http"
	"sort"
	"sync"

	"github.com/gin-gonic/gin"
	"ginlesson/step06/internal/middleware"
	"ginlesson/step06/internal/model"
)

// Handler 持有处理器依赖。
type Handler struct {
	mu    sync.RWMutex
	posts []model.Post
}

// New 创建处理器。
func New() *Handler {
	return &Handler{
		posts: []model.Post{
			{ID: "1", Title: "中间件的 c.Next() 到底是什么", Slug: "gin-next", Views: 328},
			{ID: "2", Title: "为什么 Recovery 必须放最外层", Slug: "gin-recovery-order", Views: 186},
			{ID: "3", Title: "进程内限流为什么只能单实例", Slug: "in-process-ratelimit", Views: 45},
		},
	}
}

// Ping 用来观察中间件链路是否正常。
//
// 它会返回本次请求的 request_id，证明中间件确实把值写进了 context。
func (h *Handler) Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message": "pong",
		// 这个 ID 来自 RequestID 中间件。
		// 注意：响应头里也有同一个值（X-Request-ID），
		// 这里放进 body 只是为了让 curl 一下就能看到。
		"request_id": middleware.RequestIDFrom(c.Request.Context()),
		// IP 来自 ClientIP 中间件。
		"client_ip": middleware.ClientIPFrom(c.Request.Context()),
	})
}

// ListPosts 返回文章列表。
func (h *Handler) ListPosts(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	p := append([]model.Post(nil), h.posts...)
	sort.Slice(p, func(i, j int) bool { return p[i].Views > p[j].Views })
	c.JSON(http.StatusOK, gin.H{"list": p, "total": len(p)})
}

// CreateComment 创建评论（用来演示限流）。
//
// 每调用一次就消耗一个令牌。连续快速调用 6 次就能触发 429。
func (h *Handler) CreateComment(c *gin.Context) {
	c.JSON(http.StatusCreated, gin.H{
		"message": "评论已提交，等待审核",
		"status":  "pending",
	})
}

// Panic 故意触发 panic，用来验证 Recovery 中间件。
func (h *Handler) Panic(c *gin.Context) {
	var m map[string]string
	m["boom"] = "往 nil map 里写值，必然 panic"
	_ = m
}

// AdminStats 返回后台统计，用来演示「分组 + 鉴权中间件」。
func (h *Handler) AdminStats(c *gin.Context) {
	admin, ok := middleware.AdminFrom(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"code": 2001, "message": "请先登录"})
		return
	}

	h.mu.RLock()
	count := len(h.posts)
	h.mu.RUnlock()

	c.JSON(http.StatusOK, gin.H{"admin": admin, "post_count": count})
}

// Echo 让人直观看到 request_id 的两个来源。
//
// 注意这两个值通常**不相同**，而且这个区别很容易搞混：
//
//	c.GetHeader(...) 读的是**客户端发来的**请求头，原样，可能为空
//	RequestIDFrom()  读的是中间件最终采用的 ID（复用或重新生成）
//
// 设置响应头用的是 c.Header(...)，写的是**响应**头，
// 不会影响 c.GetHeader 读到的东西——一个请求头和响应头是两套东西，
// 但名字可以同名，非常容易看错。
func (h *Handler) Echo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		// 客户端自己发来的（可能为空，也可能是非法格式）
		"client_sent": c.GetHeader(middleware.RequestIDHeader),
		// 中间件最终采用的：合法就复用，不合法就重新生成
		"effective": middleware.RequestIDFrom(c.Request.Context()),
	})
}
