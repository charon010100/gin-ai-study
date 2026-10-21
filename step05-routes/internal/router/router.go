// Package router 负责路由注册。
//
// 本步的重点全部在这个文件里：分组、嵌套、静态路由与参数路由的优先级、
// 以及两类路由（HTML 页面 / JSON 接口）各自的兜底处理。
package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"ginlesson/step05/internal/handler"
)

// Register 注册全部路由。
func Register(engine *gin.Engine, h *handler.Handler) {
	// ---------- HTML 页面 ----------
	//
	// 这些路由返回给浏览器看的页面，和 API 分开前缀。
	// 混在一起的后果：前端请求 /posts 时无法区分用户想要 HTML 还是 JSON。
	engine.GET("/", h.Index)
	engine.GET("/posts/:slug", h.PostPage)

	// ---------- JSON 接口 ----------
	//
	// 演示分组嵌套：/api → /v1 → /posts
	//
	// 为什么要嵌套而不是直接写 "/api/v1/posts"？
	// 因为 v1 这一层将来会在这里挂中间件（比如「v1 接口全部开启限流」），
	// 而 /api 这一层可以统一处理跨版本的事情。
	api := engine.Group("/api")
	{
		v1 := api.Group("/v1")
		{
			posts := v1.Group("/posts")
			{
				// 注意这里同时注册了静态路径 /hot 和参数路径 /:slug。
				//
				// gin 的路由树会优先匹配**静态**路径：
				// 请求 /api/v1/posts/hot 命中第一条，不会被当作 slug="hot"。
				//
				// 这个优先级很关键。如果没有它，/hot 就会被 /:slug 吃掉，
				// 于是「热门文章」接口永远返回「文章 hot 不存在」的 404。
				posts.GET("/hot", h.Hot)
				posts.GET("", h.ListPosts)
				posts.GET("/:slug", h.GetPost)
			}
		}
	}

	// ---------- 管理接口 ----------
	//
	// 这就是「分组」的真正价值：中间件挂在分组上，
	// 组内**所有**路由自动获得鉴权，不可能漏挂。
	//
	// 对比一下容易出错的写法：
	//   engine.DELETE("/api/v1/admin/posts/:slug", adminOnly(), h.DeletePost)
	//   engine.GET("/api/v1/admin/stats", h.Stats)          ← 漏了 adminOnly！
	//
	// 第二种写法下，新增一个管理接口时忘记加中间件，就是一次越权漏洞，
	// 而且代码 review 时很难发现（因为每个路由看起来都差不多）。
	// 用分组，漏挂从「可能发生」变成「结构上不可能」。
	admin := engine.Group("/api/v1/admin", adminOnly())
	{
		admin.DELETE("/posts/:slug", h.DeletePost)
		admin.GET("/stats", h.Stats)
	}

	// ---------- 兜底 ----------
	//
	// 关键点：兜底必须**区分两类路由**。
	//
	// 如果统一返回 JSON，用户访问不存在的页面时会看到一坨 JSON；
	// 如果统一返回 HTML，前端调 API 拿到的是 HTML，JSON.parse 直接崩。
	//
	// 所以按路径前缀分流：/api/ 开头返回 JSON，其余返回 HTML 页面。
	engine.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{
				"code":    4001,
				"message": "接口不存在",
			})
			return
		}
		c.HTML(http.StatusNotFound, "404.html", gin.H{"Title": "页面不存在"})
	})
}

// adminOnly 是一个极简的鉴权中间件，只为演示「中间件挂在分组上」。
//
// 真实实现当然不会用一个固定的字符串做凭证，Step 6 会把它换成
// 正规的中间件包（带 request_id、日志、失败响应统一格式）。
//
// 这里刻意保留了几个「不完整的细节」以便说明：
//   - 用 header 传 token 而不是 Cookie，是为了方便 curl 测试
//   - 没有校验 token 的时效与来源，因为没有用户体系（Step 7 之后才有）
func adminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 演示用固定凭证：请求头 X-Admin-Token: demo
		if c.GetHeader("X-Admin-Token") != "demo" {
			// AbortWithStatusJSON = 写响应 + 标记链路结束。
			// 少了 Abort 的话，这个中间件返回后 handler 仍会执行，
			// 于是「已经拒绝了」的请求还是产生了副作用。
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"code":    2001,
				"message": "请先登录（测试时请加请求头 X-Admin-Token: demo）",
			})
			return
		}

		// c.Set 把值放进本次请求的上下文，供后续 handler 读取。
		//
		// 注意：c.Set 写入的是**这个** Context 对象。如果下游某个函数
		// 用*值*接收 gin.Context（少了星号），它拿到的是副本，
		// 取不到这里写入的值——这就是 Step 2 讲的那个坑的实际影响场景。
		c.Set("admin_user", "admin")
		c.Next()
	}
}
