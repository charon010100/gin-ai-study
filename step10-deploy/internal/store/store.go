// Package store 负责建立并持有外部依赖连接（MySQL / Redis）。
//
// 单独成包的理由：连接池是有生命周期的资源，需要「打开」和「关闭」配对，
// 而 HTTP 层不应该知道连接是怎么建的。把这件事收在一个包里，
// 关闭逻辑就只有一处，也就不会漏。
package store

import (
	"context"
	"fmt"
	"time"

	"ginlesson/step10/internal/config"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store 持有所有外部依赖。
type Store struct {
	DB    *gorm.DB
	Redis *redis.Client
}

// Open 建立全部连接。
//
// 这里的策略是「启动时连不上就直接失败退出」，而不是「先起来、慢慢重试」。
// 理由：一个连不上数据库的实例，对用户而言和没启动没有区别，
// 但它会通过健康检查之外的所有请求并返回 500，让监控看起来「服务在线、错误率高」。
// 启动失败则容器直接重启，问题一目了然，也让编排系统按依赖顺序拉起（见 compose 的
// depends_on + condition）。
func Open(ctx context.Context, cfg *config.Config) (*Store, error) {
	db, err := openMySQL(ctx, cfg.MySQL)
	if err != nil {
		return nil, err
	}

	rdb, err := openRedis(ctx, cfg.Redis)
	if err != nil {
		// 数据库已连上，这里必须主动关掉，否则连接池泄漏到进程退出。
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, err
	}

	return &Store{DB: db, Redis: rdb}, nil
}

func openMySQL(ctx context.Context, cfg config.MySQL) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
		// 生产环境只记录慢查询和错误。
		// GORM 默认的 logger.Info 会打印每条 SQL 和参数，等于把用户数据
		// （密码哈希、邮箱、评论正文）写进日志文件，而日志往往权限更宽、留存更久。
		Logger: logger.Default.LogMode(logger.Warn),
		// 关闭「每个写操作自动包事务」：单条 INSERT/UPDATE 自身即原子，
		// 额外的事务意味着多两次网络往返。
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}

	// 连接池参数必须显式设置。默认 MaxOpenConns=0 表示「无上限」，
	// 流量突增时会打开远超数据库 max_connections 的连接数，
	// 结果是所有实例一起被拒绝连接。
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
	// 连接存活时间要短于 MySQL 的 wait_timeout（默认 8 小时），
	// 否则会拿到已被数据库单方面关闭的死连接，表现为随机出现的
	// "invalid connection" 错误。
	sqlDB.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping MySQL 失败: %w", err)
	}
	return db, nil
}

func openRedis(ctx context.Context, cfg config.Redis) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		// 一定要设超时。不设的话，Redis 网络卡住时请求会一直挂着，
		// 把 HTTP 的 goroutine 和连接一起耗尽 —— 一个慢依赖能拖垮整个服务。
		DialTimeout:  3 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     20,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}
	return rdb, nil
}

// Close 按打开的反序释放资源。
func (s *Store) Close() error {
	var firstErr error

	if s.Redis != nil {
		if err := s.Redis.Close(); err != nil {
			firstErr = fmt.Errorf("关闭 Redis 失败: %w", err)
		}
	}
	if s.DB != nil {
		if sqlDB, err := s.DB.DB(); err == nil {
			if err := sqlDB.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("关闭 MySQL 失败: %w", err)
			}
		}
	}
	return firstErr
}

// PingMySQL 供就绪探针使用。
//
// 用独立的 context 是刻意的：就绪探针不能因为数据库慢而自己也变慢，
// 否则探针超时会被误判成「服务不健康」，而实际上只是数据库在抖动。
func (s *Store) PingMySQL(ctx context.Context) error {
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// PingRedis 供就绪探针使用。
func (s *Store) PingRedis(ctx context.Context) error {
	return s.Redis.Ping(ctx).Err()
}
