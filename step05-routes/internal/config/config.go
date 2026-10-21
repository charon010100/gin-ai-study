// Package config 读取服务配置（结构与 Step 3/4 一致）。
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 是配置根。
type Config struct {
	Server ServerConfig `toml:"server"`
}

// ServerConfig 是 HTTP 服务参数。
type ServerConfig struct {
	Addr            string `toml:"addr"`
	Mode            string `toml:"mode"`
	ReadTimeout     int    `toml:"readTimeout"`
	WriteTimeout    int    `toml:"writeTimeout"`
	IdleTimeout     int    `toml:"idleTimeout"`
	ShutdownTimeout int    `toml:"shutdownTimeout"`
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

// Load 加载配置。
func Load(path string) (*Config, error) {
	cfg := &Config{Server: ServerConfig{
		Addr: ":8080", Mode: "debug",
		ReadTimeout: 15, WriteTimeout: 30, IdleTimeout: 60, ShutdownTimeout: 15,
	}}

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

	if cfg.Server.Addr == "" {
		return nil, fmt.Errorf("server.addr 不能为空")
	}
	return cfg, nil
}
