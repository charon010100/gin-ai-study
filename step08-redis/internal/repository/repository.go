// Package repository 是唯一允许出现 SQL / GORM 调用的地方。
//
// 对外暴露：
//   - 接口 Repo：描述「数据层能做什么」
//   - NewGormRepo：GORM 实现
//   - NewCachedRepo：给上面那个包一层缓存（装饰器，见 cached.go）
//
// 把缓存做成装饰器而不是塞进实现里，是为了让「带缓存」和「不带缓存」
// 都能作为 Repo 使用。测试时直接用 NewGormRepo；生产时套一层 NewCachedRepo。
// 调用方（handler）看到的类型完全一样。
package repository

import (
	"context"
	"errors"
	"strings"

	"ginlesson/step08/internal/model"
	"gorm.io/gorm"
)

// ListQuery 是列表查询的入参。
type ListQuery struct {
	Page     int
	PageSize int
	Keyword  string
	// Status 为空表示不筛状态。
	Status string
}

// PostWithCount 是列表项：文章 + 它的评论数。
//
// 评论数用独立字段而不是塞进 Post：Post 是数据库模型，
// 评论数是聚合结果，不属于这张表。塞进去会让人误以为可以写回。
type PostWithCount struct {
	model.Post
	CommentCount int64 `json:"comment_count"`
}

// Repo 描述数据层能做什么。
//
// 所有方法都接收 context.Context：让调用方的超时/取消能一路传到 SQL。
// 不接收 ctx 的写法在「用户断开连接」时无法中止查询，慢 SQL 会白跑到底。
type Repo interface {
	ListPosts(ctx context.Context, q ListQuery) ([]PostWithCount, int64, error)
	GetPostBySlug(ctx context.Context, slug string) (*model.Post, error)
	CreatePost(ctx context.Context, p *model.Post) error
	// DeletePost 执行软删除。返回 ErrNotFound 表示该文章本来就不在。
	DeletePost(ctx context.Context, slug string) error
	// IncrPostViews 批量累加浏览量，供 Redis 计数器的回写任务调用。
	IncrPostViews(ctx context.Context, deltas map[uint64]int64) error
	// CountPostsIncludingDeleted 用于演示软删除的可见性差异。
	CountPostsIncludingDeleted(ctx context.Context) (int64, error)

	ListComments(ctx context.Context, postID uint64, status model.CommentStatus) ([]model.Comment, error)
	CreateComment(ctx context.Context, c *model.Comment) error
	// ApproveComment 把评论从 pending 改为 approved。
	// 状态流转集中在一处，而不是让调用方传任意 status。
	ApproveComment(ctx context.Context, id uint64) error
}

// gormRepo 是 Repo 的 GORM 实现。
//
// 小写开头：外部拿到构造函数的返回值（接口类型）即可，
// 不需要知道也不该依赖具体实现类型。
type gormRepo struct {
	db *gorm.DB
}

// NewGormRepo 构造一个基于 GORM 的仓储。
//
// 返回值类型是接口而不是 *gormRepo：调用方只能看到接口，
// 于是「不小心依赖了实现细节」在编译期就不可能发生。
func NewGormRepo(db *gorm.DB) Repo {
	return &gormRepo{db: db}
}

// 编译期断言。
var _ Repo = (*gormRepo)(nil)

// ---------- 文章 ----------

func (r *gormRepo) ListPosts(ctx context.Context, q ListQuery) ([]PostWithCount, int64, error) {
	// 用 Table("posts") 而不是 Model(&model.Post{})。
	//
	// 原因：查询目标是 []PostWithCount，而它不是数据库模型。
	// 若只写 Find(&rows)，GORM 会把目标结构体名复数化成 post_with_counts —— 查错表。
	// 显式 Table("posts") 同时解决表名问题，又保留 Post.DeletedAt 的软删除过滤
	// （GORM 会按结构体里的 DeletedAt 字段自动补 deleted_at IS NULL，
	//  表名用 <tablename> 占位，即上面指定的 posts）。
	base := r.db.WithContext(ctx).Table("posts")

	if q.Status != "" {
		base = base.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		// 参数用 ? 占位，不要 fmt.Sprintf 拼接：
		// 拼接会让用户输入变成 SQL 片段（注入），
		// 而且不同数据库的转义规则不同，拼接永远写不对。
		//
		// 前置通配符 '%kw%' 无法用索引，这是 LIKE 的固有限制。
		base = base.Where("title LIKE ?", "%"+q.Keyword+"%")
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []PostWithCount
	err := base.
		// 标量子查询而不是 JOIN + GROUP BY：JOIN 会把一篇文章的
		// 多条评论展开成多行，必须 GROUP BY 收敛，
		// 而 GROUP BY 与 LIMIT/OFFSET 的组合语义很容易写错。
		Select("posts.*, (SELECT COUNT(*) FROM comments c WHERE c.post_id = posts.id AND c.status = ? AND c.deleted_at IS NULL) AS comment_count", model.CommentApproved).
		Order("posts.created_at DESC").
		Limit(q.PageSize).
		Offset((q.Page - 1) * q.PageSize).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *gormRepo) GetPostBySlug(ctx context.Context, slug string) (*model.Post, error) {
	var p model.Post
	err := r.db.WithContext(ctx).Where("slug = ?", slug).First(&p).Error
	if err != nil {
		// 这里就是必须用 errors.Is 的地方。
		//
		// 写成 `if err != nil { return nil, err }` 再在上层用 err != nil 判断，
		// 会把「记录不存在」（正常业务结果）和「数据库连不上」（故障）
		// 当成同一件事，于是 404 与 500 无法区分。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, model.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormRepo) CreatePost(ctx context.Context, p *model.Post) error {
	err := r.db.WithContext(ctx).Create(p).Error
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return model.ErrConflict
	}
	return err
}

func (r *gormRepo) DeletePost(ctx context.Context, slug string) error {
	// Delete 影响 0 行时 GORM 不返回错误（SQL 语义：删不到不算失败）。
	// 所以必须看 RowsAffected，否则无法区分「删成功」和「本来就没有」。
	res := r.db.WithContext(ctx).Where("slug = ?", slug).Delete(&model.Post{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (r *gormRepo) IncrPostViews(ctx context.Context, deltas map[uint64]int64) error {
	if len(deltas) == 0 {
		return nil
	}

	// 一批更新放在同一个事务里。
	//
	// 为什么这里需要事务而单条 UPDATE 不需要：
	// 分批回写是「要么这批都成功，要么这批都失败」的语义。
	// 如果只写成功一半，剩下那一半的增量已经在 Redis 里被 DEL 掉了
	// （FlushOnce 用 Lua 原子取出并清空），于是这部分浏览量永久丢失。
	//
	// 注意：事务不等于「绝对不会丢」。如果整个事务失败，
	// 增量仍然丢了（FlushOnce 里选择了丢弃策略，见那边的注释）。
	// 事务在这里的作用是「避免部分成功造成的不可预知状态」，
	// 而不是保证零丢失。
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for id, delta := range deltas {
			// 用 gorm.Expr 做列自增，而不是先读出来再加。
			//
			// 先读后写会丢更新：两个请求同时读到 views=10，
			// 各自加 3 写回 13，实际应该是 16。
			// views = views + ? 由数据库在行锁内完成，不存在这个问题。
			if err := tx.Model(&model.Post{}).
				Where("id = ?", id).
				UpdateColumn("views", gorm.Expr("views + ?", delta)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *gormRepo) CountPostsIncludingDeleted(ctx context.Context) (int64, error) {
	var n int64
	// Unscoped 是绕过软删除过滤的开关。
	//
	// 它的存在本身就是证据：GORM 默认会给软删除模型自动加过滤条件，
	// 而这个默认行为依赖 DeletedAt 字段类型是 gorm.DeletedAt。
	// 换成 *time.Time 时，这里 Unscoped 前后会得到相同数字。
	err := r.db.WithContext(ctx).Unscoped().Model(&model.Post{}).Count(&n).Error
	return n, err
}

// ---------- 评论 ----------

func (r *gormRepo) ListComments(ctx context.Context, postID uint64, status model.CommentStatus) ([]model.Comment, error) {
	var out []model.Comment
	err := r.db.WithContext(ctx).
		Where("post_id = ? AND status = ?", postID, status).
		// 评论按时间正序：从旧到新才符合对话直觉。
		// 文章列表用倒序，因为最新内容更可能有价值 ——
		// 两个方向相反是刻意的，不是笔误。
		Order("created_at ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = make([]model.Comment, 0)
	}
	return out, nil
}

func (r *gormRepo) CreateComment(ctx context.Context, c *model.Comment) error {
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *gormRepo) ApproveComment(ctx context.Context, id uint64) error {
	// 用带条件的 UPDATE，而不是「先查后改」。
	//
	// 只影响 status = pending 的行，于是：
	//   - 已 approved 的评论不会被重复操作（幂等）
	//   - 并发两次点击「通过」不会互相覆盖
	res := r.db.WithContext(ctx).
		Model(&model.Comment{}).
		Where("id = ? AND status = ?", id, model.CommentPending).
		Update("status", model.CommentApproved)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return model.ErrNotFound
	}
	return nil
}

// ---------- 内部工具 ----------

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || // SQLite
		strings.Contains(msg, "duplicate entry") // MySQL
}
