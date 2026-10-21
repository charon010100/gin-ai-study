// Command server 是 step10 的可部署服务入口。
//
// 与前面几步的 main 相比，这里多了四件「只为线上而存在」的事：
//  1. 日志走结构化输出（slog + JSON），因为线上的日志是给机器解析的；
//  2. 自己搭 http.Server 而不是 r.Run()，因为要设置超时和优雅关停；
//  3. 监听 SIGTERM —— 容器停止时编排系统发的就是这个信号；
//  4. 支持 -healthcheck 参数，供没有 curl 的极简镜像（distroless）做容器健康检查。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ginlesson/step10/internal/config"
	"ginlesson/step10/internal/handler"
	"ginlesson/step10/internal/router"
	"ginlesson/step10/internal/store"
	"github.com/gin-gonic/gin"
)

// version 在构建时注入：
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/server
//
// 默认值写成 dev，是为了让「忘了注入」这件事在 /readyz 的响应里能被看见，
// 而不是悄悄显示一个看起来正常的版本号。
var version = "dev"

func main() {
	// 用 flag 而不是环境变量控制「本次进程要干什么」：
	// 这是一个一次性的动作（探活后退出），不是持续生效的配置。
	healthcheck := flag.Bool("healthcheck", false, "探测本机 /healthz 后退出，供容器 HEALTHCHECK 使用")
	flag.Parse()

	if err := run(*healthcheck); err != nil {
		// 此时日志可能还没初始化（配置加载失败），所以直接写 stderr。
		// 这也是为什么这里不用 slog：slog 的默认 handler 也是写 stderr，
		// 但显式 fmt 能保证「无论配置是否可用，错误都能被看到」。
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run(healthcheck bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	setupLogger(cfg)

	// 探活模式：容器 HEALTHCHECK 调用 `server -healthcheck`，
	// 它发一次 HTTP 请求到自己的 /healthz，按结果设置退出码。
	//
	// 为什么需要这个：distroless / scratch 镜像里没有 curl、没有 wget、也没有 shell，
	// docker-compose 的 healthcheck 无法用 curl 写。让二进制自己探自己，
	// 是这类极简镜像的标准解法，也顺带避免了「为了健康检查而把 curl 装进镜像」，
	// 那样既增大体积又增加攻击面。
	if healthcheck {
		return probe(cfg.App.Addr)
	}

	logger := slog.Default()
	logger.Info("启动中",
		"version", version,
		"env", cfg.App.Env,
		"addr", cfg.App.Addr,
	)

	// gin 的运行模式。
	//
	// 生产必须用 ReleaseMode：
	//   - DebugMode 会在启动时打印完整路由表（等于把接口清单告诉攻击者）；
	//   - DebugMode 下每次请求有额外的调试开销；
	//   - DebugMode 会在启动时打印一行 "WARNING: This is a development build"，
	//     混在日志里容易让人误判线上跑的是开发版。
	//
	// 顺带说明：gin 自身也会读取 GIN_MODE 环境变量（gin 的 init 里做的），
	// 所以镜像里设 GIN_MODE=release 同样生效。这里再显式设置一次，
	// 是为了让「模式由谁决定」在代码里可见，而不是藏在一个环境变量里。
	if cfg.IsProd() {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// 启动期 context 只用于建立连接，不参与服务的生命周期。
	ctx := context.Background()
	st, err := store.Open(ctx, cfg)
	if err != nil {
		return err
	}
	// 先注册关闭，再启动服务：保证任何启动失败路径都会释放连接。
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("释放连接失败", "err", err)
		}
	}()

	engine, err := router.New(handler.New(st, version))
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    cfg.App.Addr,
		Handler: engine,
		// Go 的 http.Server 默认没有任何超时。一个只建立连接、不发数据的客户端
		// （slowloris）就能永久占住一个连接和 goroutine。暴露到公网后，
		// 这是最容易被利用的可用性缺口，而且症状是「连接数慢慢涨到打满」，
		// 很难联想到是超时没设。
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		// 请求头上限，防止超大 header 占内存。
		MaxHeaderBytes: 1 << 20,
	}

	// 监听 SIGTERM 与 SIGINT。
	//
	// SIGTERM 是容器停止时编排系统发的信号（docker stop / K8s termination）。
	// 如果进程不处理它，默认行为是立即退出 —— 正在处理的请求被切断，
	// 用户看到连接重置，数据库里可能留下写了一半的数据。
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务已启动", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		// 端口被占用之类的启动期错误：直接返回，交给 main 决定退出码。
		return err
	case <-sigCtx.Done():
		// 收到停止信号。注意这里要「恢复」信号处理的默认行为并在必要时强制退出：
		// 如果关停过程中又收到一次 SIGTERM（用户连按 Ctrl+C、编排系统着急），
		// 应该立刻退出，而不是继续等待。
		stop()
	}

	logger.Info("收到停止信号，开始优雅关停", "timeout", cfg.App.ShutdownTimeout)

	// 用独立的 context 控制关停时限：不能复用已经取消的 sigCtx，
	// 否则 Shutdown 会立刻返回，等于没有等待在途请求。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()

	// Shutdown 做的事：停止接受新连接，然后等待在途请求处理完。
	// 它不会中断正在执行的 handler —— 这正是「优雅」的含义。
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关停超时，强制关闭", "err", err)
		// Close 会立即断开所有连接，在途请求失败。
		// 但它至少保证了进程能退出，而不是卡死在那等编排系统 SIGKILL。
		if closeErr := srv.Close(); closeErr != nil {
			logger.Error("强制关闭失败", "err", closeErr)
		}
	} else {
		logger.Info("在途请求已处理完毕")
	}

	logger.Info("服务已退出")
	return nil
}

// setupLogger 配置 slog。
//
// 线上用 JSON：日志会被采集系统（Loki / ELK / CloudWatch）解析，
// 纯文本格式一旦遇到带换行的错误信息就会错行，而结构化日志不会被一条消息搞乱。
// 开发环境用 Text 更好读，这里按环境切换。
func setupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	switch cfg.Log.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	if cfg.IsProd() {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}

	// 日志写 stdout 而不是文件：
	// 容器的日志由运行时负责收集和轮转，应用自己往容器内写文件，
	// 会导致日志随容器销毁而丢失，也无法用 docker logs 看到。
	slog.SetDefault(slog.New(h))
}

// probe 向本机发一次 /healthz 请求。
func probe(addr string) error {
	// 监听地址 ":8080" 表示 0.0.0.0，不能直接拿来做客户端目标，
	// 需要补上 127.0.0.1。这个细节不处理的话，healthcheck 会一直失败并报
	// "dial tcp :8080: connect: connection refused"。
	target := addr
	if strings.HasPrefix(target, ":") {
		target = "127.0.0.1" + target
	}

	url := "http://" + target + "/healthz"

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("探活请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("探活返回非 200: %d", resp.StatusCode)
	}
	return nil
}
