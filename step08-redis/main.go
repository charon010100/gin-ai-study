// Step 8：接入 Redis（缓存、浏览量、限流）。
//
// 这一步解决 Step 7 的三个遗留问题：
//  1. 每次访问详情都全量查库
//  2. 浏览量无法统计（每次访问都 UPDATE 会把库写满）
//  3. 没有防刷手段
//
// 核心难点不在「怎么用 go-redis」，而在**Redis 挂了怎么办**。
// 详见 cache 包与 NOTES。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ginlesson/step08/internal/bootstrap"
	"ginlesson/step08/internal/cache"
	"ginlesson/step08/internal/handler"
	"ginlesson/step08/internal/repository"
	"ginlesson/step08/internal/router"
	"github.com/gin-gonic/gin"
)

func main() {
	dsn := flag.String("dsn", "", "SQLite 数据库路径，留空使用 data/blog.db")
	redisAddr := flag.String("redis", "127.0.0.1:6379", "Redis 地址")
	redisDB := flag.Int("redisdb", 0, "Redis 逻辑库编号")
	// 这个参数是刻意加的：没有 Redis 环境时，你仍然可以
	// 用 -redis="" 启动服务，观察「降级模式」下的行为。
	// 教学里最容易被忽略的就是「依赖不可用」这条路径。
	redisDisabled := flag.Bool("noredis", false, "不连接 Redis（用于观察降级模式）")
	flushEvery := flag.Duration("flush", time.Minute, "浏览量回写间隔")
	flag.Parse()

	ctx := context.Background()

	// ---------- 数据库 ----------
	dbCfg := bootstrap.DefaultDatabase()
	if *dsn != "" {
		dbCfg.DSN = *dsn
	}
	db, err := bootstrap.OpenDatabase(ctx, dbCfg)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		defer func() { _ = sqlDB.Close() }()
	}
	if err := bootstrap.Seed(ctx, db); err != nil {
		log.Fatalf("灌入示例数据失败: %v", err)
	}

	// ---------- Redis ----------
	//
	// 注意 Connect 的返回值：即使连不上，err 也是 nil，
	// 返回的是一个「永远未命中」的客户端。这是有意的设计 ——
	// 缓存不可用不应该阻止服务启动。
	var redisClient *cache.Client
	if *redisDisabled {
		log.Println("[INFO] 已通过 -noredis 跳过 Redis，直接进入降级模式")
		redisClient, err = cache.Connect(ctx, cache.Options{
			// 指向一个不存在的端口，让所有操作快速失败。
			// 这样你不装 Redis 也能观察降级行为。
			Addr:        "127.0.0.1:1",
			Prefix:      "blog:",
			DialTimeout: 200 * time.Millisecond,
		})
	} else {
		redisClient, err = cache.Connect(ctx, cache.Options{
			Addr:        *redisAddr,
			DB:          *redisDB,
			Prefix:      "blog:",
			DialTimeout: 3 * time.Second,
		})
	}
	if err != nil {
		// 走到这里说明是参数错误这类「不可能恢复」的问题，
		// 而不是「Redis 暂时不可用」。后者在 Connect 内部已被降级处理。
		log.Fatalf("初始化 Redis 客户端失败: %v", err)
	}
	defer func() { _ = redisClient.Close() }()

	// ---------- 装配 ----------
	// 关键的一行：GORM 实现 → 套一层缓存 → 交给 handler。
	//
	// handler 收到的类型是 repository.Repo（接口），
	// 它完全不知道缓存的存在。所以「加缓存」这个需求
	// 没有改动 handler 一行代码 —— 这就是装饰器的意义。
	baseRepo := repository.NewGormRepo(db)
	cachedRepo := repository.NewCachedRepo(baseRepo, redisClient)
	h := handler.New(cachedRepo, redisClient)

	// ---------- 浏览量回写 ----------
	//
	// 单独起一个后台 goroutine，每分钟把 Redis 里的增量刷进 DB。
	// 停止函数在退出时调用，保证最后一次增量不丢。
	flusher := cache.NewViewsFlusher(redisClient, func(ctx context.Context, deltas map[uint64]int64) error {
		return cachedRepo.IncrPostViews(ctx, deltas)
	}, *flushEvery)
	stopFlusher := flusher.Start(ctx)
	defer stopFlusher()

	// ---------- 限流 ----------
	// 评论：同一 IP 每分钟最多 5 条。
	//
	// 阈值不能只看「防攻击」，还要看「正常用户会不会被误伤」。
	// 一个人连续回复 6 条评论是可能的，所以给到 5；
	// 如果这是登录接口，应该更严（比如 5 次/15 分钟）。
	limiter := cache.NewRateLimiter(redisClient, "comment", time.Minute, 5)

	// ---------- HTTP ----------
	engine := gin.Default()
	router.Register(engine, h, limiter)

	addr := getenv("ADDR", ":8080")

	// 和 Step 3 一样，不用 engine.Run(addr)：
	// 关停时要先停流量、再停后台任务，最后关数据库——
	// 顺序错了会丢 Redis 里还没回写的浏览量增量。
	srv := &http.Server{
		Addr:              addr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	if err := serve(srv, 15*time.Second); err != nil {
		log.Fatalf("服务异常退出: %v", err)
	}

	// serve 返回后，上面 defer 的 stopFlusher 会执行最后一次回写。
	// 这就是「先停 HTTP、再停后台任务」的顺序保证 —— 反过来的话，
	// 关停瞬间还在进来的请求产生的浏览量会丢。
}

// serve 启动服务并等待退出信号。实现与 Step 3 相同。
func serve(srv *http.Server, shutdownTimeout time.Duration) error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("服务启动: http://localhost%s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("收到信号 %s，开始优雅关停", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("优雅关停超时，强制关闭: %v", err)
		return srv.Close()
	}

	log.Println("HTTP 已停止，正在回写最后的浏览量增量")
	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
