// Package repository 是数据访问层：唯一允许出现 GORM/SQL 的地方。
//
// 本包定义**接口**，实现放在同包的 gorm_repo.go 里。
// service 层依赖接口（见 service/*.go 的构造函数签名），
// 所以测试时可以用假实现替换掉数据库。
package repository

import (
	"context"

	"ginlesson/step09/internal/model"
)

// 本包的错误语义。
//
// 定义在这里而不是 model 包，是因为它们描述的是「数据访问出了什么状况」，
// 不是「业务规则是什么」。model 包只负责数据结构。
//
// 用哨兵错误而不是自定义类型：调用方只需要 errors.Is 判断，
// 不需要携带额外信息。如果错误需要携带上下文（比如"哪个 slug 冲突了"），
// 再考虑自定义类型。
var (
	// ErrNotFound 查不到目标记录。
	ErrNotFound = resultError("资源不存在")
	// ErrConflict 违反唯一性约束。
	ErrConflict = resultError("资源冲突")
)

// resultError 是轻量的哨兵错误类型。
//
// 为什么不用 errors.New：用自定义类型可以让 errors.Is 通过类型匹配，
// 即使错误被 fmt.Errorf("...: %w", err) 包装过也仍然可识别。
// errors.New 的哨兵值也支持 Is（按值比较），但类型匹配对包装更稳健。
type resultError string

func (e resultError) Error() string { return string(e) }

// ListQuery 是列表查询入参。
type ListQuery struct {
	Page     int
	PageSize int
	Keyword  string
	Status   model.PostStatus
}

// PostWithCount 是列表项：文章 + 评论数。
//
// 这里的 CommentCount 来自聚合查询，不是 posts 表的列 ——
// 放在单独的 DTO 里而不是塞进 model.Post，是为了让「哪些字段是表数据、
// 哪些是计算出来的」在类型上可见。
type PostWithCount struct {
	model.Post
	CommentCount int64
}

// PostRepo 描述文章的持久化能力。
//
// 接口定义在「使用方」这一侧还是「实现方」这一侧？
// Go 的惯例是：接口由使用方定义（consumer-side interface）。
// 但教学项目里把接口和实现放在同一个包更好理解，
// Step 9 的重点是「谁依赖谁」而不是「接口放在哪个包」。
type PostRepo interface {
	ListPosts(ctx context.Context, q ListQuery) ([]PostWithCount, int64, error)
	GetPostBySlug(ctx context.Context, slug string) (*model.Post, error)
	GetPostByID(ctx context.Context, id uint64) (*model.Post, error)
	CreatePost(ctx context.Context, p *model.Post) error
	DeletePost(ctx context.Context, slug string) error
	IncrPostViews(ctx context.Context, deltas map[uint64]int64) error
	// IncrCommentCount 维护冗余的评论计数。
	//
	// 为什么这个方法在 repository 而不是「让 service 直接 UPDATE」：
	// service 不该知道 SQL；而「计数 +1」是一个纯粹的持久化动作，
	// 放在 repository 是合适的。什么时候调用它（业务规则）才属于 service。
	IncrCommentCount(ctx context.Context, postID uint64, delta int) error
}

// CommentRepo 描述评论的持久化能力。
type CommentRepo interface {
	ListComments(ctx context.Context, postID uint64, status model.CommentStatus) ([]model.Comment, error)
	ListCommentsByStatus(ctx context.Context, status model.CommentStatus, limit int) ([]model.Comment, error)
	GetCommentByID(ctx context.Context, id uint64) (*model.Comment, error)
	CreateComment(ctx context.Context, c *model.Comment) error
	UpdateCommentStatus(ctx context.Context, id uint64, from, to model.CommentStatus) (bool, error)
}

// TxManager 提供事务边界。
//
// 为什么把它单独抽成一个接口，而不是在 PostRepo 上加一个 BeginTx：
//
// 「事务的边界」属于业务决策 —— 只有 service 知道「这两步必须一起成功」。
// repository 提供的是「事务能力」这个基础设施，不该替业务决定用在哪。
//
// 实现见 gorm_tx.go。关键在于 fn 接收的是**带事务的 ctx**，
// 而不是一个 tx 句柄 —— 于是 service 不需要知道 GORM 的存在。
type TxManager interface {
	// WithTx 在事务中执行 fn。
	//
	// fn 返回 error 时回滚，返回 nil 时提交。
	// 传入的 ctx 已经携带事务，repository 的方法会自动使用它。
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}
