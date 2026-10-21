// Package router 负责路由注册。
package router

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"ginlesson/step04/internal/handler"
	"ginlesson/step04/pkg/response"
)

// Register 注册全部路由。
func Register(engine *gin.Engine) {
	h := handler.New()

	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "code": response.CodeOK})
	})

	api := engine.Group("/api/v1")
	{
		posts := api.Group("/posts")
		{
			posts.GET("", h.ListPosts)
			posts.GET("/:slug", h.GetPost)
			posts.POST("/:slug/comments", h.CreateComment)
		}
	}

	// ---------- 兜底路由 ----------
	//
	// 不注册这两个的话，gin 会返回**纯文本**的 "404 page not found"。
	// 前端统一按 JSON 解析时会直接抛错，于是「接口不存在」变成了
	// 「前端解析异常」，排查方向完全跑偏。
	//
	// 所以只要你的 API 返回 JSON，就必须接管 404 和 405。
	engine.NoRoute(func(c *gin.Context) {
		response.Fail(c, response.ErrNotFound.Wrap(
			// 原始误差进日志：记录下到底访问了哪个不存在的路径。
			// 这在排查「前端漏改接口地址」时非常有用。
			// 注意不要把它返回给客户端——那等于给攻击者一个路径探测工具。
			fmt.Errorf("route not found: %s %s", c.Request.Method, c.Request.URL.Path),
		))
	})

	engine.NoMethod(func(c *gin.Context) {
		response.Fail(c, response.New(
			http.StatusMethodNotAllowed, response.CodeNotFound, "该路径不支持此请求方法",
		))
	})
}
