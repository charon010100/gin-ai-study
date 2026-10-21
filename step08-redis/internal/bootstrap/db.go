// Package bootstrap 负责把外部依赖准备好：数据库连接、迁移、示例数据。
//
// 与 Step 7 的差别：这里多了一个 OpenRedis —— 但注意它**不会**因为
// Redis 连不上就让启动失败（理由见 cache.Connect 的注释）。
package bootstrap

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ginlesson/step08/internal/model"
)

// Database 是数据库连接的访问参数。
type Database struct {
	DSN      string
	LogLevel logger.LogLevel
}

// DefaultDatabase 返回本地开发默认值。
//
// 用文件库而不是 :memory:：内存库在连接池多连接时会各自看到独立的数据库，
// AutoMigrate 建的表在另一条连接上不存在，表现为随机「表不存在」。
func DefaultDatabase() Database {
	return Database{
		DSN:      filepath.Join("data", "blog.db"),
		LogLevel: logger.Warn,
	}
}

// OpenDatabase 连接数据库并执行迁移。
func OpenDatabase(ctx context.Context, cfg Database) (*gorm.DB, error) {
	if dir := filepath.Dir(cfg.DSN); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录 %s 失败: %w", dir, err)
		}
	}

	db, err := gorm.Open(sqlite.Open(cfg.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(cfg.LogLevel),
		// 单条 INSERT/UPDATE 本身原子，额外包一层事务只是多两次往返。
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}
	// SQLite 写锁是整库级的，连接开太多只会互相等待。
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping 数据库失败: %w", err)
	}

	// AutoMigrate 只在开发环境用。生产必须换成版本化 SQL 迁移，
	// 理由见 Step 7 的 NOTES（它会静默保留旧列、静默不删列）。
	if err := db.WithContext(ctx).AutoMigrate(&model.Post{}, &model.Comment{}); err != nil {
		return nil, fmt.Errorf("迁移表结构失败: %w", err)
	}
	return db, nil
}

// Seed 灌入示例数据，已存在则跳过。
func Seed(ctx context.Context, db *gorm.DB) error {
	var n int64
	if err := db.WithContext(ctx).Model(&model.Post{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	now := time.Now().UTC()
	posts := []model.Post{
		{
			Title: "为什么不能在 handler 里直接 db.Find", Slug: "why-not-db-in-handler",
			Summary: "看着方便，代价是测试、事务、复用一起坏掉。",
			Content: "……", Status: "published", CreatedAt: now.Add(-72 * time.Hour),
		},
		{
			Title: "GORM 软删除踩坑：*time.Time 与 gorm.DeletedAt", Slug: "gorm-soft-delete-pitfall",
			Summary: "类型写错不会报错，但查询不再过滤已删除数据。",
			Content: "……", Status: "published", CreatedAt: now.Add(-48 * time.Hour),
		},
		{
			Title: "草稿不该出现在列表里", Slug: "draft-post-example",
			Summary: "这篇是 draft，用来观察状态过滤。",
			Content: "……", Status: "draft", CreatedAt: now.Add(-24 * time.Hour),
		},
	}
	if err := db.WithContext(ctx).Create(&posts).Error; err != nil {
		return err
	}

	comments := []model.Comment{
		{PostID: posts[0].ID, Nickname: "读者A", Content: "已通过审核，看得到。", Status: model.CommentApproved},
		{PostID: posts[0].ID, Nickname: "读者B", Content: "待审核，前台看不到。", Status: model.CommentPending},
		{PostID: posts[0].ID, Nickname: "垃圾广告", Content: "点我领福利", Status: model.CommentSpam},
	}
	if err := db.WithContext(ctx).Create(&comments).Error; err != nil {
		return err
	}

	log.Printf("已灌入 %d 篇示例文章、%d 条示例评论", len(posts), len(comments))
	return nil
}
