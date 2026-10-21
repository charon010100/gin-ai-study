// Package config 负责把「运行环境」翻译成一张配置表。
//
// 这一层在部署阶段变得关键，因为它决定了「改配置」和「改代码」的边界：
// 凡是会随环境变化的东西（数据库地址、密码、日志级别、监听端口）都必须在配置里，
// 否则改一次就要重新构建镜像 —— 而镜像一旦发出去了，你就改不动了。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是运行所需的全部配置。
type Config struct {
	App   App   `yaml:"app"`
	MySQL MySQL `yaml:"mysql"`
	Redis Redis `yaml:"redis"`
	Log   Log   `yaml:"log"`
}

// App 是进程级配置。
type App struct {
	// Env 决定要不要切到 gin 的 ReleaseMode，也决定日志格式。
	Env string `yaml:"env"`
	// Addr 形如 ":8080"。故意用完整地址而不是单独的端口号：
	// 将来想只监听回环地址（"127.0.0.1:8080"，配合 Nginx 同机部署）时不用改代码。
	Addr string `yaml:"addr"`
	// ShutdownTimeout 是优雅关停的等待上限。
	//
	// 这个值必须小于容器编排的 stop 超时（Docker 默认 10 秒，K8s 默认 30 秒），
	// 否则编排系统会先一步 SIGKILL，你精心写的优雅关停等于没执行。
	ShutdownTimeout time.Duration `yaml:"-"`
	ShutdownSeconds int           `yaml:"shutdown_timeout"`
}

// MySQL 是数据库连接参数。
type MySQL struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
}

// DSN 拼装 go-sql-driver/mysql 的连接串。
//
// parseTime=True 让驱动把 DATETIME 解析成 time.Time 而不是 []byte，
// 否则 GORM 扫描时间字段会失败；loc=UTC 保证「库里统一存 UTC」这个约定不被破坏。
func (m MySQL) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=UTC&timeout=5s",
		m.User, m.Password, m.Host, m.Port, m.Database)
}

// Redis 是缓存连接参数。
type Redis struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// Log 是日志参数。
type Log struct {
	Level string `yaml:"level"`
}

// defaults 返回一份可以直接跑起来的配置。
//
// 有默认值不是为了让「不配置也能跑」，而是为了让「少配一项」不至于得到零值：
// 零值的 ShutdownTimeout 会让优雅关停立刻超时，这类 bug 只在停服时出现，
// 平时根本测不到。
func defaults() *Config {
	return &Config{
		App: App{
			Env:             "dev",
			Addr:            ":8080",
			ShutdownSeconds: 15,
		},
		MySQL: MySQL{
			Host:     "127.0.0.1",
			Port:     3306,
			User:     "root",
			Database: "blog",
		},
		Redis: Redis{
			Addr: "127.0.0.1:6379",
			DB:   0,
		},
		Log: Log{Level: "info"},
	}
}

// Load 按「默认值 → 配置文件 → 环境变量」的顺序叠加配置，最后校验。
//
// 优先级设计成「环境变量最高」是有意为之：容器里改一个环境变量就是改配置，
// 而挂载配置文件需要重新分发文件。云平台的 secret 注入也一律走环境变量或文件，
// 让环境变量赢，才能让「平台注入的密码」覆盖掉镜像里可能残留的旧值。
func Load() (*Config, error) {
	cfg := defaults()

	// 配置文件是可选的。镜像里不放配置文件（见 NOTES 的核心决策 3），
	// 需要文件时由部署方挂载，并通过 CONFIG_FILE 指路。
	if path := os.Getenv("CONFIG_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
		}
		// 注意：yaml.Unmarshal 只覆盖文件中出现的字段，未出现的保留默认值。
		// 这正是我们想要的「叠加」语义，而不是「文件里没写就清零」。
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
		}
	}

	applyEnv(cfg)
	cfg.App.ShutdownTimeout = time.Duration(cfg.App.ShutdownSeconds) * time.Second

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyEnv 用环境变量覆盖配置。
//
// 每个环境变量都用显式的 if 判断而不是反射，看起来很啰嗦，但换来两件事：
// ① 想知道「支持哪些环境变量」时，看这一个函数就够了；
// ② 新增配置项时不会因为忘了加反射映射而静默失效。
func applyEnv(cfg *Config) {
	if v := os.Getenv("APP_ENV"); v != "" {
		cfg.App.Env = strings.ToLower(v)
	}
	if v := os.Getenv("APP_ADDR"); v != "" {
		cfg.App.Addr = v
	}
	if v := os.Getenv("SHUTDOWN_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.App.ShutdownSeconds = n
		}
	}
	if v := os.Getenv("MYSQL_HOST"); v != "" {
		cfg.MySQL.Host = v
	}
	if v := os.Getenv("MYSQL_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MySQL.Port = n
		}
	}
	if v := os.Getenv("MYSQL_USER"); v != "" {
		cfg.MySQL.User = v
	}
	if v := os.Getenv("MYSQL_PASSWORD"); v != "" {
		cfg.MySQL.Password = v
	}
	if v := os.Getenv("MYSQL_DATABASE"); v != "" {
		cfg.MySQL.Database = v
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Redis.DB = n
		}
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = strings.ToLower(v)
	}
}

// validate 在启动阶段就把配置问题暴露出来。
//
// 为什么不在第一次用到数据库时才报错：那时进程已经对外声明「我起来了」，
// 负载均衡可能已经转发流量进来，用户看到的是 500 而不是「服务没起来」。
// 启动时失败，容器会退出并重启，问题在监控上立刻可见。
func (c *Config) validate() error {
	var problems []string

	switch c.App.Env {
	case "dev", "test", "prod":
		// 合法
	default:
		problems = append(problems, fmt.Sprintf("APP_ENV 只能是 dev/test/prod，当前为 %q", c.App.Env))
	}
	if c.App.Addr == "" {
		problems = append(problems, "APP_ADDR 不能为空")
	}
	if c.App.ShutdownSeconds <= 0 {
		problems = append(problems, "SHUTDOWN_TIMEOUT 必须为正数（秒）")
	}
	if c.MySQL.Host == "" || c.MySQL.User == "" || c.MySQL.Database == "" {
		problems = append(problems, "MYSQL_HOST / MYSQL_USER / MYSQL_DATABASE 不能为空")
	}
	if c.Redis.Addr == "" {
		problems = append(problems, "REDIS_ADDR 不能为空")
	}

	// 生产环境的红线：不允许空密码。
	// 这类检查看着「多此一举」，但空密码往往来自环境变量没注入成功，
	// 而服务带着空密码照样能连上（本地 MySQL 默认可空密码），
	// 于是问题被推迟到线上被扫描到时才暴露。
	if c.IsProd() {
		if c.MySQL.Password == "" {
			problems = append(problems, "生产环境必须设置 MYSQL_PASSWORD")
		}
		if c.Redis.Password == "" {
			problems = append(problems, "生产环境必须设置 REDIS_PASSWORD")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置校验未通过:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// IsProd 判断是否生产环境。
func (c *Config) IsProd() bool { return c.App.Env == "prod" }
