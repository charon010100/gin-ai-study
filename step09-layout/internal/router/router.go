// Package router 只做 URL → handler 的映射。
//
// 它不含业务逻辑，也不创建任何依赖：engine、handler、logger
// 全部由调用方传入。这样它既是纯配置，也可以被测试直接复用。
package router

import (
	"github.com/gin-gonic/gin"

	"ginlesson/step09/internal/handler"
	"ginlesson/step09/internal/middleware"
	"ginlesson/step09/pkg/logger"
)

// Deps 是路由需要的依赖。
//
// 用结构体收集而不是一长串参数：
// 每加一个 handler，函数签名不变，所有调用点也不用改。
// 这在 router 这种「会持续变大」的地方收益最明显。
type Deps struct {
	Posts    *handler.PostHandler
	Comments *handler.CommentHandler
	Log      *logger.Logger
}

const apiVersion = "/api/v1"

// Register 注册全部路由。
func Register(engine *gin.Engine, d Deps) {
	// 全局中间件。
	// 顺序有意义：RequestID 必须在最前，后面所有日志才能带上它。
	engine.Use(middleware.RequestID())
	engine.Use(middleware.AccessLog(d.Log))

	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := engine.Group(apiVersion)
	{
		posts := api.Group("/posts")
		{
			posts.GET("", d.Posts.List)
			posts.POST("", d.Posts.Create)
			posts.GET("/:slug", d.Posts.Get)
			posts.DELETE("/:slug", d.Posts.Delete)
			posts.GET("/:slug/comments", d.Comments.List)
			posts.POST("/:slug/comments", d.Comments.Submit)
		}

		// 后台分组。
		//
		// 现在没有鉴权，但分组已经建好 —— 这是结构上的位置预留。
		// 下一步只需要 admin.Use(middleware.AuthRequired(...))，
		// 组内所有路由自动受保护，不会出现「新加了个接口忘了加鉴权」。
		admin := api.Group("/admin")
		{
			admin.PUT("/comments/:id/approve", d.Comments.Approve)
			admin.GET("/comments/pending", d.Comments.Pending)
		}
	}
}
