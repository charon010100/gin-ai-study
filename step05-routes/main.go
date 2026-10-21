// Step 5：路由的「结构性」问题。
//
// 到这里，服务的骨架、配置、优雅关停、响应格式都齐了。
// 本步处理的是「路由本身」——它看起来最枯燥，但出错时最难查，
// 因为路由的 bug 往往表现为「某个接口莫名其妙 404」。
//
// 本步四个主题：
//  1. 分组与嵌套（以及为什么分组是**安全**问题）
//  2. 静态路由与参数路由的优先级
//  3. 路由冲突：启动时就 panic，而不是运行时才出错
//  4. 静态资源与 HTML 模板（embed 而不是相对路径）
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"ginlesson/step05/internal/config"
	"ginlesson/step05/internal/handler"
	"ginlesson/step05/internal/router"
)

// 把模板和静态文件**编译进二进制**。
//
// 为什么不用 engine.LoadHTMLGlob("web/templates/*")？
//
// 因为那是**相对路径**，依赖于进程的当前工作目录。这会带来一个经典故障：
//
//	在项目目录下 go run .  → 正常
//	用 systemd / docker 启动 → 找不到模板，访问首页直接 500
//
// 原因是 systemd 的 WorkingDirectory 通常不是代码目录，容器里的 WORKDIR
// 也不会正好命中。这种「本地能跑、线上报错」的问题排查成本很高。
//
// 用 //go:embed 把文件打进二进制后，程序和它的资源就是一个整体，
// 与工作目录无关。代价是改模板要重新编译（对需要频繁改模板的项目，
// 可以加个开关：开发用 embed，生产用磁盘路径）。
//
//go:embed web/templates/*.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

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

	// ---------- 模板 ----------
	if err := setupTemplates(engine); err != nil {
		fmt.Fprintf(os.Stderr, "加载模板失败: %v\n", err)
		return 1
	}

	// ---------- 静态资源 ----------
	//
	// fs.Sub 把 web/static 变成子文件系统，
	// 这样 URL /static/style.css 对应文件 web/static/style.css，
	// 而不是 web/static/style.css 那一串完整路径。
	staticSub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		fmt.Fprintf(os.Stderr, "准备静态资源失败: %v\n", err)
		return 1
	}
	// engine.StaticFS 内部用 http.FileServer，
	// 它会自动处理 If-Modified-Since、Range 请求等细节——
	// 这些自己写很容易漏，尤其是大文件的分段下载。
	engine.StaticFS("/static", http.FS(staticSub))

	// 进程启动时把模板和路由都加载好，失败就退出。
	// 这类「启动期资源」绝对不能等到第一次请求时才加载：
	// 那时候进程已经在接受流量了，报错只能返回 500。
	h := handler.New()
	router.Register(engine, h)

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

// setupTemplates 从 embed.FS 解析模板并交给 gin。
//
// 为什么用 ParseFS + SetHTMLTemplate 而不是 LoadHTMLGlob？
// 因为 LoadHTMLGlob 只能读磁盘，读不了 embed.FS。
func setupTemplates(engine *gin.Engine) error {
	// template.New("") 之后 ParseFS 会用**文件名**作为模板名，
	// 所以 c.HTML(200, "index.html", ...) 里的名字是 "index.html" 而不是
	// "web/templates/index.html"。这一点很容易记错，报错信息是
	// "html/template: \"index.html\" is undefined"，一查就明白了。
	tmpl := template.New("")
	if _, err := tmpl.ParseFS(templateFS, "web/templates/*.html"); err != nil {
		return err
	}
	engine.SetHTMLTemplate(tmpl)
	return nil
}

// serve 启动服务并等待退出信号（与 Step 3/4 相同）。
func serve(srv *http.Server, shutdownTimeout time.Duration) int {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("HTTP 服务启动，监听 %s\n", srv.Addr)
		fmt.Println("首页:      http://localhost:8080/")
		fmt.Println("JSON 列表: http://localhost:8080/api/v1/posts")
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
