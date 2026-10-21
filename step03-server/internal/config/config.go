// Package config 负责读取、校验并暴露服务配置。
//
// 这一层只有两个职责，多一件都是越界：
//  1. 把「外部输入」（配置文件 + 环境变量）变成「强类型的值」
//  2. 尽早发现非法配置并**返回错误**，而不是带着错误值继续运行
//
// 它不负责创建服务、不负责打日志、不负责 os.Exit。
// 把退出权留给 main，这样测试里可以断言「给定错误配置，Load 确实返回了错误」，
// 而在库代码里直接 log.Fatal 的话，测试进程会被直接杀掉，什么也断言不了。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 是配置树的根。
//
// 字段用嵌套的 Server 结构而不是全部拍平，是为了配合 TOML 的 [server] 分组，
// 也是为了将来加 [database]、[redis] 时不会把根结构撑成几十个字段。
type Config struct {
	Server ServerConfig `toml:"server"`
}

// ServerConfig 是 HTTP 服务端参数。
type ServerConfig struct {
	Addr              string `toml:"addr"`
	Mode              string `toml:"mode"`
	ReadTimeout       int    `toml:"readTimeout"`
	ReadTimeoutHeader int    `toml:"readTimeoutHeader"`
	WriteTimeout      int    `toml:"writeTimeout"`
	IdleTimeout       int    `toml:"idleTimeout"`
	ShutdownTimeout   int    `toml:"shutdownTimeout"`
}

// 下面这几个方法把「秒」转成 time.Duration。
//
// 为什么不直接在 struct 里存 time.Duration？
// 因为 TOML 里写 `readTimeout = 15` 是最自然的，但 TOML 的整数不能直接解成
// time.Duration（那是纳秒）。如果强行写成 `readTimeout = "15s"` 再解析，
// 配置文件会更啰嗦。折中方案：存储用秒，边界处转换一次。
func (s ServerConfig) ReadTimeoutDur() time.Duration {
	return time.Duration(s.ReadTimeout) * time.Second
}

func (s ServerConfig) ReadHeaderTimeoutDur() time.Duration {
	return time.Duration(s.ReadTimeoutHeader) * time.Second
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

// IsRelease 判断是否生产模式。
func (s ServerConfig) IsRelease() bool { return s.Mode == "release" }

// Load 按「默认值 → 配置文件 → 环境变量 → 校验」的顺序构建配置。
//
// 这个顺序不能乱：
//   - 默认值先铺，保证配置文件里省略某项时不会得到 0 值（0 值往往有害，
//     例如 ReadTimeout=0 在 http.Server 里表示**永不超时**）
//   - 环境变量最后覆盖，这样容器部署时不需要改镜像里的文件
func Load(path string) (*Config, error) {
	cfg := &Config{Server: ServerConfig{
		Addr:              ":8080",
		Mode:              "debug",
		ReadTimeout:       15,
		ReadTimeoutHeader: 5,
		WriteTimeout:      30,
		IdleTimeout:       60,
		ShutdownTimeout:   15,
	}}

	// 允许配置文件不存在：容器化部署时配置常常全部走环境变量，
	// 这时强制要求文件存在反而是负担。
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	cfg.applyEnv()

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv 用环境变量覆盖配置。
//
// 命名约定用 GINBLOG_ 前缀：环境变量是全局命名空间，
// 不加前缀迟早会和别的程序（比如 PORT）撞名，而撞名的后果很隐蔽。
func (c *Config) applyEnv() {
	if v := os.Getenv("GINBLOG_ADDR"); v != "" {
		c.Server.Addr = v
	}
	// GIN_MODE 是 gin 自己识别的环境变量，这里跟随它的约定，
	// 避免出现「GIN_MODE=release 但我们的配置还是 debug」这种自相矛盾的状态。
	if v := os.Getenv("GIN_MODE"); v != "" {
		c.Server.Mode = v
	}
	if v := os.Getenv("GINBLOG_SHUTDOWN_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.ShutdownTimeout = n
		}
	}
}

// validate 校验必填项与取值范围。
//
// 这里故意不检查「端口是否被占用」——那是启动时才知道的运行时问题，
// 而配置校验只负责静态正确性。混在一起会让错误信息含糊。
func (c *Config) validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr 不能为空")
	}
	if c.Server.Mode != "debug" && c.Server.Mode != "release" && c.Server.Mode != "test" {
		return fmt.Errorf("server.mode 只能是 debug/release/test，当前为 %q", c.Server.Mode)
	}
	// 四个超时必须是正数。写 0 在 http.Server 里的含义是「不限时」，
	// 这几乎总是不小心写错的，所以在这里直接拦住。
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 ||
		c.Server.IdleTimeout <= 0 || c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("所有超时配置必须为正数（0 表示永不超时，几乎总是错的）")
	}
	return nil
}
