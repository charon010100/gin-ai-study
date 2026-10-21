// Package handler 存放 HTTP 处理器。
//
// 本步新增了「服务端渲染 HTML」的处理器，用来和 JSON 接口对照。
package handler

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"ginlesson/step05/internal/model"
)

// Handler 持有处理器依赖。
type Handler struct {
	mu    sync.RWMutex
	posts []model.Post
	meta  model.SiteMeta
}

// New 创建处理器。
func New() *Handler {
	return &Handler{
		meta: model.SiteMeta{
			Title:    "我的第一个 Gin 网站",
			Subtitle: "Step 5：路由分组、静态资源与服务端渲染",
		},
		posts: []model.Post{
			{ID: "1", Title: "为什么 Gin 的 Context 必须用指针", Slug: "gin-context-pointer", Summary: "一次踩坑记录。", Views: 328, CreateAt: "2024-01-01"},
			{ID: "2", Title: "从零搭建 Go 后端的分层结构", Slug: "go-project-layout", Summary: "main 包应该有多薄。", Views: 186, CreateAt: "2024-01-02"},
			{ID: "3", Title: "Redis 缓存与数据库一致性", Slug: "redis-cache-consistency", Summary: "删缓存还是更新缓存？", Views: 45, CreateAt: "2024-01-03"},
		},
	}
}

// Index 渲染首页（HTML）。
//
// 这是本步引入的新东西：Handler 不只返回 JSON，也可以返回 HTML。
// 两种响应方式可以共存于同一个站点，路由前缀不同即可。
func (h *Handler) Index(c *gin.Context) {
	h.mu.RLock()
	posts := append([]model.Post(nil), h.posts...)
	h.mu.RUnlock()

	// 首页按浏览量降序，模拟「热门在前」。
	sort.Slice(posts, func(i, j int) bool { return posts[i].Views > posts[j].Views })

	// c.HTML 的第二个参数是**模板文件名**（不是路径），
	// 必须和 ParseFS 时注册的名字一致。用 embed 加载时，
	// 模板名是相对模式匹配后的文件名，即 "index.html"。
	c.HTML(http.StatusOK, "index.html", gin.H{
		"Title":    h.meta.Title,
		"Subtitle": h.meta.Subtitle,
		"Posts":    posts,
	})
}

// PostPage 渲染文章详情页（HTML）。
//
// 注意这个路由是 GET /posts/:slug（在根路径下），
// 和 JSON 接口的 GET /api/v1/posts/:slug 完全不同。
// 同一个资源提供两种表现形式，是「前后端不分离」站点的典型结构。
func (h *Handler) PostPage(c *gin.Context) {
	slug := c.Param("slug")

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, p := range h.posts {
		if p.Slug == slug {
			c.HTML(http.StatusOK, "post.html", gin.H{"Post": p, "Title": p.Title})
			return
		}
	}
	// HTML 页面的 404 和 API 的 404 不一样：这里要返回一个**页面**，
	// 而不是 JSON，否则用户在浏览器里会看到一段 JSON。
	c.HTML(http.StatusNotFound, "404.html", gin.H{"Title": "页面不存在"})
}

// ListPosts 返回文章列表（JSON）。
func (h *Handler) ListPosts(c *gin.Context) {
	h.mu.RLock()
	all := append([]model.Post(nil), h.posts...)
	h.mu.RUnlock()

	keyword := c.Query("keyword")
	filtered := make([]model.Post, 0, len(all))
	for _, p := range all {
		if keyword == "" || strings.Contains(p.Title, keyword) {
			filtered = append(filtered, p)
		}
	}
	c.JSON(http.StatusOK, gin.H{"list": filtered, "total": len(filtered)})
}

// Hot 返回热门文章（JSON）。
//
// 这个接口存在的意义是演示**静态路由优先于参数路由**：
// 我们同时注册了 GET /api/v1/posts/hot 和 GET /api/v1/posts/:slug。
// gin 会优先匹配精确的 /hot，而不是把它当成 slug="hot"。
func (h *Handler) Hot(c *gin.Context) {
	h.mu.RLock()
	posts := append([]model.Post(nil), h.posts...)
	h.mu.RUnlock()

	sort.Slice(posts, func(i, j int) bool { return posts[i].Views > posts[j].Views })
	if len(posts) > 2 {
		posts = posts[:2]
	}
	c.JSON(http.StatusOK, gin.H{"list": posts})
}

// GetPost 返回单篇文章（JSON）。
func (h *Handler) GetPost(c *gin.Context) {
	slug := c.Param("slug")
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, p := range h.posts {
		if p.Slug == slug {
			c.JSON(http.StatusOK, p)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"code": 4001, "message": "资源不存在"})
}

// DeletePost 删除文章（仅管理接口，用于演示分组鉴权）。
func (h *Handler) DeletePost(c *gin.Context) {
	slug := c.Param("slug")

	h.mu.Lock()
	defer h.mu.Unlock()

	for i, p := range h.posts {
		if p.Slug == slug {
			h.posts = append(h.posts[:i], h.posts[i+1:]...)
			c.JSON(http.StatusOK, gin.H{"message": "已删除", "slug": slug})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"code": 4001, "message": "资源不存在"})
}

// Stats 返回后台统计（演示如何从中间件写入的 context 里取值）。
func (h *Handler) Stats(c *gin.Context) {
	// 这里的 admin_user 是 adminOnly 中间件用 c.Set 写进来的。
	//
	// 注意用 c.Get 返回的是 (any, bool)：取出来要断言类型。
	// 直接 c.GetString 也行，但它在类型不对时返回空串而不报错，
	// 会把「中间件没执行」和「值是空串」两种情况混为一谈。
	v, ok := c.Get("admin_user")
	if !ok {
		// 正常情况下走不到这里（中间件会先拦截）。
		// 但永远不要假设上游中间件一定执行过——防御性检查的成本只有一行。
		c.JSON(http.StatusUnauthorized, gin.H{"code": 2001, "message": "请先登录"})
		return
	}

	h.mu.RLock()
	count := len(h.posts)
	h.mu.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"admin":      v,
		"post_count": count,
	})
}
