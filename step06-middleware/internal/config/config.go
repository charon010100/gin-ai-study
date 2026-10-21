// Package config 读取服务配置。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 是配置根。
type Config struct {
	Server ServerConfig `toml:"server"`
	CORS   CORSConfig   `toml:"cors"`
}

// ServerConfig 是 HTTP 服务参数。
type ServerConfig struct {
	Addr            string `toml:"addr"`
	Mode            string `toml:"mode"`
	ReadTimeout     int    `toml:"readTimeout"`
	WriteTimeout    int    `toml:"writeTimeout"`
	IdleTimeout     int    `toml:"idleTimeout"`
	ShutdownTimeout int    `toml:"shutdownTimeout"`

	// TrustProxy 表示「本服务是否位于可信反向代理之后」。
	//
	// 这个配置项直接决定安全：为 true 时信任 X-Forwarded-For 头，
	// 也就是相信代理会覆盖它。如果服务直接暴露公网却设成 true，
	// 客户端就能伪造 IP，绕过限流与黑名单。
	TrustProxy bool `toml:"trustProxy"`
}

func (s ServerConfig) ReadTimeoutDur() time.Duration {
	return time.Duration(s.ReadTimeout) * time.Second
}
func (s ServerConfig) WriteTimeoutDur() time.Duration {
	return time.Duration(s.WriteTimeout) * time.Second
}
func (s ServerConfig) IdleTimeoutDur() time.Duration {
	return time.Duration(s.IdleTimeout) * time.Second
}
func (s ServerConfig) ShutdownTimeoutDur() time.Duration {
	return time.Duration(s.ShutdownTimeout) * time.Second
}
func (s ServerConfig) IsRelease() bool { return s.Mode == "release" }

// CORSConfig 是跨域配置。
type CORSConfig struct {
	// AllowOrigins 是允许的来源白名单。
	// 先用 "*" 可以快速开发，但生产必须写具体域名。
	AllowOrigins []string `toml:"allowOrigins"`
}

// Load 加载配置。
func Load(path string) (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Addr: ":8080", Mode: "debug",
			ReadTimeout: 15, WriteTimeout: 30, IdleTimeout: 60, ShutdownTimeout: 15,
			TrustProxy: false,
		},
		CORS: CORSConfig{AllowOrigins: []string{"*"}},
	}

	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	if v := os.Getenv("GINBLOG_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("GIN_MODE"); v != "" {
		cfg.Server.Mode = v
	}
	if v := os.Getenv("GINBLOG_TRUST_PROXY"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Server.TrustProxy = true
	}
	// 环境变量里写多个来源用逗号分隔，例如：
	//   GINBLOG_CORS_ORIGINS="https://blog.example.com,https://www.example.com"
	if v := os.Getenv("GINBLOG_CORS_ORIGINS"); v != "" {
		parts := strings.Split(v, ",")
		origins := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				origins = append(origins, p)
			}
		}
		cfg.CORS.AllowOrigins = origins
	}

	if cfg.Server.Addr == "" {
		return nil, fmt.Errorf("server.addr 不能为空")
	}
	return cfg, nil
}
