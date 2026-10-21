// Package handler 存放 HTTP 处理器。
//
// 与 Step 2 相比，本步的 handler 里不再出现任何 http 状态码字面量
// 和 gin.H —— 所有响应都走 pkg/response。这看起来是多写了一层，
// 但换来了「格式只有一处定义」。
package handler

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"ginlesson/step04/internal/model"
	"ginlesson/step04/pkg/response"
)

// Handler 持有处理器需要的依赖。
type Handler struct {
	mu    sync.RWMutex
	posts []model.Post
	seq   int
}

// New 创建处理器。
func New() *Handler {
	return &Handler{
		posts: []model.Post{
			{ID: "1", Title: "为什么 Gin 的 Context 必须用指针", Slug: "gin-context-pointer", Summary: "一次踩坑记录。", Author: "博主", Views: 128, CreateAt: "2024-01-01T10:00:00Z"},
			{ID: "2", Title: "从零搭建 Go 后端的分层结构", Slug: "go-project-layout", Summary: "main 包应该有多薄。", Author: "博主", Views: 86, CreateAt: "2024-01-02T10:00:00Z"},
			{ID: "3", Title: "Redis 缓存与数据库一致性", Slug: "redis-cache-consistency", Summary: "删缓存还是更新缓存？", Author: "博主", Views: 45, CreateAt: "2024-01-03T10:00:00Z"},
		},
	}
}

// ListPosts 返回文章列表。
func (h *Handler) ListPosts(c *gin.Context) {
	var q ListPostsQuery
	// 查询参数也可能校验失败，同样走 BindJSON 那套错误转换逻辑——
	// 只是绑定方法换成 ShouldBindQuery。
	if err := c.ShouldBindQuery(&q); err != nil {
		response.Fail(c, response.ErrInvalidParam.Wrap(err))
		return
	}
	q.normalize()

	h.mu.RLock()
	all := h.posts
	h.mu.RUnlock()

	filtered := make([]model.Post, 0, len(all))
	for _, p := range all {
		if q.Keyword != "" && !strings.Contains(p.Title, q.Keyword) {
			continue
		}
		filtered = append(filtered, p)
	}

	total := len(filtered)
	start := (q.Page - 1) * q.PageSize
	if start > total {
		start = total
	}
	end := start + q.PageSize
	if end > total {
		end = total
	}

	// 用泛型分页结构：调用方拿到的就是 response.Page[model.Post]，
	// 不需要 any 断言。
	response.OK(c, response.NewPage(filtered[start:end], int64(total), q.Page, q.PageSize))
}

// GetPost 按 slug 返回单篇文章。
func (h *Handler) GetPost(c *gin.Context) {
	slug := c.Param("slug")

	// 演示用：slug 为 "boom" 时故意返回一个内部错误。
	//
	// 目的是让你看到「内部错误只进日志、不进响应」的实际效果：
	// 客户端看到的是「服务器内部错误」，而日志里是完整的连接失败原因。
	if slug == "boom" {
		response.Fail(c, response.ErrDatabase.Wrap(errors.New(
			"dial tcp 127.0.0.1:3306: connect: connection refused; sql: SELECT * FROM blog_post WHERE slug = ?",
		)))
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, p := range h.posts {
		if p.Slug == slug {
			response.OK(c, p)
			return
		}
	}

	// 资源不存在：用预定义错误。
	// 注意这里**不需要** return 之后再写任何东西 —— Fail 内部已经 Abort，
	// 但 Go 里仍然需要显式 return 来结束函数。
	response.Fail(c, response.ErrNotFound.Wrap(fmt.Errorf("post not found: %s", slug)))
}

// CreateComment 创建一条评论。
func (h *Handler) CreateComment(c *gin.Context) {
	slug := c.Param("slug")

	// ① 绑定 + 校验，**只绑定一次**。
	//
	// 这里有个真实的坑值得单独说：请求体是「流」，只能读一次。
	// 如果你写成：
	//
	//	var a model.Comment
	//	c.ShouldBindJSON(&a)          // 读走 body
	//	var b model.Comment
	//	c.ShouldBindJSON(&b)          // ← 第二次必然失败：EOF
	//
	// 第二次会收到 EOF 错误，因为 body 已经被读空了。
	// 需要同时做「校验」和「取值」时，必须绑定到同一个变量，
	// 而不是为了「先校验再取」绑两次。
	//
	// 对比 Step 2 的写法：
	//   Step 2: c.JSON(400, gin.H{"error": err.Error()})   ← 泄露内部结构
	//   本步:   response.Fail(c, BindJSON(...))            ← 统一格式 + 人话
	var req model.Comment
	if appErr := BindJSON(c, &req); appErr != nil {
		response.Fail(c, appErr)
		return
	}

	// ② 检查父资源是否存在。
	h.mu.RLock()
	found := false
	for _, p := range h.posts {
		if p.Slug == slug {
			found = true
			break
		}
	}
	h.mu.RUnlock()

	if !found {
		response.Fail(c, response.ErrNotFound.Wrap(fmt.Errorf("post not found: %s", slug)))
		return
	}

	// ③ 分配 ID。
	h.mu.Lock()
	h.seq++
	id := fmt.Sprintf("c%d", h.seq)
	h.mu.Unlock()

	// ④ 返回 201。
	//
	// 状态固定 pending：这是服务端决定的值，绝不接受客户端输入。
	response.Created(c, model.CommentResp{
		ID: id, Nickname: req.Nickname, Content: req.Content, Status: "pending",
	})
}

// ListPostsQuery 是列表接口的查询参数。
type ListPostsQuery struct {
	Page     int    `form:"page"      binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=50"`
	Keyword  string `form:"keyword"   binding:"omitempty,max=50"`
}

func (q *ListPostsQuery) normalize() {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.PageSize <= 0 {
		q.PageSize = 10
	}
	if q.PageSize > 50 {
		q.PageSize = 50
	}
}
