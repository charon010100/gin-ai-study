// Package model 定义数据库模型，以及数据层向业务层暴露的错误。
//
// 本包不 import 项目内的任何其他包。
// 这样 repository、handler 都能引用它而不产生依赖环。
package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// CommentStatus 评论状态。
//
// 用自定义字符串类型而不是 int：
//   - 数据库里直接看得懂（'pending' 优于 1），排查时省一次脑内翻译
//   - 前端拿到的值可直接判断，不需要维护映射表
//   - 编译期能挡住「把 PostStatus 赋给 CommentStatus」这类手误
type CommentStatus string

const (
	CommentPending  CommentStatus = "pending"
	CommentApproved CommentStatus = "approved"
	CommentRejected CommentStatus = "rejected"
	CommentSpam     CommentStatus = "spam"
	CommentHidden   CommentStatus = "hidden"
)

// Valid 供 handler 校验客户端传入的状态串。
//
// 如果不校验就写进数据库，状态字段会变成自由文本，
// 之后所有按状态过滤的查询都会漏数据，而且没有任何报错。
func (s CommentStatus) Valid() bool {
	switch s {
	case CommentPending, CommentApproved, CommentRejected, CommentSpam, CommentHidden:
		return true
	}
	return false
}

// ---------- 数据层错误 ----------

// ErrNotFound 表示目标资源不存在。
var ErrNotFound = errors.New("资源不存在")

// ErrConflict 表示违反唯一性约束（如重复的 slug）。
var ErrConflict = errors.New("资源冲突")

// 为什么在这里定义错误而不是直接返回 gorm.ErrRecordNotFound：
// 一旦 gorm 的错误类型漏到 handler，handler 就必须 import gorm 才能判断它，
// 于是 HTTP 层被绑死在 ORM 选型上。换 ORM 时所有 handler 都要改。
// 在数据层边界把错误翻译成语义错误，是成本最低的解耦方式。

// ---------- 模型 ----------

// Post 是一篇文章。
type Post struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"not null;index:idx_posts_status_created,priority:2" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	// DeletedAt 必须用 gorm.DeletedAt 这个具体类型。
	//
	// 这是 GORM 软删除的开关：只有识别到该类型，它才自动
	//   1. 查询时追加 deleted_at IS NULL
	//   2. Delete() 时改写为 UPDATE ... SET deleted_at = now()
	//
	// 换成 *time.Time（看起来完全等价）后，查询不再过滤已删除数据，
	// Delete() 变成真正的 DELETE。代码上看不出异常，只有跑起来才发现。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Title   string `gorm:"type:varchar(200);not null" json:"title"`
	Slug    string `gorm:"type:varchar(200);not null;uniqueIndex" json:"slug"`
	Summary string `gorm:"type:varchar(500);not null;default:''" json:"summary"`
	Content string `gorm:"type:text;not null;default:''" json:"content,omitempty"`

	Status string `gorm:"type:varchar(16);not null;default:'published'" json:"status"`

	// Views 是 DB 里的快照。
	//
	// 真实值 = 这里的值 + Redis 里尚未回写的增量。
	// 所以详情接口返回的 views 会比数据库里的大 —— 这是设计，不是 bug。
	Views int64 `gorm:"not null;default:0" json:"views"`

	// 复合索引 (status, created_at) 支撑列表页核心查询：
	//   WHERE status = 'published' AND deleted_at IS NULL
	//   ORDER BY created_at DESC LIMIT ? OFFSET ?
	// 等值列 status 在前（收敛结果集），排序列 created_at 在后（免 filesort）。

	Comments []Comment `gorm:"foreignKey:PostID" json:"comments,omitempty"`
}

// TableName 显式指定表名，避免 GORM 复数化成 posts 之外的东西，
// 同时与 NOTES 里的索引名对齐。
func (Post) TableName() string { return "posts" }

// Comment 是一条评论。
type Comment struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"not null;index:idx_comments_post_status_created,priority:3" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// 复合索引 (post_id, status, created_at) 支撑
	// 「取某篇文章已通过的评论并按时间排序」这条最热查询。
	PostID uint64 `gorm:"not null;index:idx_comments_post_status_created,priority:1" json:"post_id"`

	Nickname string `gorm:"type:varchar(64);not null" json:"nickname"`
	Content  string `gorm:"type:text;not null" json:"content"`

	// Status 默认 pending —— 整个评论功能的红线。
	//
	// 默认值写在 tag 里，但**不要依赖它**：真正的保障是 handler
	// 在创建时显式赋值 pending。默认值只是「即使有人漏了，落库也是安全值」的兜底。
	Status CommentStatus `gorm:"type:varchar(16);not null;default:'pending';index:idx_comments_post_status_created,priority:2" json:"status"`
}

// TableName 显式指定表名。
func (Comment) TableName() string { return "comments" }
