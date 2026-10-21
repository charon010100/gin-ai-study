// Package bootstrap 负责数据库的准备：连接、迁移、种子数据。
//
// 放进独立包而不是写在 main 里，因为 main 无法被 import，
// 写在那里的逻辑天然无法被测试或脚本复用。
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

	"ginlesson/step09/internal/model"
)

// OpenDatabase 连接 SQLite 并执行迁移。
//
// 用纯 Go 驱动的 SQLite 是为了零依赖启动（不需要装 MySQL、不需要 CGO）。
// 换 MySQL 只需把 sqlite.Open 换成 mysql.Open，并改 DSN ——
// 详见 Step 7 的 NOTES。
func OpenDatabase(ctx context.Context, dsn string) (*gorm.DB, error) {
	if dir := filepath.Dir(dsn); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
		// 单条写操作本身原子，额外事务只是多两次往返。
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// SQLite 写锁是整库级的，连接开太多只会互相等待。
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping 失败: %w", err)
	}

	// AutoMigrate 只用于开发。生产必须用版本化 SQL 迁移，
	// 因为 AutoMigrate 会静默保留旧列、静默不删列（见 Step 7 NOTES）。
	if err := db.WithContext(ctx).AutoMigrate(&model.Post{}, &model.Comment{}); err != nil {
		return nil, fmt.Errorf("迁移失败: %w", err)
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
			Title: "为什么 handler 里不该出现 *gorm.DB", Slug: "no-gorm-in-handler",
			Summary: "一处违反分层的代码，会让事务和测试同时失效。",
			Content: "……", Status: model.PostPublished,
			CreatedAt: now.Add(-72 * time.Hour),
		},
		{
			Title: "事务边界应该放在哪一层", Slug: "transaction-boundary",
			Summary: "service 层，而且只有 service 层。",
			Content: "……", Status: model.PostPublished,
			CreatedAt: now.Add(-48 * time.Hour),
		},
		{
			Title: "这篇是草稿", Slug: "draft-example",
			Summary: "观察它不会出现在前台列表里。",
			Content: "……", Status: model.PostDraft,
			CreatedAt: now.Add(-24 * time.Hour),
		},
	}
	if err := db.WithContext(ctx).Create(&posts).Error; err != nil {
		return err
	}

	comments := []model.Comment{
		{PostID: posts[0].ID, Nickname: "读者A", Content: "已通过审核，前台可见。", Status: model.CommentApproved},
		{PostID: posts[0].ID, Nickname: "读者B", Content: "待审核，前台不可见。", Status: model.CommentPending},
	}
	if err := db.WithContext(ctx).Create(&comments).Error; err != nil {
		return err
	}

	// 种子数据的评论数要手动对齐（真实项目里由 Approve 事务维护）。
	if err := db.WithContext(ctx).Model(&model.Post{}).
		Where("id = ?", posts[0].ID).
		Update("comment_count", 1).Error; err != nil {
		return err
	}

	log.Printf("已灌入 %d 篇示例文章、%d 条示例评论", len(posts), len(comments))
	return nil
}
