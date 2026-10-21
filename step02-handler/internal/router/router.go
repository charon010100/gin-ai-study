// Package router 只负责一件事：把 URL 映射到处理函数。
//
// 为什么要把这件事从 main 里拿出来？
//
// 因为它是「会持续变长的代码」。一个博客有文章、分类、标签、归档、评论、
// 搜索、后台管理……最后会有几十条路由。这类代码的特点是不含逻辑、
// 纯粹是清单，放在 main 里会把真正的启动逻辑淹没。
//
// 注意本包不做任何「创建引擎」的事：engine 由调用方传入。
// 这样 router 包不关心引擎是怎么配置的（超时、模式、中间件），
// 也不依赖任何具体的初始化方式，必要时可以被测试代码用自己造的 engine 调用。
package router

import (
	"github.com/gin-gonic/gin"
	"ginlesson/step02/internal/handler"
)

// Register 注册全部路由。
//
// apiVersion 单独提出来，是因为接口版本会变，而它出现在每一条路由里。
// 写死字符串的话，将来升 v2 就要全文替换，容易漏。
const apiVersion = "/api/v1"

// Register 把所有路由挂到 engine 上。
func Register(engine *gin.Engine) {
	// 创建处理器实例。
	//
	// 这里只创建一次，然后所有路由共享它 —— 因为 Handler 里持有共享数据
	// （内存中的 posts 切片）。如果每条路由都 new 一个 Handler，
	// 它们会各自持有独立的副本，新建的评论在另一个接口里就查不到。
	//
	// 这个「共享一个实例」的要点，在 Step 7 换上数据库后会更明显：
	// 那时共享的是 *gorm.DB（连接池），每条路由都新建连接池会瞬间打满数据库连接数。
	h := handler.NewHandler()

	// ① 认证相关的路由（假设）。
	//
	// 这里用一个 Group 来演示分组，虽然只有一条路由。
	// 分组的价值有两个：
	//   - 路径前缀只写一次，不然后面每条都是 "/api/v1/auth/login"
	//   - 可以在分组上批量挂中间件：auth := api.Group("/auth", middleware.RequireLogin())
	//     Step 6 会详细讲这个用法，它也是「不要把管理接口误挂到公开组」的保障。
	auth := engine.Group(apiVersion + "/auth")
	{
		// 花括号不是语法要求，纯粹是为了让分组边界在视觉上清晰。
		// Go 允许直接写成 engine.Group(...).GET(...)，但路由一多就看不出
		// 哪些属于哪个分组了。
		auth.POST("/login", func(c *gin.Context) {
			c.JSON(200, gin.H{"token": "fake-token-for-now"})
		})
	}

	// ② 文章相关路由。
	//
	// 注意这里是 apiVersion + "/posts"，而不是 apiVersion + "/post"。
	// 复数形式的资源名是 REST 惯例：/posts 表示「文章集合」，
	// /posts/:slug 表示「集合中的某一项」。用单数会让
	// 「集合」和「某一项」的语义混淆。
	posts := engine.Group(apiVersion + "/posts")
	{
		// GET /api/v1/posts?page=1&page_size=10&keyword=gin
		// 列表：用查询参数做分页与筛选。
		posts.GET("", h.ListPosts)

		// GET /api/v1/posts/:slug
		// 详情：用路径参数标识唯一资源。
		//
		// 为什么详情用 slug 而不是 /posts/:id？
		// 因为 slug 是 URL 友好的（redis-cache-consistency），
		// 而数字 ID 会暴露文章总数、发布顺序，也不利于 SEO。
		// 两者可以并存：对外用 slug，内部用 ID。
		posts.GET("/:slug", h.GetPost)

		// POST /api/v1/posts/:slug/comments
		// 给指定文章创建评论。
		//
		// 这个路径体现了「评论从属于文章」的关系：
		// 不写成 /comments?post_slug=xxx，是因为从属资源的归属关系
		// 应该体现在路径结构里，而不是塞进查询参数。
		posts.POST("/:slug/comments", h.CreateComment)
	}

	// ③ 根路径，方便浏览器直接打开时能看到点东西。
	engine.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"service": "gin tutorial step02",
			"routes": []string{
				"GET  /api/v1/posts",
				"GET  /api/v1/posts/:slug",
				"POST /api/v1/posts/:slug/comments",
				"POST /api/v1/auth/login",
			},
		})
	})
}
