package handler

import (
	"errors"
	"log"

	"ginlesson/step08/internal/cache"
	"ginlesson/step08/internal/model"
	"ginlesson/step08/internal/repository"
	"github.com/gin-gonic/gin"
)

// Handler 持有它需要的依赖。
//
// 本步相比 Step 7 多了两样：缓存客户端与浏览量计数器。
// 注意它们都是「能力」而不是「数据表」—— handler 不需要知道
// 缓存里存的是什么结构，只需要知道怎么读、怎么失效。
type Handler struct {
	repo  repository.Repo
	cache *cache.Client
}

// New 构造处理器。
//
// 参数 repo 的类型是 repository.Repo（接口）。
// 调用方传进来的其实是「带缓存装饰器的仓储」（NewCachedRepo 的返回值），
// 但 handler 完全不需要知道这件事 —— 这就是接口的价值：
// 加缓存不需要改 handler 一行代码。
func New(repo repository.Repo, c *cache.Client) *Handler {
	return &Handler{repo: repo, cache: c}
}

// ListPosts 返回文章列表。
//
// 路径：GET /api/v1/posts
func (h *Handler) ListPosts(c *gin.Context) {
	var q listPostsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(400, gin.H{"error": "分页参数不合法"})
		return
	}
	q.normalize()

	status := q.Status
	if status == "" {
		status = "published"
	}
	if status != "published" && status != "draft" {
		c.JSON(400, gin.H{"error": "status 只能是 published 或 draft"})
		return
	}

	rows, total, err := h.repo.ListPosts(c.Request.Context(), repository.ListQuery{
		Page: q.Page, PageSize: q.PageSize, Keyword: q.Keyword, Status: status,
	})
	if err != nil {
		respondInternal(c, "查询文章列表失败", err)
		return
	}

	list := make([]postItem, 0, len(rows))
	for _, r := range rows {
		list = append(list, toPostItem(r))
	}
	c.JSON(200, postListResp{
		List: list, Total: total, Page: q.Page, PageSize: q.PageSize,
	})
}

// GetPost 按 slug 返回文章详情，并累加浏览量。
//
// 路径：GET /api/v1/posts/:slug
func (h *Handler) GetPost(c *gin.Context) {
	slug := c.Param("slug")
	ctx := c.Request.Context()

	p, err := h.repo.GetPostBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			c.JSON(404, gin.H{"error": "文章不存在"})
			return
		}
		respondInternal(c, "查询文章失败", err)
		return
	}

	// 浏览量走 Redis，不写库。
	//
	// 这里返回给客户端的数字是「DB 里的旧值 + Redis 里的增量」，
	// 所以能看到它实时增长，而数据库一分钟才被写一次。
	//
	// 注意 incrOK 为 false 时（Redis 不可用）要退回显示 DB 值：
	// 计数失败不该让接口报错，最多是「这次没加 1」。
	if delta, incrOK := h.cache.IncrViews(ctx, p.ID); incrOK {
		p.Views += delta
	}

	c.JSON(200, toPostDetail(p))
}

// CreatePost 新建文章。
func (h *Handler) CreatePost(c *gin.Context) {
	var req createPostReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数不合法：" + err.Error()})
		return
	}

	status := req.Status
	if status == "" {
		status = "published"
	}
	if status != "published" && status != "draft" {
		c.JSON(400, gin.H{"error": "status 只能是 published 或 draft"})
		return
	}

	p := model.Post{
		Title: req.Title, Slug: req.Slug, Summary: req.Summary,
		Content: req.Content, Status: status,
	}
	if err := h.repo.CreatePost(c.Request.Context(), &p); err != nil {
		switch {
		case errors.Is(err, model.ErrConflict):
			c.JSON(409, gin.H{"error": "slug 已被占用，换一个"})
		default:
			respondInternal(c, "创建文章失败", err)
		}
		return
	}
	c.JSON(201, toPostDetail(&p))
}

// DeletePost 软删除文章。
func (h *Handler) DeletePost(c *gin.Context) {
	if err := h.repo.DeletePost(c.Request.Context(), c.Param("slug")); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			c.JSON(404, gin.H{"error": "文章不存在"})
			return
		}
		respondInternal(c, "删除文章失败", err)
		return
	}
	c.Status(204)
}

// ListComments 返回已通过审核的评论。
func (h *Handler) ListComments(c *gin.Context) {
	p, err := h.repo.GetPostBySlug(c.Request.Context(), c.Param("slug"))
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			c.JSON(404, gin.H{"error": "文章不存在"})
			return
		}
		respondInternal(c, "查询文章失败", err)
		return
	}

	comments, err := h.repo.ListComments(c.Request.Context(), p.ID, model.CommentApproved)
	if err != nil {
		respondInternal(c, "查询评论失败", err)
		return
	}

	items := make([]commentItem, 0, len(comments))
	for _, cm := range comments {
		items = append(items, toCommentItem(cm))
	}
	c.JSON(200, gin.H{"list": items, "total": len(items)})
}

// CreateComment 提交评论，状态强制为 pending。
//
// 这个接口挂载了限流中间件（见 router），
// 所以到达这里的请求已经过了「同 IP 每分钟 N 次」的检查。
func (h *Handler) CreateComment(c *gin.Context) {
	var req createCommentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数不合法：" + err.Error()})
		return
	}

	p, err := h.repo.GetPostBySlug(c.Request.Context(), c.Param("slug"))
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			c.JSON(404, gin.H{"error": "文章不存在"})
			return
		}
		respondInternal(c, "查询文章失败", err)
		return
	}

	cm := model.Comment{
		PostID: p.ID, Nickname: req.Nickname, Content: req.Content,
		// 状态仍由服务端强制。限流只能减少垃圾量，
		// 不能替代审核 —— 限流挡的是「频率」，不是「内容」。
		Status: model.CommentPending,
	}
	if err := h.repo.CreateComment(c.Request.Context(), &cm); err != nil {
		respondInternal(c, "创建评论失败", err)
		return
	}
	c.JSON(201, toCommentItem(cm))
}

// ApproveComment 审核通过。
func (h *Handler) ApproveComment(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.repo.ApproveComment(c.Request.Context(), id); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			c.JSON(404, gin.H{"error": "没有找到可审核的待审评论"})
			return
		}
		respondInternal(c, "审核评论失败", err)
		return
	}
	c.JSON(200, gin.H{"message": "已通过审核"})
}

// ---------- 辅助 ----------

// respondInternal 统一处理不该暴露细节的服务端错误。
func respondInternal(c *gin.Context, msg string, err error) {
	log.Printf("[ERROR] %s: %v", msg, err)
	c.JSON(500, gin.H{"error": msg})
}
