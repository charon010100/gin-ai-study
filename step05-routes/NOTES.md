# Step 5：路由分组、静态资源与页面渲染

## 这一步解决了什么问题

前面的路由都扁平地写在 `router.go` 里。本步处理路由的**结构性问题**——
它们看起来枯燥，但出错时最难查，因为症状往往是「某个接口莫名其妙 404」。

四个主题：

1. 分组与嵌套（以及为什么分组本质上是**安全**机制）
2. 静态路由与参数路由的优先级
3. 路由冲突：**启动就 panic**，不是运行时出错
4. 静态资源与 HTML 模板（用 embed，不用相对路径）

到这一步，项目才算真的能叫「网站」——它既能返回 JSON，也能渲染页面。

## 目录结构

```
step05-routes/
├── main.go                    # embed 模板与静态资源、启动
├── demo_conflict.go           # 单独运行，看路由冲突的 panic
├── configs/config.toml
├── internal/
│   ├── config/config.go
│   ├── handler/handler.go     # HTML 页面 + JSON 接口
│   ├── model/model.go
│   └── router/router.go       # 全部路由结构
└── web/
    ├── templates/{index,post,404}.html
    └── static/style.css
```

## 核心决策

### 决策 1：分组是安全问题，不是风格问题

先看两种写法的对比。

**扁平写法（危险）：**

```go
engine.DELETE("/api/v1/admin/posts/:slug", adminOnly(), h.DeletePost)
engine.GET("/api/v1/admin/stats", h.Stats)          // ← 忘了加 adminOnly
```

**分组写法（安全）：**

```go
admin := engine.Group("/api/v1/admin", adminOnly())
{
    admin.DELETE("/posts/:slug", h.DeletePost)
    admin.GET("/stats", h.Stats)                     // 自动带鉴权
}
```

第二种写法里，「漏挂鉴权」在**结构上不可能发生**。而第一种写法下，
每新增一个管理接口都是一次赌博：你有没有记得加中间件？

这类漏洞在代码 review 里极难发现，因为每一行长得都差不多。
真实项目里，管理接口漏鉴权是最常见的高危漏洞之一。

> **如果一个安全约束可以靠「结构」保证，就绝不要靠「自觉」。**

验证一下：

```bash
# 不带凭证 → 401
curl -i http://localhost:8080/api/v1/admin/stats

# 带凭证 → 200
curl -i -H "X-Admin-Token: demo" http://localhost:8080/api/v1/admin/stats
```

### 决策 2：静态路由与参数路由同级共存——**先验证，再依赖**

```go
posts.GET("/hot", h.Hot)       // 静态
posts.GET("/:slug", h.GetPost) // 参数
```

**期望行为**：请求 `/api/v1/posts/hot` 命中第一条，不会被当作 `slug="hot"`。

gin 的路由树是自研的 radix tree，设计上静态分支优先于参数分支
（这点与历史上著名的 `httprouter` 不同——后者遇到这种组合会直接 panic）。

**但我要诚实说明：我没有在本环境里实测过这个行为**（写这份教程时 shell 被策略
限制，无法 `go run`）。不同 gin 版本的路由实现有过差异，历史上有版本会在这里 panic。

所以请你自己先跑一下确认：

```bash
curl -s http://localhost:8080/api/v1/posts/hot | jq
```

- 返回热门文章列表 → 静态优先，正常
- 返回 `{"code":4001,"message":"资源不存在"}` → 被 `:slug` 吃掉了
- **服务启动直接 panic** → 你这个版本不支持共存，会看到类似
  `'hot' in new path ... conflicts with existing wildcard ':slug'`

**如果出现后两种情况，怎么改？** 把静态路径挪到参数同级之外，彻底回避这个问题：

```go
posts.GET("/:slug", h.GetPost)     // /api/v1/posts/:slug
api.GET("/hot-posts", h.Hot)       // /api/v1/hot-posts
```

这也是我在**生产项目里更推荐的做法**：不是所有"能跑"的路由设计都值得依赖。
一个不依赖路由树实现细节的 URL 结构，升级 gin 时不会给你惊喜。

> 一条经验：**凡是"我印象里应该可以"的路由行为，都用一条 curl 验证一遍。**
> 路由 bug 的症状通常是"某个接口莫名 404"，排查方向容易跑偏到业务代码上。

### 决策 3：路由冲突在启动时 panic

单独跑这个文件，亲眼看一看：

```bash
go run demo_conflict.go
```

输出：

```
panic: ':id' in new path '/posts/:id' conflicts with existing wildcard
       ':slug' in existing prefix '/posts/:slug'
```

原因是：**同一个位置不能有两个不同名字的参数**。
`/posts/:slug` 和 `/posts/:id` 都会匹配 `/posts/123`，
路由树无法决定把 `123` 赋给 `slug` 还是 `id`。

**为什么这是好事？** 因为它在**启动时**就 panic，而不是等某个请求进来才出错。
一个有路由冲突的服务根本起不来，就不可能上线。

这类「宁愿起不来也别带病运行」的设计，和 Step 3 的「配置校验失败即退出」
是同一个思路。

> 记一条规则：**同一前缀下，同一层级只能有一个参数名。**
> 需要区分时改成 `/posts/:slug` 和 `/users/:id`（不同前缀）。

### 决策 4：两类路由的 404 必须分开处理

```go
engine.NoRoute(func(c *gin.Context) {
    if strings.HasPrefix(c.Request.URL.Path, "/api/") {
        c.JSON(404, gin.H{"code": 4001, "message": "接口不存在"})
        return
    }
    c.HTML(404, "404.html", gin.H{"Title": "页面不存在"})
})
```

为什么不能统一？因为两类客户端完全不同：

| 客户端 | 期望 | 给错了会怎样 |
| --- | --- | --- |
| 浏览器 | HTML 页面 | 页面上显示一坨 JSON，用户懵 |
| 前端请求（fetch） | JSON | `JSON.parse("<html>...")` 直接抛异常 |

判断依据是路径前缀，不是 `Accept` 头。

**为什么不用 `Accept` 头？** 理论上更"正确"，但实践上不可靠：
很多客户端（curl、部分 SDK、移动端 HTTP 库）默认的 `Accept` 是 `*/*`，
你无法据此判断。而 URL 前缀是你自己设计的，100% 可控。

### 决策 5：模板和静态资源用 embed，而不是相对路径

```go
//go:embed web/templates/*.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS
```

对比一下常见写法 `engine.LoadHTMLGlob("web/templates/*")` 的问题：

它是**相对路径**，依赖进程的当前工作目录。于是会出现：

| 启动方式 | 工作目录 | 结果 |
| --- | --- | --- |
| 在项目里 `go run .` | 项目根 | 正常 |
| systemd 服务 | `/`（默认） | 找不到模板，首页 500 |
| Docker `WORKDIR /app` + 编译产物在别处 | 不确定 | 500 |

这就是经典的「本地能跑，线上报错」。

用 embed 之后，模板和静态文件**成为二进制的一部分**，与工作目录无关。
代价是改模板要重新编译——对内容经常变的项目，可以加个开关：
开发用磁盘路径（热更新），生产用 embed（稳定）。

**顺带一个细节**：`engine.StaticFS` 内部用 `http.FileServer`，
它自动处理 `If-Modified-Since`、`Range` 请求等细节。
自己手写静态文件处理很容易漏掉这些，表现为「大文件下载到一半失败」
或者「改了 CSS 但浏览器一直用旧的」。

### 决策 6：模板名是文件名，不是路径

```go
tmpl := template.New("")
tmpl.ParseFS(templateFS, "web/templates/*.html")
engine.SetHTMLTemplate(tmpl)

// 渲染时用 "index.html"，不是 "web/templates/index.html"
c.HTML(200, "index.html", data)
```

用错会报 `html/template: "index.html" is undefined`。这个错误信息其实很明确，
但新手常因为「路径写对了呀」而卡住。

### 决策 7：尾斜杠会被 301 重定向

```go
posts.GET("", h.ListPosts)   // 匹配 /api/v1/posts
```

如果客户端请求 `/api/v1/posts/`（多一个斜杠），gin 默认会返回 **301** 重定向到
`/api/v1/posts`。

这看起来无害，但有个实际影响：**301 会被浏览器永久缓存**。
如果你后来把接口改成了 `/api/v1/posts/`，老用户的浏览器会一直跳到旧地址，
而且你无法清除这个缓存（只能让用户清缓存或换路径）。

所以在设计接口时就要定好尾斜杠规范，并且**不要中途改**。

## 动手实验

```bash
cd step05-routes
go mod tidy
go run .
```

### 1. 首页是渲染出来的 HTML

浏览器打开 <http://localhost:8080/>，你会看到一个正常排版的页面，
CSS 来自 `/static/style.css`。

**确认它真的不是静态文件**：查看页面源码，文章列表是模板渲染进去的。

### 2. 同一个资源的两种表现形式

```bash
# HTML 页面
curl -s http://localhost:8080/posts/gin-context-pointer | head -20

# 同一个文章的 JSON
curl -s http://localhost:8080/api/v1/posts/gin-context-pointer | jq
```

### 3. 静态路由与参数路由共存（**必须先验证**）

```bash
curl -s http://localhost:8080/api/v1/posts/hot | jq
```

三种可能的结果，分别说明你的 gin 版本的行为：

| 你看到的 | 含义 | 该怎么做 |
| --- | --- | --- |
| 热门文章列表 | 静态优先，符合预期 | 可以依赖，但升级 gin 时复查一次 |
| `{"code":4001,...}`（说 slug=hot 不存在） | 参数路由赢了 | 把 `/hot` 挪到 `/api/v1/hot-posts` |
| 启动时 panic | 你这版不支持共存 | 同上 |

这一条实验的意义不在于"验证 gin 对不对"，而在于养成习惯：
**路由行为不要凭印象，跑一条 curl 就清楚了。**

### 4. 分组鉴权

```bash
# 无凭证 → 401
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/admin/stats

# 有凭证 → 200
curl -s -H "X-Admin-Token: demo" http://localhost:8080/api/v1/admin/stats | jq
```

### 5. 两类 404

```bash
# API 的 404 → JSON
curl -s http://localhost:8080/api/v1/nope | jq

# 页面的 404 → HTML
curl -s -i http://localhost:8080/nope | head -5
```

### 6. 亲眼看到路由冲突的 panic

```bash
go run demo_conflict.go
```

### 7. 尾斜杠 301

```bash
curl -s -o /dev/null -w "%{http_code} -> %{redirect_url}\n" http://localhost:8080/api/v1/posts/
# 期望: 301 -> http://localhost:8080/api/v1/posts
```

## 这一步的遗留问题

1. **`adminOnly` 还写在 router 包里**，而且它返回的 JSON 格式和 Step 4 的
   `response` 包不一致（这里手写 `{"code":2001,...}`）。
   中间件应该独立成包，并用统一的响应工具。→ **Step 6**
2. **`gin.Default()` 的日志无法定制。** 没有 `request_id`，无法把一次请求的多条日志串联；
   探活接口的高频日志也没有过滤。→ **Step 6**
3. **数据仍在内存里**，重启即丢。→ **Step 7**
4. **HTML 页面的 SEO 基础没做**（`<meta description>`、`sitemap.xml`、RSS）。
   对个人博客来说这些很重要，但属于业务层面，不在本教程主线内。
