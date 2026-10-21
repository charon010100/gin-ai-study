# Step 9：完整分层

## 这一步解决了什么问题

走完前八步，功能都有了：能配、能停、格式统一、有中间件、有数据库、有缓存。
但代码的形状有一个问题——**业务逻辑散落在 handler 里**。

回头看 Step 7 的 `CreateComment`：

```go
func (h *Handler) CreateComment(c *gin.Context) {
    // 绑参数（HTTP 的事）
    var req Comment
    c.ShouldBindJSON(&req)

    // 查文章存不存在（业务的事）
    post, err := h.repo.GetPostBySlug(ctx, req.Slug)

    // 判断评论是否关闭（业务的事）
    if !post.AllowComment { ... }

    // 默认 pending（业务的事，还是安全红线）
    comment.Status = "pending"

    // 写库 + 给文章加计数（两件事，必须一起成功）
    h.repo.CreateComment(ctx, comment)
    h.repo.IncrCommentCount(ctx, post.ID, 1)
}
```

四个问题，全是「层次没分开」导致的：

| 问题 | 后果 |
| --- | --- |
| 业务规则写在 handler | 定时任务/CLI 想复用就得复制一遍，或者伪造一个 `gin.Context` |
| 事务散落在两次 repo 调用之间 | 第二个失败时第一个已提交，数据不一致 |
| 错误映射各写一套 | 「找不到」有时 404、有时 500 |
| 依赖硬编码在构造里 | 测试必须连真实数据库和 Redis |

本步把 `handler → service → repository` 拆开，每个问题对应一条决策。

## 目录结构

```
step09-layout/
├── cmd/server/main.go              # 唯一的装配点
├── internal/
│   ├── config/        config.go    # 配置
│   ├── model/         model.go     # 数据结构 + 状态枚举（无出边）
│   ├── repository/                 # 数据访问
│   │   ├── repository.go           # 接口 + 数据层错误
│   │   └── gorm_repo.go            # GORM 实现 + WithTx
│   ├── service/                    # 业务规则
│   │   ├── errors.go               # 业务错误码（不是 HTTP 状态码）
│   │   ├── cache_port.go           # 缓存接口 + NoopCache（降级实现）
│   │   ├── post.go                 # PostService
│   │   └── comment.go              # CommentService
│   ├── handler/                    # HTTP
│   │   ├── errors.go               # 业务错误 → HTTP 状态码（唯一映射点）
│   │   ├── dto.go                  # 请求/响应结构
│   │   └── handler.go              # PostHandler / CommentHandler
│   ├── middleware/    middleware.go
│   ├── router/        router.go     # URL → handler，Deps 结构体
│   └── bootstrap/     db.go         # 连接、迁移、种子数据
└── pkg/
    ├── logger/        logger.go
    └── response/      response.go
```

### 依赖方向（这是全步最该记住的一张图）

```
cmd/server（装配，唯一能 new 所有东西的地方）
      │
      ▼
   router ──► handler ──► service ──► repository（接口）
                              │              │
                              ▼              ▼
                          model          gorm 实现
```

每层**能** import 谁：

| 层 | 可以 import | 禁止 import |
| --- | --- | --- |
| `handler` | service、pkg/* | gorm、repository 的具体实现 |
| `service` | repository 的**接口**、model、cache 抽象 | gin、gorm、net/http |
| `repository` | model、gorm | service、handler |
| `model` | 标准库 | 项目内任何包 |

两条判定规则：

> **`service` 里出现 `gin.Context` → 分层破了。**
> **`handler` 里出现 `*gorm.DB` → 分层破了。**

这两条可以用 grep 立刻检查，比靠代码评审可靠。

## 核心决策

### 决策 1：接口放在 repository 包，而不是「使用方定义」

Go 社区有一种说法：接口应该由**使用方**定义（consumer-side interface），
也就是把 `PostRepo` 定义在 `service` 包里。

这是更"纯粹"的做法，本步**没有**采用，理由：

- 教学项目里接口和实现放一起更好读，能直接对照
- `service` 包会立刻膨胀（每个 service 都要声明一堆它用到的接口）
- 本项目只有一个 repository 实现，接口放在实现侧不会造成实际问题

**什么时候应该改成使用方定义？** 当出现第二个调用方、
或者你发现接口里有 service 根本用不到的方法时。那时把接口搬到使用方，
让每个 service 只声明自己需要的部分（这也是 `TxManager` 单独抽出来的思路）。

### 决策 2：业务错误码 ≠ HTTP 状态码

`service/errors.go` 里定义的是：

```go
type Code string

const (
    CodeNotFound        Code = "NOT_FOUND"
    CodeConflict        Code = "CONFLICT"
    CodeInvalidArgument Code = "INVALID_ARGUMENT"
    CodeForbidden       Code = "FORBIDDEN"
    CodeUnavailable     Code = "UNAVAILABLE"
)
```

**不是 `404`、`409`、`400`。**

为什么？因为 service 不该知道 HTTP 的存在：

- 将来做 gRPC 接口，同一个 service 可以直接复用
- 定时任务调用 service 时，收到一个"404"毫无意义
- 「发生了什么」和「怎么告诉客户端」是两件事，前者属于业务，后者属于协议

映射集中在 `handler/errors.go` 一个函数里：

```go
func mapServiceError(c *gin.Context, err error) {
    var bizErr *service.Error
    if !errors.As(err, &bizErr) {
        response.ServerError(c, err)   // 漏了转换 → 500，且不泄露原文
        return
    }
    switch bizErr.Code {
    case service.CodeNotFound:        response.Fail(c, 404, ..., bizErr.Message)
    case service.CodeConflict:        response.Fail(c, 409, ..., bizErr.Message)
    case service.CodeInvalidArgument: response.Fail(c, 400, ..., bizErr.Message)
    case service.CodeForbidden:       response.Fail(c, 403, ..., bizErr.Message)
    case service.CodeUnavailable:     response.Fail(c, 503, ..., bizErr.Message)
    default:                          response.ServerError(c, bizErr)
    }
}
```

**集中在一处的收益**：你能一眼看完整个 API 的错误语义。
分散在各 handler 里的后果是——三个月后「NOT_FOUND 该返回 404 还是 200」
会有三个不同答案，而新加的错误码总有一处忘了处理，悄悄变成 500。

三个容易搞混的状态码：

- **400 vs 409**：400 是"请求本身不合法"，用户改输入即可；409 是"和现有资源冲突"（slug 已被占用），要用户换标识
- **403 vs 401**：401 是"我不知道你是谁"；403 是"我知道你是谁，但你不能做这个"
- **503 vs 500**：503 是"依赖暂时不可用，可以重试"；500 是"我出 bug 了，重试也没用"

### 决策 3：事务边界在 service，且不把 tx 句柄往下传

这是本步技术上最值得看的一段。评论审核要做两件事：

1. 把评论状态从 `pending` 改成 `approved`
2. 给文章的 `comment_count` +1

这两步**必须一起成功**。少做第 2 步，计数永久偏移；多做了，计数虚高。

`repository` 提供的是「事务能力」，`service` 决定「边界在哪」：

```go
// service/comment.go
func (s *CommentService) Approve(ctx context.Context, id uint64) error {
    return s.tx.WithTx(ctx, func(ctx context.Context) error {
        // ① 改状态（带 from 条件，防止重复审核）
        ok, err := s.comments.UpdateCommentStatus(ctx, id,
            model.CommentPending, model.CommentApproved)
        if err != nil { return err }
        if !ok { return ErrCommentNotFound() }   // 已经被别人处理过

        // ② 加计数 —— 失败会返回 error，① 自动回滚
        return s.posts.IncrCommentCount(ctx, comment.PostID, 1)
    })
}
```

注意两个关键设计：

**① `fn` 接收的是 `ctx`，不是 `*gorm.Tx`。**

```go
WithTx(ctx context.Context, fn func(ctx context.Context) error) error
```

如果签名是 `fn func(tx *gorm.Tx)`，那 `service` 就必须 import gorm——
分层立刻破掉。让事务藏在 `ctx` 里，`service` 完全不知道事务是怎么实现的。

**② 用「带条件的 UPDATE」而不是「先查后改」。**

```go
UpdateCommentStatus(ctx, id, from=pending, to=approved) (bool, error)
// 内部：UPDATE comments SET status='approved' WHERE id=? AND status='pending'
// 返回 RowsAffected > 0 表示真的改到了
```

「先 `SELECT` 判断状态，再 `UPDATE`」在并发下是错的：两个审核请求同时读到
`pending`，然后都执行 `UPDATE`，计数被加了两次。

条件 UPDATE 是原子的——只有一个请求会 `RowsAffected=1`，另一个拿到 `false`，
返回「该评论已不是待审核状态」。

> 这条经验适用于所有「状态机」场景：**用条件更新代替读-判断-写。**

### 决策 4：缓存抽象成接口，并提供 Noop 降级实现

`service/cache_port.go` 定义的是接口，不是 Redis 客户端：

```go
type CachePort interface {
    GetJSON(ctx context.Context, key string, dst any) (bool, error)
    SetJSON(ctx context.Context, key string, val any, ttlSeconds int)
    Del(ctx context.Context, keys ...string)
    DelByPrefix(ctx context.Context, prefix string)
}

// NoopCache 不做任何缓存，永远 miss。
type NoopCache struct{}
```

两个细节值得注意：

- **接口里只有 4 个方法，且都不是 Redis 专有的。** 「最小接口」让假实现几乎零成本；
  一个 300 行的大接口，没人愿意为它写假实现，最后测试就只剩集成测试。
- **`SetJSON` / `Del` 没有返回值。** 这是刻意的：缓存写失败不该让请求失败（fail-open）。
  如果它们返回 error，调用方迟早会写成 `if err := cache.SetJSON(...); err != nil { return err }`，
  于是 Redis 抖一下，用户的请求就 500 了——正好违背了引入缓存的目的。
  失败只记日志、不影响主流程，这才是缓存该有的姿态。
```

`NoopCache` 的用途不是"占位"，而是**降级开关**：

- Redis 连不上时切到 Noop，服务照常可用（只是变慢）
- 单元测试里用 Noop，不需要起 Redis
- 怀疑缓存有问题时，一键关掉缓存来对比行为

这就是 Step 8「缓存故障不该等于服务故障」在架构上的落点：
**把缓存放在接口后面，它才可能被替换掉。**

### 决策 5：装配全在 main，且不用 wire

`cmd/server/main.go` 是唯一出现 `new` 的地方，顺序严格按依赖方向：

```
config → logger → db → seed → cache → repository → service → handler → router → http.Server
```

为什么先手写、不上 `wire`：

- 手写装配是**可读的**，你能一眼看出依赖关系
- 依赖数量（现在 8 个）远没到手写的痛苦阈值
- 引入依赖注入框架后，"某个依赖为什么是 nil"会变成新的排查成本

**什么时候该上 wire：** 当 main 里的装配超过 100 行、且大量重复模板代码时。
在那之前，手写是更好的选择。

注意 `main` 里这段注释：

```go
// store 同时满足 PostRepo、CommentRepo、TxManager 三个接口。
// 每个 service 只声明自己需要的那部分。
postSvc := service.NewPostService(store, cache, cfg.MaxPageSize)
commentSvc := service.NewCommentService(store, store, store)
```

同一个 `store` 被传了三次，看起来重复。但类型系统告诉你的信息是：
`CommentService` 需要「查文章、查/改评论、开事务」三种能力。
它不需要的能力（比如删文章）在类型上根本拿不到。这比传一个大而全的接口更精确。

### 决策 6：`router.Deps` 结构体而不是长参数列表

```go
router.Register(engine, router.Deps{
    Posts:    postH,
    Comments: commentH,
    Log:      lg,
})
```

为什么不用 `Register(engine, postH, commentH, lg)`：

- 每加一个 handler，函数签名就变，所有调用点都要改
- 参数多了以后，调用处一堆同类型的值，容易传错顺序（编译器帮不了你）
- 用结构体以后，加字段不影响任何调用点

这是「参数超过 3 个就考虑用结构体」的一个具体案例。
`router` 这种「会持续变大」的地方收益最明显。

## 动手实验

```bash
cd step09-layout
go mod tidy
go run ./cmd/server
```

### 1. 验证分层：grep 一下就知道

```bash
# service 层不应该出现 gin
grep -rn "gin-gonic" internal/service/    # 期望：无输出

# handler 层不应该出现 gorm
grep -rn "gorm.io" internal/handler/      # 期望：无输出
```

这两条命令就是「分层有没有破」的自动化检查。**把它加到 CI 里**，
比自己记着"下次别这么写"可靠得多。

### 2. 看事务回滚

审核一条不存在的评论：

```bash
curl -s -X PUT http://localhost:8080/api/v1/admin/comments/999999/approve | jq
```

期望 404。此时事务里的两步都没生效——`UpdateCommentStatus` 的
`RowsAffected=0` 触发了 `ErrCommentNotFound()`，`WithTx` 看到 error 就回滚。

### 3. 看错误映射

```bash
# 资源不存在 → 404
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/posts/nope

# slug 冲突 → 409（先建一篇，再用同一个 slug 建一次）
curl -s -X POST http://localhost:8080/api/v1/posts \
  -H 'Content-Type: application/json' \
  -d '{"title":"测试","slug":"dup-test"}' | jq
curl -s -X POST http://localhost:8080/api/v1/posts \
  -H 'Content-Type: application/json' \
  -d '{"title":"测试2","slug":"dup-test"}' | jq
```

第二次应该是 409 而不是 500——这就是「业务错误 → 正确状态码」的具体收益。

### 4. 验证依赖倒置（这步最有价值）

因为 `service` 只依赖 `repository` 的**接口**，你可以在测试里塞个假实现：

```go
type fakePostRepo struct{}

func (fakePostRepo) GetPostBySlug(_ context.Context, slug string) (*model.Post, error) {
    if slug == "missing" {
        return nil, repository.ErrNotFound
    }
    return &model.Post{ID: 1, Slug: slug, Status: model.PostPublished}, nil
}
// ... 补齐接口其他方法

func TestGetBySlug_NotFound(t *testing.T) {
    svc := service.NewPostService(fakePostRepo{}, service.NoopCache{}, 50)
    _, err := svc.GetBySlug(context.Background(), "missing")
    if !errors.Is(err, service.ErrPostNotFound()) {
        t.Fatalf("期望 NOT_FOUND，得到 %v", err)
    }
}
```

**注意这个测试：不连数据库、不连 Redis、不需要 `gin.Context`、毫秒级完成。**
这正是分层的直接回报——如果业务逻辑写在 handler 里，这个测试根本写不出来
（你得构造 HTTP 请求 + 准备数据库）。

## 这一步的遗留问题

1. **后台接口没有鉴权。** `admin` 分组已经建好（结构上留了位置），
   但里面是空的。真实项目需要用户表 + 密码哈希 + session/JWT。→ 本教程不含这一块，
   `blog-backend` 项目里有完整实现。
2. **缓存一致性只做了「删缓存」**，没有处理删除失败的情况
   （比如删缓存时 Redis 挂了）。生产上需要重试或依赖短 TTL 兜底。→ Step 8 提过，未深入
3. **没有测试。** 上面的 `fakePostRepo` 只是演示，没有真的写测试文件。
   分层做完之后写测试的成本最低，这一步最该补。
4. **日志还是 `pkg/logger` 的简易封装**，不是 zap/slog 的结构化 JSON。
   → Step 10 用 `slog` 演示了生产级日志
5. **没有 metrics / tracing。** 出了问题只能靠日志 grep。
