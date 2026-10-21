// Step 3：把「能跑」变成「能停」。
//
// Step 1 和 Step 2 都用 r.Run(":8080") 一行启动，看起来极其简洁。
// 这一步要拆开它，因为那一行藏着三个问题：
//
//	1. 地址写死。换端口要改代码重新编译。
//	2. 没有超时。Go 的 http.Server 默认**不给任何超时**，
//	   一个只连不发数据的客户端就能永久占住一个连接（slowloris 攻击）。
//	3. 无法优雅关停。Ctrl+C 会直接切断正在处理的请求——
//	   用户看到报错，而数据库里可能已经写了一半的数据。
//
// 这三点的解决办法都在本文件里：读配置、显式构造 http.Server、监听信号。
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
	"ginlesson/step03/internal/config"
	"ginlesson/step03/internal/router"
)

func main() {
	// main 只做一件事：把 run() 的返回值变成退出码。
	//
	// 为什么不直接在 run 里 os.Exit？
	// 因为 os.Exit 会**跳过所有 defer**。如果 run 里写了 `defer file.Close()`，
	// 直接 os.Exit 会让它不执行。把 os.Exit 限制在 main 这一层，
	// 就保证了 run 内部的 defer 都能正常跑完。
	os.Exit(run())
}

// run 返回退出码：0 表示正常，非 0 表示异常。
//
// 这个「run() int」的小模式值得记住：它让整个启动流程变得可测试，
// 也让每个错误分支的处理方式统一（返回码 + 打印原因，而不是随处 os.Exit / log.Fatal）。
func run() int {
	var configPath = flag.String("config", "configs/config.toml", "配置文件路径")
	flag.Parse()

	// ---------- 1. 读配置 ----------
	cfg, err := config.Load(*configPath)
	if err != nil {
		// 此刻还没初始化日志，只能写标准错误。
		// 配置错误必须让启动**失败**而不是用默认值硬跑：
		// 带着错误的数据库地址"启动成功"，比直接启动失败难查得多。
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}

	// ---------- 2. 设置 gin 模式 ----------
	//
	// 必须在创建引擎**之前**设置。gin.SetMode 会改变全局的 debug 标志，
	// 影响后续创建的所有引擎；放在 gin.Default() 之后虽然大多也能生效，
	// 但会打出一行 "debug" 的启动信息，容易误判。
	if cfg.Server.IsRelease() {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// ---------- 3. 组装引擎 ----------
	engine := gin.Default()
	router.Register(engine)

	// ---------- 4. 构造 http.Server ----------
	//
	// 这是本步的核心：不再用 engine.Run()，而是自己构造标准库的 Server。
	// engine.Run(addr) 内部就是 http.ListenAndServe(addr, engine)，
	// 而 ListenAndServe 用的是**零值** http.Server——所以那几个超时全是 0（不限时），
	// 而且它不提供任何关停接口。
	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: engine, // gin.Engine 实现了 http.Handler 接口

		// 各超时的作用（顺序就是它们生效的顺序）：
		//
		// ReadHeaderTimeout：从连接建立起，读完请求头的时限。
		//   这是防 slowloris 的关键——攻击者可以慢慢发 header，一个字一个字地发，
		//   如果没有这个超时，一个连接就能被无限期占住。
		//   它**不**限制请求体，所以大文件上传不受影响。
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeoutDur(),

		// ReadTimeout：读完整个请求（含 body）的时限。
		//   注意它是从连接建立起算的，包含客户端上传文件的时间——
		//   如果将来支持上传大文件，这个值要放大，否则大文件必然超时。
		ReadTimeout: cfg.Server.ReadTimeoutDur(),

		// WriteTimeout：写完响应的时限。
		//   注意它的计时起点是**读完请求头之后**，也就是说
		//   它已经把 handler 的执行时间算进去了。
		//   所以它必须大于你最慢的接口耗时，否则那个接口一定失败。
		WriteTimeout: cfg.Server.WriteTimeoutDur(),

		// IdleTimeout：keep-alive 连接在两次请求之间的空闲存活时间。
		//   浏览器会复用连接，如果这个值太大，大量空闲连接会占着文件描述符；
		//   太小则会频繁重建连接（每次都要 TLS 握手，延迟上升）。
		IdleTimeout: cfg.Server.IdleTimeoutDur(),

		// MaxHeaderBytes：请求头最大字节数，防止超大 header 吃内存。
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	// ---------- 5. 启动并等待退出信号 ----------
	return serve(srv, cfg.Server.ShutdownTimeoutDur())
}

// serve 启动服务并阻塞，直到收到信号或服务出错。
//
// 单独拆成函数而不是写在 run 里，是因为这段逻辑有 40 行且包含一个 select 分支，
// 混在启动流程里会让 run 的主线看不清。
func serve(srv *http.Server, shutdownTimeout time.Duration) int {
	// 缓冲 1 个的 channel。
	//
	// 为什么必须缓冲：如果无人接收时信号到达，signal 包的投递语义是「丢失」。
	// 缓冲 1 个能保证「信号已经发生」这件事被记住，不会因为
	// 主 goroutine 正好在处理别的分支而错过。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// 函数返回前停止监听，避免重复注册（在测试里尤其重要）。
	defer signal.Stop(quit)

	// 在 goroutine 里启动监听。
	//
	// 为什么不在主 goroutine 里 ListenAndServe？
	// 因为它是**阻塞**的——它会一直阻塞在 accept 循环里，
	// 主 goroutine 就没机会去等信号了。所以必须放到后台，
	// 主 goroutine 通过 select 等待「出错」或「收到信号」两种结局。
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("HTTP 服务启动，监听 %s\n", srv.Addr)
		// ListenAndServe 在正常关停时会返回 http.ErrServerClosed，
		// 那**不是**错误，是关停的正常结果，必须排除掉。
		// 漏掉这个判断的话，每次 Ctrl+C 都会打印一条吓人的错误。
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		// 典型情况：端口被占用（address already in use）。
		// 这类错误必须打印人能看懂的原因，而不是让进程静默退出。
		fmt.Fprintf(os.Stderr, "HTTP 服务启动失败: %v\n", err)
		return 1
	case sig := <-quit:
		fmt.Printf("\n收到信号 %s，开始优雅关停（最多等 %s）\n", sig, shutdownTimeout)
	}

	// ---------- 优雅关停 ----------
	//
	// Shutdown 做了三件事，顺序很重要：
	//   1. 关闭监听 socket —— 不再接受新连接（新请求会被拒绝，由负载均衡转走）
	//   2. 关闭所有空闲连接
	//   3. **等待**正在处理中的请求完成
	//
	// 第 3 点就是它和 Close() 的本质区别：
	//   Close()    立刻关闭所有连接，在途请求全部中断（客户端收到连接重置）
	//   Shutdown() 先停止接客，再等在途请求做完，超时才强制关闭
	//
	// 所以 Close() 是「紧急断电」，Shutdown() 是「拉闸限行」。
	// 正常情况下永远用 Shutdown，只在超时后兜底用 Close。
	//
	// 关键细节：这个 context **必须是新的**，不能用请求的 context，
	// 也不能用已经被取消的 context —— Shutdown 一旦发现 context 已取消
	// 就会立即返回，那样等于没等。
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		// 走到这里说明「等超时了还有请求没做完」。
		// 这时才用 Close 强制断开，并如实记录——这是个值得关注的事件，
		// 说明有接口比 shutdownTimeout 还慢。
		fmt.Fprintf(os.Stderr, "优雅关停超时（%s），强制关闭剩余连接: %v\n", shutdownTimeout, err)
		if closeErr := srv.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "强制关闭失败: %v\n", closeErr)
		}
		return 1
	}

	// 如果这里将来要关闭数据库、Redis 连接池，必须放在 Shutdown **之后**。
	// 顺序颠倒的话，在途请求会在使用已关闭的连接时报错——
	// 这正是优雅关停想避免的问题。
	fmt.Println("在途请求已处理完毕，服务正常退出")
	return 0
}
