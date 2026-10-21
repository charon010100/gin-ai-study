# Step 2：把路由和处理器搬出 main

## 这一步解决了什么问题

Step 1 结束时留下三个问题，这一步解决第一个：

> 所有路由都写在 `main.go` 里，20 个接口就有 300 行，找 `/posts` 要滚半天。

但更重要的是：**这一步建立了整个项目的依赖方向**。这个方向一旦定错，后面越写越乱，
所以值得花时间理解。

## 目录结构

```
step02-handler/
├── main.go                         # 只做装配，不写业务
└── internal/
    ├── model/model.go              # 跨层共享的数据结构
    ├── handler/
    │   ├── post.go                 # 处理器
    │   └── value_vs_pointer.go     # 故意写错的示例（不参与路由）
    └── router/router.go            # URL → 处理器 的映射
```

### 为什么是 `internal/` 目录

`internal` 是 Go 的**编译器强制**规则：只有同一个模块内的代码能 import 它下面的包。
外部模块引用了会直接编译失败。

对一个网站项目来说，这意味着**你的实现细节不会被别人当成 API 依赖**。
将来你做重构（比如把 `handler` 拆成 `handler/public` 和 `handler/admin`），
不需要担心有外部项目已经 import 了旧路径。

这不是风格问题，是 Go 工具链提供的保证——所以「内部代码放 internal」应该成为肌肉记忆。

### 包名用单数（`handler` 而不是 `handlers`）

Go 官方风格建议包名用单数、小写、无下划线。引用时是 `handler.NewHandler()`，
读起来像 `handler.NewHandler` 而不是 `handlers.NewHandler`，更自然。

### 依赖方向：`main → router → handler → model`

```
main.go
   └─> router ──> handler ──> model
                              ↑
                       谁都可以引用它
```

这个方向有两个铁律：

**铁律 1：箭头只能单方向。** 如果 `model` 反过来 import `handler`，就形成循环依赖，
Go 会直接编译失败。这其实是好事——编译器帮你守住了分层。

**铁律 2：`model` 没有任何出边。** 它不 import 项目里的任何包。
这样 `model` 才能被 `handler`、`service`、`repository` 同时引用而不产生依赖问题。

这也是为什么 `Post` 定义在 `model` 而不是 `handler` 里。如果定义在 `handler`，
Step 7 的 `repository` 想用它就得 import `handler`——数据层依赖 HTTP 层，
方向反了。

## 核心陷阱：`gin.Context` 传值还是传指针

这是我想让你记住的第一个真正的坑，因为它**不报错**。

### 错误写法

```go
func BrokenHandler(c gin.Context) {      // 少了星号
    c.JSON(200, gin.H{"message": "hi"})
}
```

### 先纠正一个流传很广的错误说法

网上很多文章说「传值会导致接口没有响应」。**这是错的**（我写这篇时一开始也这么写，
后来查源码才发现不对）。

真实情况是：**这段代码能编译，而且几乎总是能正常返回响应。**

原因在于 `gin.Context` 内部的 `Writer` 字段是一个**接口**：

```go
type Context struct {
    writermem responseWriter   // 值类型，内含 Writer、status 等
    Request   *http.Request
    Writer    ResponseWriter   // 接口
    ...
}
```

按值复制 struct 时，接口字段复制的是**接口头**（类型指针 + 数据指针），
它指向的底层对象是同一个。所以副本里的 `c.JSON(...)` 依然写进了**本次请求真正的
响应缓冲区**，客户端能收到响应。

这就是最危险的地方：**它看起来是对的。**

### 真正的问题：Context 的语义被破坏了

问题不是响应写不出去，而是「中间件和 handler 不再共享同一个上下文」。
有四处会出问题，而且都不会报错。

**1. `c.Next()` 与 `c.Abort()` 失效**

`Context` 内部用 `index` 字段记录「执行到第几个中间件」。副本有自己的 `index`，
在副本里调用 `Next()`，推进的是副本；调用 `Abort()` 也只是把副本标记为中止。

现象：本该在 handler 之后执行的中间件逻辑没跑，或执行顺序莫名其妙。

**2. `c.Set` / `c.Get`——这一条最容易讲错，必须说准确**

很多文章（包括我最初写这份笔记时）会说「传值导致 `c.Set` 的值取不到」。**这不准确。**

`Keys` 是一个 **map（引用类型）**。按值复制 struct 时复制的是 map 头（指针），
所以副本和原件**共享同一个 map**——中间件 `c.Set` 的值，副本里 `c.Get` **大概率还是能取到**。

决定性证据在 gin 自己的 `Copy()` 里。它专门做了克隆：

```go
func (c *Context) Copy() *Context {
    cp := Context{ writermem: c.writermem, Request: c.Request, engine: c.engine }
    cp.writermem.ResponseWriter = nil
    cp.Writer = &cp.writermem
    cp.index = abortIndex
    cp.handlers = nil
    cp.fullPath = c.fullPath

    c.mu.RLock()
    cp.Keys = maps.Clone(c.Keys)     // ← 若裸拷贝不共享 Keys，这行就不必存在
    c.mu.RUnlock()
    ...
}
```

**「能用」不是好消息，而是坏消息**：bug 被掩盖了。真正的问题有两个：

- **边界情况会失灵**：`Set` 里有 `if c.Keys == nil { c.Keys = make(map[any]any) }`。
  如果 `Keys` 还是 `nil` 时在副本上 `Set`，副本会**自己新建**一个 map，
  原件确实看不到——表现为「有时能取到、有时取不到」。
- **更严重：锁失效**。`Context` 内部有 `mu sync.RWMutex`，注释写明 *"This mutex
  protects Keys map"*。按值复制后，副本和原件是**两把不同的锁**，却保护着**同一个 map**。
  并发读写时锁形同虚设 → **data race**，可能出现 `fatal error: concurrent map read and map write`。
  不加 `-race` 跑就完全静默。

**3. `c.Error(err)` 记录的错乱**

`Errors` 是切片，分配在副本自己的字段上：`append` 得到的新切片头写回副本，
原件的 `c.Errors` 长度没变，看不到追加项。

现象：错误被吞掉，日志里查不到，但客户端确实收到了 4xx/5xx。

**4. 复制了 `sync.RWMutex`**

`gin.Context` 在 v1.8+ 内部含锁，复制含锁的类型本身就是错误用法。

这一条有个细节要注意，不然实验会让人困惑：**`go vet` 的 copylocks 检查主要在
「调用处」触发，而不是在函数声明处。** 所以 `value_vs_pointer.go` 里那个
`BrokenHandler` 单独放着（没人调用它）时，`go vet` 可能一声不响。
你需要真的**调用**它，`go vet` 才会报——见下面的动手验证。

### 一句话总结

> 不是「响应没了」，也不是「值全丢了」，而是「**部分语义静默失效、部分语义碰巧还能用，
> 且原本受锁保护的 map 不再受保护**」。

三者混在一起，比彻底坏掉难查得多——因为它不报错。

### 动手验证

**实验 A：看 `c.Next` / `c.Error` 失效**

```go
// 在 router.go 里临时加：
engine.GET("/broken", func(c *gin.Context) {
    c.Set("who", "middleware")
    handler.BrokenHandler(*c)      // 传值
})
// BrokenHandler 内部改成：
//   fmt.Println("who =", c.GetString("who"))   // 多半能打印出 middleware —— 别惊讶
//   c.Error(errors.New("boom"))
//   fmt.Println("copy errors =", len(c.Errors)) // 1（在副本里）
// 然后回到 router 里打印 len(c.Errors)：是 0 —— 错误丢了
```

**实验 B：看 `go vet` 报 copylocks**（必须真的调用，见上面第 4 点）

```bash
go vet ./...
# 期望：call of handler.BrokenHandler copies lock value
```

**实验 C：看数据竞争**（这条最能说明问题）

在 `BrokenHandler` 里起一个 goroutine 反复 `c.Set`，主 goroutine 同时 `c.Get`，
然后用竞争检测跑：

```bash
go run -race .
```

会看到 `DATA RACE` 报在 `Context.Set` / `Context.Get` 上——因为两把锁保护一个 map。

### 规则

> **接收 `gin.Context` 的地方，永远用 `*gin.Context`。**
> 没有例外——包括你自己的辅助函数。

## 另外几个必须理解的决策

### 决策 1：为什么用 `*Handler` 而不是一堆独立函数

```go
type Handler struct {
    mu    sync.RWMutex
    posts []model.Post
}

func (h *Handler) ListPosts(c *gin.Context) { ... }
```

处理器需要访问**共享资源**。现在只是内存里的假数据，Step 7 会变成 `*gorm.DB`，
Step 8 再加上 Redis 客户端。

如果用独立函数，传依赖只能靠包级全局变量：

```go
var db *gorm.DB    // 全局变量

func ListPosts(c *gin.Context) {
    db.Find(...)   // 测试时无法替换成测试库
}
```

后果：
- **无法测试**：跑单元测试会连到真实数据库
- **无法并行**：两个测试用不同配置会互相干扰
- **初始化顺序不可控**：`init()` 和 `main()` 谁先跑容易搞错

用 struct 持有依赖，是最简单的依赖注入：**谁创建这个 struct，谁就决定它用哪个 DB。**

### 决策 2：为什么方法名必须大写（导出）

对比 Step 1，之前的处理函数是闭包，写在 `main` 里，不需要导出：

```go
// Step 1：闭包，同包内使用，小写没问题
r.GET("/ping", func(c *gin.Context) { ... })
```

现在跨包引用，**必须导出**：

```go
// router 包要引用 handler 包的方法
posts.GET("", h.ListPosts)   // ListPosts 首字母必须大写
```

写成 `h.listPosts` 会直接编译失败（`cannot refer to unexported field or method`）。
这算是个「好事」——它不会静默失效。但要注意的是，**导出意味着它成了包的公开 API**，
改名字就是破坏性变更。

### 决策 3：路由里的四个细节

```go
posts := engine.Group(apiVersion + "/posts")
posts.GET("", h.ListPosts)
posts.GET("/:slug", h.GetPost)
posts.POST("/:slug/comments", h.CreateComment)
```

**① 为什么是 `posts` 而不是 `post`（复数）**

REST 惯例：`/posts` 是**集合**，`/posts/:slug` 是**集合中的一项**。
用单数会让「集合」和「单项」的语义混在一起，也无法表达
「对集合做筛选」和「对单项做操作」的区别。

**② 为什么是 `GET("")` 而不是 `GET("/")`**

这个坑很隐蔽：

- `engine.Group("/api/v1/posts")` 后再 `GET("")` → 匹配 `/api/v1/posts`
- `GET("/")` → 匹配 `/api/v1/posts/`（**多个尾斜杠**）

两者是不同的路径。用 `"/"` 的话，客户端请求 `/api/v1/posts`（不带尾斜杠，
这是最常见的写法）会得到 301 重定向或 404。

**③ 分组（`Group`）的价值**

现在只有一个前缀，看起来没必要。但分组有两个实际作用：

- 前缀只写一次。`/api/v1/posts` 后面还有 20 条路由，不分组就要重复 20 次
- **可以在分组上批量挂中间件**：

  ```go
  admin := engine.Group("/api/v1/admin", middleware.RequireLogin())
  // 这一组下面所有路由自动需要登录，不可能漏挂
  ```

  第二个作用才是关键。管理接口漏挂鉴权是最常见的安全事故，
  而分组让「漏挂」在结构上就不可能发生。Step 6 会展开讲。

**④ 为什么是 `/:slug/comments` 而不是 `/comments?post=xxx`**

从属关系应该体现在路径结构里。评论从属于文章，所以文章 slug 出现在评论路径的上层。
用查询参数的话，`/comments?post=` 这种接口无法表达「必须先有这篇文章」的语义，
也容易写出「给不存在的文章发评论」这种脏数据。

### 决策 4：`ShouldBindJSON` 而不是 `BindJSON`

```go
// 推荐
if err := c.ShouldBindJSON(&req); err != nil {
    c.JSON(400, gin.H{"error": err.Error()})
    return
}

// 不推荐
if err := c.BindJSON(&req); err != nil {
    return   // Gin 已经自动写了 400，你无法控制格式
}
```

区别：

| | 绑定失败时 | 响应格式 |
| --- | --- | --- |
| `BindJSON` | **自动**写 400 并 abort | Gin 默认纯文本，你改不了 |
| `ShouldBindJSON` | 只返回 error | 你自己决定 |

用 `Bind` 的后果：所有参数错误都返回纯文本 `400 Bad Request`，
和你其他接口的 JSON 格式不一致，前端得为参数错误单独写一套解析逻辑。

**注意 `binding` tag 只在 Bind/ShouldBind 系列调用时生效。**
自己手写 `json.Unmarshal` 的话，`binding:"required"` 完全不会被检查——
这是一个很常见的误解。

### 决策 5：创建资源返回 201

```go
c.JSON(201, CommentResp{...})
```

- `200` 表示「请求成功」，但没说是否产生了新资源
- `201 Created` 明确表示「**产生了一个新资源**」

用 201 让客户端能区分「提交成功并创建了东西」和「提交成功但什么也没发生」。
严格来说还应该带 `Location` 响应头指向新资源地址。

**顺带一个容易混的**：删除成功通常返回 `204 No Content`，它的含义是
「成功，但没有响应体」。这时候**不能**再写 `c.JSON`——写了会导致
「superfluous response.WriteHeader call」警告，因为 204 不允许有 body。

### 决策 6：请求结构和响应结构分开定义

```go
type Comment struct {          // 请求
    Nickname string
    Content  string
    Email    string
}

type CommentResp struct {      // 响应
    ID       string
    Nickname string
    Content  string
    Status   string
    // 注意：没有 Email
}
```

为什么不用同一个结构？

因为真实项目里 `Comment` 一定会加上 `Email`、`IP`、`UserAgent`、`RiskScore`
这些**不该外泄**的字段。如果请求和响应共用结构，你就必须记得在每个返回点
手动排除它们——只要漏一处，访客邮箱就被公开了。

分开定义之后，泄露不可能发生：**响应结构里根本没有那个字段**。

这比「记得排除」可靠得多。凡是靠「记得」来保证的安全，最终都会出事。

### 决策 7：分页上限必须服务端强制

```go
type ListPostsQuery struct {
    Page     int `form:"page"      binding:"omitempty,min=1"`
    PageSize int `form:"page_size" binding:"omitempty,min=1,max=50"`
}
```

分页接口最常见的故障：客户端传 `page_size=100000`，
服务端直接拿去查数据库，一次性捞出全部数据，把内存和带宽打满。

`binding:"max=50"` 能挡住超大值，但要注意 **`max` 只在参数存在时生效**。
所以 `normalize()` 里还要再夹一次——这不是冗余，是防御性编程：
将来有人改 tag 时逻辑依然安全。

**永远不要相信客户端传来的分页参数。**

### 决策 8：handler 不能写业务逻辑

看 `CreateComment` 里这几行：

```go
h.mu.RLock()
found := false
for _, p := range h.posts {
    if p.Slug == slug { found = true; break }
}
h.mu.RUnlock()
if !found { c.JSON(404, ...); return }
```

这段代码的问题不是「写得不好」，而是**放错了地方**：

- 它包含业务规则（「评论必须属于已存在的文章」）
- 它无法被单元测试直接测试（必须构造 HTTP 请求才能测到）
- 它会在「后台批量导入评论」时被复制一遍（那个场景没有 HTTP 请求）

Step 9 会把它挪到 `service` 层。判断标准：

> 需要知道「HTTP 状态码」或 `gin.Context` 的代码 → 属于 handler
> 只关心业务规则的代码 → 不属于 handler

## 动手实验清单

```bash
cd step02-handler
go mod tidy
go run .
```

```bash
# 列表（默认分页）
curl -s http://localhost:8080/api/v1/posts | jq

# 关键词筛选
curl -s "http://localhost:8080/api/v1/posts?keyword=Redis" | jq

# 测试分页上限：传 999 会被夹到 50
curl -s "http://localhost:8080/api/v1/posts?page_size=999" | jq '.page_size'

# 详情（存在）
curl -s http://localhost:8080/api/v1/posts/gin-context-pointer | jq

# 详情（不存在）→ 应该 404，而不是 200 + 空对象
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/posts/not-exist

# 创建评论（成功）→ 应该 201，且 status 固定为 pending
curl -s -X POST http://localhost:8080/api/v1/posts/gin-context-pointer/comments \
  -H "Content-Type: application/json" \
  -d '{"nickname":"张三","content":"写得很清楚"}' | jq

# 参数校验失败 → 应该 400
curl -s -X POST http://localhost:8080/api/v1/posts/gin-context-pointer/comments \
  -H "Content-Type: application/json" \
  -d '{"nickname":"x","content":""}' | jq

# 给不存在的文章发评论 → 应该 404（而不是 201）
curl -s -X POST http://localhost:8080/api/v1/posts/not-exist/comments \
  -H "Content-Type: application/json" \
  -d '{"nickname":"张三","content":"测试"}' | jq
```

最后别忘了这个，它会指出本文档讲的那个坑：

```bash
go vet ./...
```

## 试一试：绕过「先审后发」

本步代码里 `CreateComment` 固定返回 `status: "pending"`，
并且**不接受**客户端传入的 status。试着让它「可配置」：

```go
// 危险写法：把客户端传的 status 存下来
type Comment struct {
    Nickname string
    Content  string
    Status   string   // ← 加上这个
}
```

然后提交 `{"nickname":"a","content":"b","status":"approved"}`，观察会发生什么。

结论：**凡是由服务端决定的字段，都不能接受客户端输入。**
这就是评论审核功能形同虚设的典型成因，也是安全领域常说的
「不要信任客户端」在具体代码里的样子。

## 这一步的遗留问题

1. **端口 8080 写死。** 换环境要改代码重新编译。
   而且 `Ctrl+C` 会直接切断正在处理的请求。→ **Step 3**
2. **`c.JSON(400, gin.H{"error": err.Error()})` 到处都是。**
   十几处写法略有差异，前端无法统一解析。而且 `err.Error()` 会把
   `Key: 'Comment.Nickname' Error:Field validation for 'Nickname' failed on the 'min' tag`
   这种内部信息直接暴露给用户。→ **Step 4**
3. **`gin.Default()` 的日志格式改不了。** 想加 `request_id` 串联日志、
   想按状态码分级、想排除探活接口的噪音——都做不到。→ **Step 6**
4. **Handler 里混着业务逻辑**（检查文章存在性）。→ **Step 9**
