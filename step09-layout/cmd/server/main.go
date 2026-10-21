// Command server 是 step09 的入口。
//
// 这个文件是「依赖注入的唯一发生地」：全项目只有这里 new 对象，
// 其余各层只接收依赖。这是能让「分层」从口号变成事实的关键 ——
// 一旦允许各层自己创建依赖，依赖方向就无法再被检查。
//
// 为什么不引入 wire / fx 之类的 DI 框架：
// 手写装配在这个规模下不到 50 行，而框架会带来代码生成步骤、
// 隐式行为、以及「出问题不知道该看哪里」。
// 等装配超过 150 行再考虑，那时你也更清楚自己需要什么。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ginlesson/step09/internal/bootstrap"
	"ginlesson/step09/internal/cache"
	"ginlesson/step09/internal/config"
	"ginlesson/step09/internal/handler"
	"ginlesson/step09/internal/repository"
	"ginlesson/step09/internal/router"
	"ginlesson/step09/internal/service"
	"ginlesson/step09/pkg/logger"

	"github.com/gin-gonic/gin"
)

// 跨包编译期断言：cache.Client 必须满足 service.CachePort。
//
// 放在 main 包是因为只有这里同时 import 了 cache 与 service。
// 写在 cache 包里会让 cache 依赖 service，依赖方向就反了。
//
// 这一行的价值：如果哪天有人改了 CachePort 的方法签名，
// 编译立刻失败，而不是运行时才发现某个方法没实现。
var _ service.CachePort = (*cache.Client)(nil)

func main() {
	// ---------- 1. 配置 ----------
	cfg, err := config.Load()
	if err != nil {
		// 配置阶段日志还没初始化，只能用标准库。
		log.Fatalf("加载配置失败: %v", err)
	}

	// ---------- 2. 日志 ----------
	lg := logger.New(logger.LevelInfo)

	ctx := context.Background()

	// ---------- 3. 基础设施 ----------
	db, err := bootstrap.OpenDatabase(ctx, cfg.DSN)
	if err != nil {
		lg.Error("初始化数据库失败: %v", err)
		os.Exit(1)
	}
	// 用 defer 关闭：defer 在 serve 返回之后才执行，
	// 正好是「先停流量、再关依赖」的顺序。
	if sqlDB, err := db.DB(); err == nil {
		defer func() { _ = sqlDB.Close() }()
	}

	if err := bootstrap.Seed(ctx, db); err != nil {
		lg.Error("灌入种子数据失败: %v", err)
		os.Exit(1)
	}

	c := cache.New(ctx, cfg.RedisAddr, cfg.RedisPass, cfg.RedisDB, "blog:")
	defer func() {
		if err := c.Close(); err != nil {
			lg.Warn("关闭 Redis 失败: %v", err)
		}
	}()

	// ---------- 4. 仓储层（数据访问） ----------
	store := repository.New(db)

	// ---------- 5. 服务层（业务规则） ----------
	//
	// store 同时满足 PostRepo、CommentRepo、TxManager 三个接口。
	// 每个 service 只声明自己需要的那部分，于是
	// 「PostService 能不能开事务」在类型上一目了然 —— 它不能，也不需要。
	postSvc := service.NewPostService(store, c, cfg.MaxPageSize)
	commentSvc := service.NewCommentService(store, store, store)

	// ---------- 6. HTTP 层 ----------
	postH := handler.NewPostHandler(postSvc)
	commentH := handler.NewCommentHandler(commentSvc)

	engine := gin.New()
	// 用 gin.New() + 自带 Recovery，而不是 gin.Default()：
	// Default 会装上它固定的 Logger，格式改不了、也带不上 request_id，
	// 还会和我们自己的 AccessLog 重复输出。
	engine.Use(gin.Recovery())
	router.Register(engine, router.Deps{
		Posts:    postH,
		Comments: commentH,
		Log:      lg,
	})

	// ---------- 7. 启动 ----------
	//
	// 不用 engine.Run(addr)。它在内部是裸的 http.ListenAndServe：
	//   - 没有任何超时，慢客户端（slowloris）能永久占住连接
	//   - 无法优雅关停，Ctrl+C 会直接切断在途请求
	// 这两点都是 Step 3 的内容，这里只是应用它。
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	lg.Info("服务启动于 http://%s", cfg.Addr)
	if err := serve(srv, lg, 15*time.Second); err != nil {
		lg.Error("服务异常退出: %v", err)
		os.Exit(1)
	}
	lg.Info("服务已退出")
}

// serve 启动服务并等待退出信号。实现与 Step 3 相同，不再展开解释。
func serve(srv *http.Server, lg *logger.Logger, shutdownTimeout time.Duration) error {
	// 缓冲 1 个：signal.Notify 在无人接收时会丢弃信号，
	// 缓冲能保证「信号已发出」这一事实不丢。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		// 端口被占用这类启动期错误：直接返回，由 main 决定退出码。
		return err
	case sig := <-quit:
		lg.Info("收到信号 %s，开始优雅关停", sig)
	}

	// 用独立的 context 控制关停时限。
	// 不能复用已被取消的 ctx，否则 Shutdown 会立刻返回，
	// 起不到「等待在途请求」的作用。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		lg.Warn("优雅关停超时，强制关闭剩余连接: %v", err)
		return srv.Close()
	}

	lg.Info("HTTP 已停止接收请求，在途请求处理完毕")
	return nil
}
