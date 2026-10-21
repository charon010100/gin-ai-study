package handler

import (
	"strconv"
	"time"

	"ginlesson/step08/internal/model"
	"ginlesson/step08/internal/repository"
	"github.com/gin-gonic/gin"
)

// 本文件是 HTTP 与数据库之间的一层「翻译」。
//
// 为什么不直接把 model.Post 作为响应返回？
//
// 因为它含 DeletedAt、以及将来会有的内部字段（作者 IP、审核人、内部备注）。
// 一旦有人给模型加了字段，所有引用它的接口都会立刻把它吐出去 ——
// 而且没有任何代码改动、没有代码评审会注意到。
//
// 显式定义响应结构，让「对外契约」变成一份必须主动修改才能变更的东西。

// ---------- 请求 ----------

// listPostsQuery 是列表查询参数。
type listPostsQuery struct {
	Page     int    `form:"page"      binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1"`
	Keyword  string `form:"keyword"   binding:"omitempty,max=50"`
	Status   string `form:"status"    binding:"omitempty"`
}

// normalize 补默认值并夹住上限。
//
// 上限必须在服务端强制：客户端传 page_size=100000 时，
// 直接拿去 LIMIT 会一次性捞出全表，把内存和带宽打满。
// 注意 binding 的 max 只在参数存在时生效，所以这里必须再夹一次。
func (q *listPostsQuery) normalize() {
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

// createPostReq 是新建文章的入参。
type createPostReq struct {
	Title   string `json:"title"   binding:"required,min=1,max=200"`
	Slug    string `json:"slug"    binding:"required,min=2,max=200"`
	Summary string `json:"summary" binding:"max=500"`
	Content string `json:"content"`
	Status  string `json:"status"  binding:"omitempty"`
}

// createCommentReq 是提交评论的入参。
//
// 这里**故意没有** Status 字段。
// 如果加了它，无论 handler 里怎么写，都会有人图省事直接赋给模型。
// 让这个字段根本不存在，是防止「先审后发」被悄悄绕开的最有效手段。
type createCommentReq struct {
	Nickname string `json:"nickname" binding:"required,min=2,max=32"`
	Content  string `json:"content"  binding:"required,min=2,max=500"`
}

// ---------- 响应 ----------

type postItem struct {
	ID           uint64 `json:"id"`
	Title        string `json:"title"`
	Slug         string `json:"slug"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	Views        int64  `json:"views"`
	CreatedAt    string `json:"created_at"`
	CommentCount int64  `json:"comment_count"`
}

// postDetail 是详情，比列表项多了正文。
//
// 列表不返回正文的理由：一篇 5000 字的文章 × 10 条/页 = 50KB，
// 而列表页根本不需要正文。这类问题不会报错，只会慢。
type postDetail struct {
	postItem
	Content string `json:"content"`
}

type postListResp struct {
	// List 用 []postItem 而非 []any，且 handler 里用 make 初始化，
	// 保证空列表序列化成 [] 而不是 null。
	List     []postItem `json:"list"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

type commentItem struct {
	ID        uint64 `json:"id"`
	Nickname  string `json:"nickname"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// ---------- 转换 ----------

func toPostItem(r repository.PostWithCount) postItem {
	return postItem{
		ID:           r.ID,
		Title:        r.Title,
		Slug:         r.Slug,
		Summary:      r.Summary,
		Status:       r.Status,
		Views:        r.Views,
		CreatedAt:    formatTime(r.CreatedAt),
		CommentCount: r.CommentCount,
	}
}

func toPostDetail(p *model.Post) postDetail {
	return postDetail{
		postItem: postItem{
			ID:        p.ID,
			Title:     p.Title,
			Slug:      p.Slug,
			Summary:   p.Summary,
			Status:    p.Status,
			Views:     p.Views,
			CreatedAt: formatTime(p.CreatedAt),
		},
		Content: p.Content,
	}
}

func toCommentItem(c model.Comment) commentItem {
	return commentItem{
		ID:        c.ID,
		Nickname:  c.Nickname,
		Content:   c.Content,
		Status:    string(c.Status),
		CreatedAt: formatTime(c.CreatedAt),
	}
}

// formatTime 统一时间输出格式。
//
// 用 RFC3339 而不是 time.Time 默认序列化的原因：
// 默认格式带纳秒和时区偏移，前端各语言的解析器对它支持不一致。
// 另外先转 UTC 再格式化：DB 里存的就是 UTC，
// 时区转换是展示层的事，不该在 API 里绑定服务器本地时区。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseUintParam 解析路径里的数字参数。
//
// 抽成函数是因为「解析失败必须返回 400 并 return」这个模式会在每个
// 带 ID 的接口里出现。漏掉 return 是经典事故：
// 解析失败后 id 是 0，继续执行就会去查 id=0，
// 返回 404 而不是「你的 ID 格式不对」，排查时会往错误方向找。
//
// 返回 (值, 是否成功)，成功与否由调用方决定怎么响应。
func parseUintParam(c *gin.Context, name string) (uint64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": name + " 必须是正整数"})
		return 0, false
	}
	return id, true
}
