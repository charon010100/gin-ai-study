// Package handler 存放所有 HTTP 处理器。
//
// 这一层只做三件事，多一件都算越界：
//  1. 取出请求里的数据（路径参数、查询参数、请求体）
//  2. 调用下层完成业务（这一步现在还没抽出去，Step 9 会做）
//  3. 把结果写成响应
//
// 判断标准很粗暴：如果一段代码需要知道「HTTP 状态码」或「gin.Context」，
// 它属于 handler；如果它只关心业务规则（比如「评论必须先审后发」），
// 它不属于 handler。
package handler

import (
	"fmt"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"ginlesson/step02/internal/model"
)

// Handler 聚合所有处理器方法，并持有它们需要的依赖。
//
// 为什么要用 struct 而不是一堆独立的函数？
//
// 因为处理器需要访问共享资源。现在只是内存里的一份假数据，
// Step 7 会变成 *gorm.DB，Step 8 会再加上 *redis.Client。
// 如果用独立函数，就只能用包级全局变量来传这些依赖——
// 那样单元测试时无法替换（测试会连到真实数据库），也无法并行跑两个不同配置的实例。
//
// 用 struct 持有依赖，是 Go 里最简单有效的依赖注入形式：
// 谁创建这个 struct，谁就决定它用哪个 DB、哪个 Redis。
type Handler struct {
	// mu 保护下面的 posts。HTTP 服务的每个请求都在独立 goroutine 里跑，
	// 多个请求同时读写一个切片会发生数据竞争（用 -race 跑能立刻发现问题，
	// 不报错的后果是数据错乱或进程崩溃）。
	mu    sync.RWMutex
	posts []model.Post

	// seq 用于生成评论 ID，同样受 mu 保护。
	seq int
}

// NewHandler 创建处理器，并把示例数据塞进去。
//
// 返回值是 *Handler 而不是 Handler（值）：
// 因为 Handler 里含 sync.RWMutex，值拷贝会复制锁的状态，
// 导致两个副本各锁各的，锁完全失效。含 mutex 的结构体一律用指针传递。
func NewHandler() *Handler {
	return &Handler{
		posts: []model.Post{
			{
				ID: "1", Title: "为什么 Gin 的 Context 必须用指针",
				Slug: "gin-context-pointer", Summary: "一次真实的踩坑记录。",
				Author: "博主", Views: 128, CreateAt: "2024-01-01T10:00:00Z",
			},
			{
				ID: "2", Title: "从零搭建 Go 后端的分层结构",
				Slug: "go-project-layout", Summary: "main 包应该有多薄。",
				Author: "博主", Views: 86, CreateAt: "2024-01-02T10:00:00Z",
			},
			{
				ID: "3", Title: "Redis 缓存与数据库一致性",
				Slug: "redis-cache-consistency", Summary: "删缓存还是更新缓存？",
				Author: "博主", Views: 45, CreateAt: "2024-01-03T10:00:00Z",
			},
		},
	}
}

// ListPosts 返回文章列表，支持分页与关键词筛选。
//
// 路径：GET /api/v1/posts
//
// 关于方法接收者用指针还是值：这里用 *Handler 是必须的——
// 上一条注释说过，Handler 含 mutex，值接收者会导致锁失效。
// 顺带一提，如果某个方法真的不需要访问 Handler 的字段，
// 它可以写成值接收者，但混用容易忘记，统一用指针更省心。
func (h *Handler) ListPosts(c *gin.Context) {
	// ① 取参数：用 ShouldBindQuery 而不是 c.Query。
	var req ListPostsQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		// 参数不合法属于「客户端请求有问题」，返回 400 而不是 500。
		// 区分 4xx 和 5xx 的意义：4xx 说明重试也没用，5xx 说明可以重试。
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	req.normalize()

	// ② 取数据。加读锁，允许并发读。
	h.mu.RLock()
	all := h.posts
	h.mu.RUnlock()

	// 这里用最朴素的方式实现筛选与分页。
	// 真实项目里这一步会变成一条 SQL：
	//   SELECT ... WHERE title LIKE ? LIMIT ? OFFSET ?
	// 现在写在这里只是权宜之计，Step 9 会把它挪到 service 层。
	filtered := make([]model.Post, 0, len(all))
	for _, p := range all {
		if req.Keyword != "" && !strings.Contains(p.Title, req.Keyword) {
			continue
		}
		filtered = append(filtered, p)
	}

	total := len(filtered)
	start := (req.Page - 1) * req.PageSize
	if start > total {
		start = total
	}
	end := start + req.PageSize
	if end > total {
		end = total
	}

	// ③ 写响应。
	//
	// 注意这里用 struct 而不是 gin.H：
	// struct 的字段顺序固定、可被文档化、可被前端类型定义引用、
	// 可被测试直接断言。gin.H 这三样都做不到。
	c.JSON(200, PostListResp{
		List:     filtered[start:end],
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
}

// GetPost 按 slug 返回单篇文章。
//
// 路径：GET /api/v1/posts/:slug
//
// :slug 是路径参数，用 c.Param("slug") 取，名字必须和路由里注册的一致。
// 取不到时返回空字符串——注意它**不会报错**，所以忘记注册参数名会导致
// 查询永远查不到数据，这是个静默失败，排查时优先怀疑这里。
func (h *Handler) GetPost(c *gin.Context) {
	slug := c.Param("slug")

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, p := range h.posts {
		if p.Slug == slug {
			// 顺带演示一下：真实项目里这里会 INCR 浏览量并写 Redis，
			// 而不是每次访问都 UPDATE 数据库（见 Step 8）。
			c.JSON(200, p)
			return
		}
	}

	// 资源不存在返回 404。
	// 不要返回 200 + 空对象：那会让前端无法区分「文章不存在」和
	// 「文章存在但字段都是空的」，也会让监控统计不到 404。
	c.JSON(404, gin.H{"error": "文章不存在"})
}

// CreateComment 接收一条评论。
//
// 路径：POST /api/v1/posts/:slug/comments
//
// 这个方法演示了 POST 接口的标准流程：绑定 → 校验 → 处理 → 返回 201。
func (h *Handler) CreateComment(c *gin.Context) {
	slug := c.Param("slug")

	// ① 绑定请求体。
	//
	// ShouldBindJSON 与 BindJSON 的区别，是必须记住的一点：
	//
	//	BindJSON     绑定失败时，**自动**写入 400 响应并 abort 后续处理
	//	ShouldBindJSON 失败时只返回 error，由你决定怎么响应
	//
	// 为什么推荐 Should？因为它把「怎么响应错误」的控制权留在你手里。
	// 用 Bind 的话，所有参数错误都会返回 Gin 默认的纯文本 400，
	// 和你其他接口的 JSON 格式不一致，前端就得为参数错误单独写一套解析。
	var req model.Comment
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// ② 检查父资源是否存在。
	//
	// 这一步经常被忘掉，后果是：给一个不存在的文章发评论，接口返回 201 成功，
	// 但这条评论永远不会被任何人看到（因为文章都不存在）。
	// 这类「脏数据」在数据库里越积越多，且很难追溯来源。
	h.mu.RLock()
	found := false
	for _, p := range h.posts {
		if p.Slug == slug {
			found = true
			break
		}
	}
	h.mu.RUnlock()

	if !found {
		c.JSON(404, gin.H{"error": "文章不存在"})
		return
	}

	// ③ 处理业务。此处只是分配 ID。
	h.mu.Lock()
	h.seq++
	id := fmt.Sprintf("c%d", h.seq)
	h.mu.Unlock()

	// ④ 返回 201 Created。
	//
	// 为什么创建资源该用 201 而不是 200：
	// 201 让客户端明确知道「产生了一个新资源」，并且可以配合 Location 响应头
	// 告诉它新资源的地址。用 200 的话，客户端无法区分
	// 「提交成功且创建了东西」和「提交成功但什么也没发生」。
	//
	// 注意返回体里 status 固定为 "pending"，而不是接受前端传进来的状态。
	// 这是评论系统的红线：如果前端能指定 status=approved，
	// 那么「先审后发」就形同虚设——攻击者直接提交 approved 即可绕过审核。
	// 凡是由服务端决定的字段，都不能接受客户端输入。
	c.JSON(201, model.CommentResp{
		ID:       id,
		Nickname: req.Nickname,
		Content:  req.Content,
		Status:   "pending",
	})
}

// ---------- 下面是包内的辅助代码，不属于任何处理器 ----------

// ListPostsQuery 是文章列表的查询参数。
//
// 用 struct + form tag 接收查询参数，比逐个 c.Query 的好处是：
//   - 参数集中声明，一眼能看全这个接口支持什么筛选条件
//   - 类型转换与校验交给 Gin，不用手写 strconv.Atoi 和它的错误处理
//   - 将来加参数只改这里，不必翻遍函数体
type ListPostsQuery struct {
	// form tag 对应 URL 里的 ?page=2。
	// binding 不写 required，因为分页参数应该允许缺省。
	Page     int    `form:"page"      binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=50"`
	Keyword  string `form:"keyword"   binding:"omitempty,max=50"`
}

// normalize 把缺省值补上，并给分页参数兜底。
//
// 为什么要有这个函数：分页接口最常见的故障是「用户传 page_size=100000」，
// 如果直接拿去查数据库，会一次性捞出全部数据，把内存和带宽打满。
// 所以上限必须在服务端强制，不能只靠前端传对。
func (q *ListPostsQuery) normalize() {
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.PageSize <= 0 {
		q.PageSize = 10
	}
	// 双保险：binding 的 max=50 只在参数存在时生效，
	// 这里再夹一次，防止将来有人改 tag 时忘了同步逻辑。
	if q.PageSize > 50 {
		q.PageSize = 50
	}
}

// PostListResp 是列表接口的响应结构。
//
// 分页响应最好始终带上 total / page / page_size 三个字段，
// 哪怕前端这次用不上：翻页组件、空状态提示、移动端下拉刷新
// 都需要知道「总共多少条、还有没有下一页」。
type PostListResp struct {
	// List 用 []model.Post 而不是 []any，这样返回空列表时
	// 序列化结果是 [] 而不是 null（前提是切片被初始化为空切片而非 nil，
	// 见 ListPosts 里 make([]model.Post, 0, ...) 的写法）。
	List     []model.Post `json:"list"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}
