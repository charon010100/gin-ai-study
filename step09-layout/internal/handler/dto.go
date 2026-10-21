package handler

import (
	"time"

	"ginlesson/step09/internal/model"
	"ginlesson/step09/internal/service"
)

// 本文件是 HTTP 与 service 之间的翻译层。
//
// 为什么不把 model.Post 直接返回给客户端：
// 它含 DeletedAt 这类内部字段，而且将来一定会加更多。
// 一旦有人给模型加字段，所有引用它的接口都会自动把它吐出去 ——
// 没有代码改动，没有评审会注意到。
//
// 显式定义响应 DTO，让「对外契约」变成一份必须主动修改才能变更的东西。

// ---------- 请求 ----------

type listPostsQuery struct {
	Page     int    `form:"page"      binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1"`
	Keyword  string `form:"keyword"   binding:"omitempty,max=50"`
}

type createPostReq struct {
	Title   string `json:"title"   binding:"required,min=1,max=200"`
	Slug    string `json:"slug"    binding:"required,min=2,max=200"`
	Summary string `json:"summary" binding:"max=500"`
	Content string `json:"content"`
	Status  string `json:"status"  binding:"omitempty"`
}

// createCommentReq 故意没有 Status 字段。
//
// service.CreateCommentInput 也没有 —— 两层都不给外部修改状态的能力。
// 这不是重复防御，而是「防呆」：让错误的代码写得出来但编不过，
// 比如 req.Status 直接赋给 Input 时编译失败。
type createCommentReq struct {
	Nickname string `json:"nickname" binding:"required,min=2,max=32"`
	Content  string `json:"content"  binding:"required,min=2,max=500"`
}

// ---------- 响应 ----------

type postItemResp struct {
	ID           uint64 `json:"id"`
	Title        string `json:"title"`
	Slug         string `json:"slug"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	Views        int64  `json:"views"`
	CommentCount int64  `json:"comment_count"`
	CreatedAt    string `json:"created_at"`
}

type postDetailResp struct {
	postItemResp
	Content string `json:"content"`
}

type postListResp struct {
	// 用 [] 而不是 nil，保证空列表序列化成 [] 而不是 null。
	List     []postItemResp `json:"list"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
}

type commentResp struct {
	ID        uint64 `json:"id"`
	Nickname  string `json:"nickname"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// ---------- 转换 ----------

func toPostItemResp(p service.PostItem) postItemResp {
	return postItemResp{
		ID:           p.ID,
		Title:        p.Title,
		Slug:         p.Slug,
		Summary:      p.Summary,
		Status:       p.Status,
		Views:        p.Views,
		CommentCount: p.CommentCount,
		CreatedAt:    formatTime(p.CreatedAt),
	}
}

func toPostDetailResp(p *model.Post) postDetailResp {
	return postDetailResp{
		postItemResp: postItemResp{
			ID:           p.ID,
			Title:        p.Title,
			Slug:         p.Slug,
			Summary:      p.Summary,
			Status:       string(p.Status),
			Views:        p.Views,
			CommentCount: p.CommentCount,
			CreatedAt:    formatTime(p.CreatedAt),
		},
		Content: p.Content,
	}
}

func toCommentResp(c model.Comment) commentResp {
	return commentResp{
		ID:        c.ID,
		Nickname:  c.Nickname,
		Content:   c.Content,
		Status:    string(c.Status),
		CreatedAt: formatTime(c.CreatedAt),
	}
}

// formatTime 统一输出 RFC3339。
//
// 不用 time.Time 默认序列化：它带纳秒和时区偏移，
// 前端各语言解析器的支持程度不一。RFC3339 是各方都认的标准。
// 先转 UTC 再格式化：DB 里存的就是 UTC，时区转换是展示层的事。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
