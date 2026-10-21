package repository

import (
	"context"
	"errors"
	"strings"

	"ginlesson/step09/internal/model"
	"gorm.io/gorm"
)

// gormStore 同时实现 PostRepo、CommentRepo 和 TxManager。
//
// 放在一个类型里而不是三个，是因为它们共享同一个 *gorm.DB。
// 拆成三个类型只是多几次构造调用，没有实际收益。
type gormStore struct {
	db *gorm.DB
}

// New 构造仓储。
//
// 返回值是具体类型（不是接口）：调用方需要把它同时赋给
// PostRepo、CommentRepo、TxManager 三个接口，
// 返回接口的话还得再断言一次，没有意义。
func New(db *gorm.DB) *gormStore {
	return &gormStore{db: db}
}

// 编译期断言：一个类型必须同时满足三个接口。
//
// 三行写在一起的好处：任何一行编译失败，你立刻知道是哪个接口没过，
// 而不是等到某个 service 构造时报错。
var (
	_ PostRepo    = (*gormStore)(nil)
	_ CommentRepo = (*gormStore)(nil)
	_ TxManager   = (*gormStore)(nil)
)

// conn 取出当前应使用的连接。
//
// 这是本步实现事务的核心技巧：事务句柄通过 context 传递，
// 而不是通过函数参数。好处是 repository 的每个方法签名都不用变，
// 也不需要「有事务版」和「无事务版」两套方法。
//
// 代价是：事务是隐式的，读代码时不容易看出某个调用在不在事务里。
// 这也是为什么 service 层必须把 WithTx 的边界写得很显眼。
func (s *gormStore) conn(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx.WithContext(ctx)
	}
	return s.db.WithContext(ctx)
}

// txKey 是 context 中存放事务的键。
type txKey struct{}

// WithTx 在事务中执行 fn。
func (s *gormStore) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 把事务塞进 ctx，后续 repository 调用会自动通过 conn() 取到它。
		txCtx := context.WithValue(ctx, txKey{}, tx)
		return fn(txCtx)
	})
}

// ---------- 文章 ----------

func (s *gormStore) ListPosts(ctx context.Context, q ListQuery) ([]PostWithCount, int64, error) {
	// 用 Table("posts") 而不是 Model(&model.Post{})：
	// 查询目标是 []PostWithCount，它不是数据库模型。
	// 只写 Find(&rows) 的话 GORM 会把结构体名复数化成 post_with_counts —— 查错表。
	base := s.conn(ctx).Table("posts")

	if q.Status != "" {
		base = base.Where("status = ?", q.Status)
	}
	if q.Keyword != "" {
		// 用 ? 占位而不是字符串拼接：拼接会让用户输入变成 SQL 片段。
		base = base.Where("title LIKE ?", "%"+q.Keyword+"%")
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []PostWithCount
	err := base.
		// 标量子查询而不是 JOIN + GROUP BY：
		// JOIN 会把一篇文章的多条评论展开成多行，必须 GROUP BY 收敛，
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

func (s *gormStore) GetPostBySlug(ctx context.Context, slug string) (*model.Post, error) {
	var p model.Post
	err := s.conn(ctx).Where("slug = ?", slug).First(&p).Error
	return &p, translate(err)
}

func (s *gormStore) GetPostByID(ctx context.Context, id uint64) (*model.Post, error) {
	var p model.Post
	err := s.conn(ctx).Where("id = ?", id).First(&p).Error
	return &p, translate(err)
}

func (s *gormStore) CreatePost(ctx context.Context, p *model.Post) error {
	err := s.conn(ctx).Create(p).Error
	if err == nil {
		return nil
	}
	// 把驱动层的唯一约束错误翻译成语义错误。
	// 靠字符串匹配是因为 GORM 没有可移植的唯一约束错误类型：
	//   SQLite -> "UNIQUE constraint failed: posts.slug"
	//   MySQL  -> "Duplicate entry ... for key 'posts.slug'"
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *gormStore) DeletePost(ctx context.Context, slug string) error {
	// 必须看 RowsAffected：删不到行时 GORM 不返回错误（SQL 语义），
	// 不看的话无法区分「删成功」和「本来就没有」。
	res := s.conn(ctx).Where("slug = ?", slug).Delete(&model.Post{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *gormStore) IncrPostViews(ctx context.Context, deltas map[uint64]int64) error {
	if len(deltas) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(ctx context.Context) error {
		for id, delta := range deltas {
			// 用 gorm.Expr 做列自增，而不是「先读出来再写回去」。
			// 先读后写会丢更新：两个请求同时读到 10，各自 +3 写回 13，应该是 16。
			if err := s.conn(ctx).Model(&model.Post{}).
				Where("id = ?", id).
				UpdateColumn("views", gorm.Expr("views + ?", delta)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *gormStore) IncrCommentCount(ctx context.Context, postID uint64, delta int) error {
	if delta == 0 {
		return nil
	}
	return s.conn(ctx).Model(&model.Post{}).
		Where("id = ?", postID).
		UpdateColumn("comment_count", gorm.Expr("comment_count + ?", delta)).Error
}

// ---------- 评论 ----------

func (s *gormStore) ListComments(ctx context.Context, postID uint64, status model.CommentStatus) ([]model.Comment, error) {
	var out []model.Comment
	err := s.conn(ctx).
		Where("post_id = ? AND status = ?", postID, status).
		Order("created_at ASC"). // 评论正序：从旧到新才符合对话直觉
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		// 保证序列化成 [] 而不是 null。
		// 放在 repository 是因为所有调用方都需要这个保证。
		out = make([]model.Comment, 0)
	}
	return out, nil
}

func (s *gormStore) ListCommentsByStatus(ctx context.Context, status model.CommentStatus, limit int) ([]model.Comment, error) {
	var out []model.Comment
	err := s.conn(ctx).
		Where("status = ?", status).
		Order("created_at ASC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = make([]model.Comment, 0)
	}
	return out, nil
}

func (s *gormStore) GetCommentByID(ctx context.Context, id uint64) (*model.Comment, error) {
	var c model.Comment
	err := s.conn(ctx).Where("id = ?", id).First(&c).Error
	return &c, translate(err)
}

func (s *gormStore) CreateComment(ctx context.Context, c *model.Comment) error {
	return s.conn(ctx).Create(c).Error
}

// UpdateCommentStatus 带条件地更新状态。
//
// 返回 (是否更新了行, error)。更新 0 行不是错误 —— 它表示
// 「状态已经不对了」，由调用方决定当作 404 还是忽略。
//
// 用带条件的 UPDATE 而不是「先查后改」：
//   - 天然幂等（重复调用第二次更新 0 行）
//   - 并发安全（不需要额外加锁）
func (s *gormStore) UpdateCommentStatus(ctx context.Context, id uint64, from, to model.CommentStatus) (bool, error) {
	res := s.conn(ctx).Model(&model.Comment{}).
		Where("id = ? AND status = ?", id, from).
		Update("status", to)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ---------- 内部工具 ----------

// translate 把 GORM 的错误翻译成本包的语义错误。
//
// 这是「数据层边界」的职责：GORM 的错误类型不允许漏到上层，
// 否则 service 就必须 import gorm 才能判断「是不是没找到」。
func translate(err error) error {
	if err == nil {
		return nil
	}
	// 必须用 errors.Is：GORM 内部会用 %w 包装错误，
	// 用 == 比较在包装后就失效了。
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate entry")
}
