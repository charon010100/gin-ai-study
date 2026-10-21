# Step 4：统一响应体与错误处理

## 这一步解决了什么问题

Step 3 之后服务能配、能停、有超时了，但每个 handler 里还是这样：

```go
c.JSON(400, gin.H{"error": err.Error()})
c.JSON(404, gin.H{"error": "文章不存在"})
c.JSON(200, gin.H{"list": ..., "total": ...})
```

三个问题，按严重程度排：

| 问题 | 后果 | 严重度 |
| --- | --- | --- |
| `err.Error()` 直接外泄 | 结构体名、字段名、校验规则全被看见 | 中 |
| 500 时泄露 SQL 与连接串 | 攻击者拿到表结构和内网地址 | **高** |
| 响应格式不一致 | 前端每个接口写一套解析 | 中 |

第三条最烦，但第二条最危险。本步用一个 `pkg/response` 把三者一起解决。

## 目录结构

```
step04-response/
├── main.go
├── configs/config.toml
├── internal/
│   ├── config/config.go
│   ├── model/model.go
│   ├── handler/
│   │   ├── post.go        # 处理器
│   │   └── bind.go        # 绑定错误 → 人话
│   └── router/router.go
└── pkg/response/
    └── response.go        # 统一响应体 + 错误码 + AppError
```

`pkg/` 和 `internal/` 的区别：`pkg` 表示「可以被外部复用」，`internal` 表示「只在本模块内」。
`response` 放 `pkg` 是因为它只依赖 gin 和标准库，将来抽成公共库不需要搬别的东西。

## 核心决策

### 决策 1：为什么响应要有 `code` 字段，HTTP 状态码不够吗

**不够，而且两个都不能少。**

HTTP 状态码只有几十个，而业务错误远多于此。「文章不存在」和「评论不存在」都是 404，
前端如果想分别提示「去草稿箱看看」和「刷新页面」，只能靠 `code` 区分。

但只有 `code` 也不行，因为中间层不理解你的业务：

| 中间层 | 依赖什么 | 行为 |
| --- | --- | --- |
| 监控/告警 | 状态码 | 5xx 比例上升 → 报警 |
| 负载均衡 | 状态码 | 5xx → 摘除实例 |
| CDN | 状态码 | 404 → 可以缓存 |
| 浏览器/客户端 | 状态码 | 401 → 跳登录页 |
| 前端业务代码 | `code` | 1001 → 高亮表单字段 |

**结论：状态码给基础设施看，`code` 给业务代码看，两者都要。**

### 决策 2：`Message` 和 `Err` 为什么必须是两个字段

```go
type AppError struct {
    HTTPStatus int
    Code       int
    Message    string   // 进响应体，用户能看到
    Err        error    // 只进日志，永不外泄
}
```

这是整个包最重要的设计。它的价值在于：**让「泄露」变成不可能，而不是靠自觉**。

如果只有一个 `error` 字段，每个 handler 都得自己判断「这个错误能不能给用户看」。
只要有一个人写了：

```go
c.JSON(500, gin.H{"error": err.Error()})
```

数据库地址就跑到用户屏幕上了。而有了 `AppError` 之后：

- `Fail()` 只读 `Message`
- `Err` 只在 `logIfNeeded` 里被使用

**默认行为是安全的**，不需要任何人记得什么。

验证一下：访问 `GET /api/v1/posts/boom`，这个接口内部返回了一个假的连接失败错误
（含 SQL 语句）。你会看到：

- 客户端收到 `{"code":5002,"message":"数据操作失败","data":null}`
- 服务端日志里有完整的 `dial tcp 127.0.0.1:3306: connection refused; sql: SELECT ...`

### 决策 3：`Wrap` 为什么返回新对象而不是改自己

```go
var ErrNotFound = New(404, CodeNotFound, "资源不存在")

// 如果这样写：
func (e *AppError) Wrap(err error) *AppError {
    e.Err = err        // ← 危险
    return e
}
```

后果：`ErrNotFound` 是**包级共享变量**。一次请求的错误会粘在它上面，
之后所有返回 `ErrNotFound` 的请求都会带上那个错误。

表现：日志里出现「资源不存在: post not found: xxx」，但这次请求的 slug 其实是 `yyy`。
多 goroutine 下极难复现，也极难定位。

所以 `Wrap` 必须返回新的 `*AppError`。

### 决策 4：为什么 4xx 打 WARN、5xx 才打 ERROR

因为 4xx 是**正常业务流转**：用户输错密码、请求不存在的文章、参数填错。
这类请求每天可能比成功请求还多。

如果都记成 ERROR：

- 日志被噪音淹没，真正的故障（数据库连不上）看不见
- 告警系统因为误报太多而被无视（这叫告警疲劳）

**只有 5xx 才意味着「服务端自己出了问题」，才值得惊醒你。**

### 决策 5：`AbortWithStatusJSON` 而不是 `JSON`

`Fail` 里用的是 `AbortWithStatusJSON`，它比 `JSON` 多做一件事：**调用 `c.Abort()`**，
把当前请求的中间件链标记为已结束，后面的 handler 不再执行。

本步还没有中间件，所以看不出差别。Step 6 会看到实际效果。

但即使现在，它也能救你一次：**如果你忘了写 `return`**。

### 决策 6：`data` 为 null 也要输出

```json
{"code":4001,"message":"资源不存在","data":null}
```

而不是省略 `data` 字段。

理由：省略的话，前端拿到的是 `resp.data === undefined`；输出 null 则是 `resp.data === null`。
两者在 JS 里行为不同（`"data" in resp` 也不同），会导致「有时报错有时不报错」的怪现象。

**统一输出 `data`，让前端只需处理一种情况。**

## 两个必踩的坑（都在本步代码里涉及）

### 坑 1：`c.JSON` 之后忘记 `return`

```go
func bad(c *gin.Context) {
    if err != nil {
        response.Fail(c, err)
        // ← 这里少了个 return
    }
    // 代码继续往下跑，又写了一次响应
    response.OK(c, data)
}
```

结果：

```
[WARNING] Headers were already written. Wanted to override status code 400 with 200
```

客户端收到的是**两段 JSON 拼接**：

```json
{"code":4001,"message":"资源不存在","data":null}{"code":0,"message":"ok","data":...}
```

前端 `JSON.parse` 直接抛异常，而且错误信息完全指向别处（看起来像"接口返回了乱码"）。

**规则：每个 `response.Fail` / `response.OK` 之后都要 `return`。**
`Abort` 只能阻止后续的中间件，**不能阻止当前函数继续执行**。

### 坑 2：请求体只能绑定一次

```go
var a model.Comment
c.ShouldBindJSON(&a)      // 读走 body

var b model.Comment
c.ShouldBindJSON(&b)      // ← EOF，必然失败
```

请求体是**流**，读一次就空了。需要同时校验和取值，必须绑定到同一个变量。

这个坑我写本步代码时真的踩了一次——最早写成「先绑一个空结构体做校验，再绑一次拿数据」，
第二次绑定直接 EOF。

## 动手实验

```bash
cd step04-response
go mod tidy
go run .
```

### 1. 统一格式

```bash
# 成功
curl -s http://localhost:8080/api/v1/posts | jq
# {"code":0,"message":"ok","data":{"list":[...],"total":3,"page":1,"page_size":10,"total_page":1}}

# 不存在
curl -s http://localhost:8080/api/v1/posts/nope | jq
# {"code":4001,"message":"资源不存在","data":null}
```

### 2. 内部错误不外泄（本步最重要的实验）

```bash
curl -s http://localhost:8080/api/v1/posts/boom | jq
```

响应里**只能**看到「数据操作失败」，没有任何 SQL 或连接串。
然后回头看服务端的终端输出——完整的内部错误在那里：

```
[ERROR] GET /api/v1/posts/boom -> 500/5002: dial tcp 127.0.0.1:3306: connect: connection refused; sql: SELECT ...
```

**这就是「客户端看人话，日志看真相」。**

### 3. 参数错误变得可读

```bash
curl -s -X POST http://localhost:8080/api/v1/posts/gin-context-pointer/comments \
  -H "Content-Type: application/json" \
  -d '{"nickname":"x","content":""}' | jq
```

对比 Step 2 的输出：

| Step 2 | Step 4 |
| --- | --- |
| `Key: 'Comment.Nickname' Error:Field validation for 'Nickname' failed on the 'min' tag` | `nickname 长度不能少于 2 个字符` |

### 4. 404 也是 JSON

```bash
curl -s -i http://localhost:8080/api/v1/not-exist | head -5
```

不注册 `NoRoute` 的话，这里返回的是纯文本 `404 page not found`，
和你的 JSON 格式完全不一致。前端统一按 JSON 解析时会在这里崩掉。

### 5. 亲眼看到"忘记 return"的后果

临时把 `GetPost` 里的某处 `return` 删掉，然后访问对应路径，观察：

- 服务端打印 `[WARNING] Headers were already written`
- 响应体变成两段 JSON 黏在一起

（实验完记得改回来。）

## 一组容易搞混的状态码

| 状态码 | 含义 | 什么时候用 |
| --- | --- | --- |
| 200 | 成功 | 查询、更新成功 |
| 201 | 已创建 | POST 创建了资源，建议带 `Location` 头 |
| 204 | 成功但无内容 | DELETE 成功。**注意不能有响应体**，写 `c.JSON` 会告警 |
| 400 | 请求格式错 | JSON 语法错、缺少必填字段 |
| 401 | 未认证 | 没登录、token 失效 |
| 403 | 已认证但无权 | 登录了但不是作者 |
| 404 | 资源不存在 | |
| 409 | 状态冲突 | 唯一键重复、版本冲突 |
| 429 | 请求过频 | 限流 |
| 500 | 服务端故障 | 自己的 bug、依赖挂了 |

最容易混的两个：
- **401 vs 403**：401 是「你是谁我不知道」，403 是「我知道你是谁，但你不能做这个」
- **400 vs 422**：400 是请求都解析不了，422 是解析成功但语义不合法。本教学项目统一用 400

## 这一步的遗留问题

1. **`gin.Default()` 的日志无法定制。** 想给每个请求加 `request_id` 把多条日志串起来、
   想按状态码分级、想过滤掉探活接口的噪音——都做不到。→ **Step 6**
2. **没有鉴权。** 将来所有 `/api/v1/admin/**` 接口都没有任何登录校验，
   而且现在也看不出「哪些接口漏挂了」。→ **Step 5、Step 6**
3. **数据在内存里。** 重启就没了。→ **Step 7**
4. **`bind.go` 里字段名的转换不严谨。** `strings.ToLower("PageSize")` 得到 `pagesize`，
   而真实参数名是 `page_size`。要严格一致得反射读 json/form tag，
   或者用 validator 官方的 `universal-translator`。教学项目从简，但生产应该做对。
