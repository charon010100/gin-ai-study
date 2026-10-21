// Package router 只做一件事：把 URL 映射到处理函数。
//
// 本步新增的重点不是路由，而是**中间件挂在哪一层分组上**。
package router

import (
	"ginlesson/step08/internal/cache"
	"ginlesson/step08/internal/handler"
	"ginlesson/step08/internal/ratelimit"
	"github.com/gin-gonic/gin"
)

const apiVersion = "/api/v1"

// Register 注册全部路由。
func Register(engine *gin.Engine, h *handler.Handler, limiter *cache.RateLimiter) {
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := engine.Group(apiVersion)
	{
		posts := api.Group("/posts")
		{
			posts.GET("", h.ListPosts)
			posts.POST("", h.CreatePost)
			posts.GET("/:slug", h.GetPost)
			posts.DELETE("/:slug", h.DeletePost)
			posts.GET("/:slug/comments", h.ListComments)

			// 只有「提交评论」挂了限流，而不是整个 posts 分组。
			//
			// 为什么：读接口有限流价值（防爬），但把它和写接口放同一个阈值里，
			// 会导致正常读者刷新几次就被挡住，而写接口的阈值又不够严。
			// 不同用途的接口应该各有各的策略 —— 这也是把限流中间件
			// 挂在单个路由上而不是分组上的原因。
			posts.POST("/:slug/comments", ratelimit.Middleware(limiter), h.CreateComment)
		}

		admin := api.Group("/admin")
		{
			admin.PUT("/comments/:id/approve", h.ApproveComment)
		}
	}
}
