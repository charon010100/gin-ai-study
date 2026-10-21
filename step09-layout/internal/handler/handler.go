// Package handler 是 HTTP 适配层。
//
// 它做三件事，且只做这三件：
//  1. 从请求里取出参数（路径/查询/body）
//  2. 调用 service
//  3. 把结果或错误写成 HTTP 响应
//
// 判断标准：需要 HTTP 状态码或 gin.Context 的代码属于这里；
// 关心业务规则的代码不属于这里。
//
// 特别注意：这个包**没有 import gorm，也没有 import repository**。
// 这不是巧合，是分层正确的标志 —— 见 NOTES 的依赖清单。
package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"ginlesson/step09/internal/service"
	"ginlesson/step09/pkg/response"
)

// PostHandler 处理文章相关请求。
type PostHandler struct {
	posts *service.PostService
}

// NewPostHandler 构造处理器。
//
// 接收的是 service（具体类型）而不是接口。
// 为什么这里不用接口：handler 的测试价值较低（它几乎没有逻辑），
// 而为 PostService 造接口会让「service 有哪些方法」变成两份定义。
// 接口应该加在有多个实现、或需要隔离外部依赖的地方。
func NewPostHandler(posts *service.PostService) *PostHandler {
	return &PostHandler{posts: posts}
}

// List 返回前台文章列表。
//
// GET /api/v1/posts
func (h *PostHandler) List(c *gin.Context) {
	var q listPostsQuery
	if !bindQuery(c, &q) {
		return
	}

	// 注意这里调用的是 ListPublic，参数里没有 status。
	// 「前台只能看到已发布文章」这条规则由 service 保证，
	// 而不是靠 handler 记得传 status=published。
	res, err := h.posts.ListPublic(c.Request.Context(), q.Page, q.PageSize, q.Keyword)
	if err != nil {
		mapServiceError(c, err)
		return
	}

	list := make([]postItemResp, 0, len(res.Items))
	for _, it := range res.Items {
		list = append(list, toPostItemResp(it))
	}
	response.OK(c, postListResp{
		List:     list,
		Total:    res.Total,
		Page:     res.Page,
		PageSize: res.PageSize,
	})
}

// Get 返回文章详情。
//
// GET /api/v1/posts/:slug
func (h *PostHandler) Get(c *gin.Context) {
	p, err := h.posts.GetBySlug(c.Request.Context(), c.Param("slug"))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, toPostDetailResp(p))
}

// Create 新建文章。
//
// POST /api/v1/posts
func (h *PostHandler) Create(c *gin.Context) {
	var req createPostReq
	if !bindJSON(c, &req) {
		return
	}

	p, err := h.posts.Create(c.Request.Context(), service.CreatePostInput{
		Title:   req.Title,
		Slug:    req.Slug,
		Summary: req.Summary,
		Content: req.Content,
		Status:  req.Status,
	})
	if err != nil {
		mapServiceError(c, err)
		return
	}
	response.Created(c, toPostDetailResp(p))
}

// Delete 删除文章。
//
// DELETE /api/v1/posts/:slug
func (h *PostHandler) Delete(c *gin.Context) {
	if err := h.posts.Delete(c.Request.Context(), c.Param("slug")); err != nil {
		mapServiceError(c, err)
		return
	}
	// 204：成功且无响应体。204 不允许写 body，所以不能 c.JSON。
	response.NoContent(c)
}

// CommentHandler 处理评论相关请求。
type CommentHandler struct {
	comments *service.CommentService
}

// NewCommentHandler 构造处理器。
func NewCommentHandler(comments *service.CommentService) *CommentHandler {
	return &CommentHandler{comments: comments}
}

// List 返回已通过审核的评论。
//
// GET /api/v1/posts/:slug/comments
func (h *CommentHandler) List(c *gin.Context) {
	comments, err := h.comments.ListApproved(c.Request.Context(), c.Param("slug"))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	list := make([]commentResp, 0, len(comments))
	for _, cm := range comments {
		list = append(list, toCommentResp(cm))
	}
	response.OK(c, gin.H{"list": list, "total": len(list)})
}

// Submit 提交评论。
//
// POST /api/v1/posts/:slug/comments
func (h *CommentHandler) Submit(c *gin.Context) {
	var req createCommentReq
	if !bindJSON(c, &req) {
		return
	}

	cm, err := h.comments.Submit(c.Request.Context(), c.Param("slug"), service.CreateCommentInput{
		Nickname: req.Nickname,
		Content:  req.Content,
	})
	if err != nil {
		mapServiceError(c, err)
		return
	}

	// 201 而非 200：确实创建了新资源。
	// 响应里明确带 status=pending，让提交者知道自己在等审核，
	// 而不是以为已经发布成功。
	response.Created(c, toCommentResp(*cm))
}

// Approve 审核通过（后台接口，本步尚未加鉴权）。
//
// PUT /api/v1/admin/comments/:id/approve
func (h *CommentHandler) Approve(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		// 解析失败必须返回 400 并 return。
		// 忽略后 id 会是 0，继续执行就会去查 id=0，
		// 返回 404 而不是「你的 ID 格式不对」，排查时会往错误方向找。
		response.Fail(c, 400, response.CodeInvalid, "评论 id 必须是正整数")
		return
	}

	if err := h.comments.Approve(c.Request.Context(), id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, gin.H{"message": "已通过审核"})
}

// Pending 返回待审队列（后台接口）。
//
// GET /api/v1/admin/comments/pending
func (h *CommentHandler) Pending(c *gin.Context) {
	comments, err := h.comments.ListPending(c.Request.Context(), 20)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	list := make([]commentResp, 0, len(comments))
	for _, cm := range comments {
		list = append(list, toCommentResp(cm))
	}
	response.OK(c, gin.H{"list": list, "total": len(list)})
}
