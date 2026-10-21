// Package config 集中管理配置。
//
// 本步用「环境变量 + 代码默认值」，不引入配置文件与 Viper。
// 理由：教学项目里，多一层配置抽象只会让你多花半小时理解它而不是理解分层。
// 真实项目里配置该独立成包并做校验（Step 3 已经做过）。
//
// 但有一个设计从第一步就该做对：**配置必须是显式传入的，不是全局变量**。
// 全局 config 会让「同一个进程跑两套配置」变得不可能，也让测试无法隔离。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是全部配置的根。
type Config struct {
	// Addr HTTP 监听地址。
	Addr string
	// DSN 数据库连接串。SQLite 是文件路径。
	DSN string
	// Redis 相关。
	RedisAddr string
	RedisDB   int
	RedisPass string
	// SessionLog 每页条数上限，服务端强制。
	MaxPageSize int
	// FlushEvery 浏览量回写间隔。
	FlushEvery time.Duration
}

// Load 从环境变量读取配置，缺省时用安全默认值。
//
// 只对环境变量做「覆盖」，不做「必填校验」—— 因为每个字段都有合理默认值。
// 如果某个字段没有安全默认（比如数据库密码），必须在这里显式报错，
// 否则配置缺失会变成运行时的诡异故障。
func Load() (*Config, error) {
	cfg := &Config{
		Addr:        "localhost:8080",
		DSN:         "data/blog.db",
		RedisAddr:   "localhost:6379",
		RedisDB:     0,
		MaxPageSize: 50,
		FlushEvery:  time.Minute,
	}

	if v := os.Getenv("BLOG_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("BLOG_DSN"); v != "" {
		cfg.DSN = v
	}
	if v := os.Getenv("BLOG_REDIS_ADDR"); v != "" {
		cfg.RedisAddr = v
	}
	if v := os.Getenv("BLOG_REDIS_PASS"); v != "" {
		cfg.RedisPass = v
	}
	if v := os.Getenv("BLOG_REDIS_DB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("BLOG_REDIS_DB 必须是数字，当前为 %q: %w", v, err)
		}
		cfg.RedisDB = n
	}

	if cfg.MaxPageSize <= 0 {
		return nil, fmt.Errorf("MaxPageSize 必须为正数")
	}
	return cfg, nil
}
