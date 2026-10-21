// Package model 定义数据库模型。
//
// 本包不 import 项目内任何其他包，因此位于依赖图的最底层。
// 「能 import 谁、不能 import 谁」的清单见 NOTES。
package model

import (
	"time"

	"gorm.io/gorm"
)

// PostStatus 文章状态。
//
// 用自定义类型而不是裸 string，是为了在函数签名上表达约束：
// 接收 PostStatus 的函数不会收到任意字符串，编译期就挡住了把评论状态传进来的手误。
type PostStatus string

const (
	PostPublished PostStatus = "published"
	PostDraft     PostStatus = "draft"
)

// Valid 判断状态是否合法，用于校验客户端输入。
func (s PostStatus) Valid() bool {
	return s == PostPublished || s == PostDraft
}

// CommentStatus 评论状态。默认 pending，先审后发。
type CommentStatus string

const (
	CommentPending  CommentStatus = "pending"
	CommentApproved CommentStatus = "approved"
	CommentRejected CommentStatus = "rejected"
	CommentSpam     CommentStatus = "spam"
	CommentHidden   CommentStatus = "hidden"
)

// Valid 判断状态是否合法。
func (s CommentStatus) Valid() bool {
	switch s {
	case CommentPending, CommentApproved, CommentRejected, CommentSpam, CommentHidden:
		return true
	}
	return false
}

// Post 是一篇文章。
type Post struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"not null;index:idx_posts_status_created,priority:2" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	// 软删除：必须是 gorm.DeletedAt 这个具体类型。
	// 换成 *time.Time 后查询不再过滤已删除数据，且没有任何报错。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Title   string `gorm:"type:varchar(200);not null" json:"title"`
	Slug    string `gorm:"type:varchar(200);not null;uniqueIndex" json:"slug"`
	Summary string `gorm:"type:varchar(500);not null;default:''" json:"summary"`
	Content string `gorm:"type:text;not null;default:''" json:"content,omitempty"`

	Status PostStatus `gorm:"type:varchar(16);not null;default:'published'" json:"status"`

	// Views 是 DB 快照，真实值还要加上 Redis 里未回写的增量。
	Views int64 `gorm:"not null;default:0" json:"views"`

	// 冗余的评论计数，由 service 在事务里维护。
	// 「跨表计数」是典型的业务规则，不该由 repository 自作主张。
	CommentCount int64 `gorm:"not null;default:0" json:"comment_count"`
}

// TableName 显式指定表名，与 NOTES 中的索引名保持一致。
func (Post) TableName() string { return "posts" }

// Comment 是一条评论。
type Comment struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `gorm:"not null;index:idx_comments_post_status_created,priority:3" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	PostID uint64 `gorm:"not null;index:idx_comments_post_status_created,priority:1" json:"post_id"`

	Nickname string `gorm:"type:varchar(64);not null" json:"nickname"`
	Content  string `gorm:"type:text;not null" json:"content"`

	// 默认 pending。但不要依赖这个默认值 ——
	// 真正的保障是 service 显式赋值，默认值只是兜底。
	Status CommentStatus `gorm:"type:varchar(16);not null;default:'pending';index:idx_comments_post_status_created,priority:2" json:"status"`
}

// TableName 显式指定表名。
func (Comment) TableName() string { return "comments" }
