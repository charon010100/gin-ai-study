// Step 4：让所有接口的返回格式统一，并确保内部错误不外泄。
//
// Step 3 之后，服务能配、能停、有超时了。但每个 handler 里还是这样写：
//
//	c.JSON(400, gin.H{"error": err.Error()})
//	c.JSON(404, gin.H{"error": "文章不存在"})
//	c.JSON(200, gin.H{"list": ..., "total": ...})
//
// 三个问题：
//  1. 格式不一致 —— 前端为每个接口写一套解析，改一处忘一处
//  2. err.Error() 直接外泄内部结构（结构体名、字段名、校验规则）
//  3. 500 错误时把 SQL 和连接串也一起泄露出去了
//
// 本步用一个 response 包把这些全部收口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"ginlesson/step04/internal/config"
	"ginlesson/step04/internal/router"
)

func main() {
	os.Exit(run())
}

func run() int {
	var configPath = flag.String("config", "configs/config.toml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}

	if cfg.Server.IsRelease() {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.Default()
	router.Register(engine)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.Server.ReadTimeoutDur(),
		WriteTimeout:      cfg.Server.WriteTimeoutDur(),
		IdleTimeout:       cfg.Server.IdleTimeoutDur(),
		MaxHeaderBytes:    1 << 20,
	}

	return serve(srv, cfg.Server.ShutdownTimeoutDur())
}

// serve 启动服务并等待退出信号（逻辑与 Step 3 相同）。
func serve(srv *http.Server, shutdownTimeout time.Duration) int {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("HTTP 服务启动，监听 %s\n", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "HTTP 服务启动失败: %v\n", err)
		return 1
	case sig := <-quit:
		fmt.Printf("\n收到信号 %s，开始优雅关停\n", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "优雅关停超时: %v\n", err)
		_ = srv.Close()
		return 1
	}

	fmt.Println("服务正常退出")
	return 0
}
