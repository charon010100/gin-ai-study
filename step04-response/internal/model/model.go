// Package model 存放跨层共享的数据结构。
package model

// Post 是一篇文章。
type Post struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Summary  string `json:"summary"`
	Author   string `json:"author"`
	Views    int64  `json:"views"`
	CreateAt string `json:"created_at"`
}

// Comment 是创建评论的请求体。
//
// 注意它和 CommentResp 是**两个**结构，不是共用一个。
// 理由：真实项目里 Comment 很快会加上 Email、IP、UserAgent、RiskScore 这些
// 不该外泄的字段。如果请求和响应共用一个结构，你就必须在每个返回点
// 手动排除它们——只要漏一处，访客邮箱就公开了。
//
// 分开定义之后，泄露**不可能**发生：响应结构里根本没有那个字段。
// 凡是靠「记得排除」来保证的安全，最终都会出事。
type Comment struct {
	Nickname string `json:"nickname" binding:"required,min=2,max=32"`
	Content  string `json:"content"  binding:"required,min=2,max=500"`
	Email    string `json:"email"    binding:"omitempty,email"`
}

// CommentResp 是创建评论的响应体。
type CommentResp struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	Content  string `json:"content"`
	// Status 固定为 pending，由服务端决定，不接受客户端输入。
	// 如果接受输入，攻击者直接提交 status=approved 就绕过了「先审后发」。
	Status string `json:"status"`
}
