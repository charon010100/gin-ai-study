# 踩坑速查表

> 把十步里最容易咬人的问题集中在一页。每条给出：**现象 → 原因 → 怎么查**。
>
> 用法：遇到诡异问题时先来这里扫一眼，比从头读 NOTES 快。

---

## 一、路由与处理器

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| 接口返回 200 但 body 为空，或 body 是两段 JSON 拼接 | `c.JSON` 之后忘了 `return`，响应被写了两次 | 服务端日志出现 `superfluous response.WriteHeader call` | 4 |
| 某个接口莫名 404，其它都正常 | 路由被参数路由吃掉（`/hot` 被 `/:slug` 匹配） | 启动日志里的路由表；`curl` 单独试 | 5 |
| `GET /posts` 404 但 `GET /posts/` 正常 | 注册成了 `GET("/")` 而不是 `GET("")` | 检查路由注册字符串 | 2 |
| 服务启动直接 panic | 同一前缀下有两个不同名的参数（`/posts/:slug` 和 `/posts/:id`） | panic 信息里写明了冲突的两条路径 | 5 |
| `c.Next()` 不推进、`c.Error` 的记录丢失 | 下游用**值**接收 `gin.Context`（少个星号） | `go vet`（需真正调用）；改成 `*gin.Context` | 2 |
| 偶发 `concurrent map read and map write` | 同上——副本与原件的锁各管一半，却共享 `Keys` map | `go run -race` 可定位 | 2 |
| 中间件改动对某些路由不生效 | `engine.Use` 写在了路由注册**之后** | 把所有 `Use` 移到 `Register` 之前 | 6 |

## 二、请求绑定

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| 第二次绑定返回 `EOF` | 请求体是流，只能读一次 | 一个 handler 里只 `ShouldBind` 一次 | 4 |
| `binding:"required"` 完全没生效 | 自己写了 `json.Unmarshal`，没走 Gin 的绑定 | 改用 `ShouldBindJSON` | 2 |
| 参数错误返回纯文本 `400 Bad Request` | 用了 `BindJSON`（自动写响应且格式不可控） | 换成 `ShouldBindJSON` | 2 |
| 用户看到 `Field validation for 'X' failed` | 直接把 `err.Error()` 返回给了客户端 | 翻译成可读文案，原始错误只进日志 | 4 |

## 三、中间件

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| 耗时日志永远是 0 | 把 `time.Since(start)` 写在了 `c.Next()` 之前 | 计时必须在 `Next` 之后 | 6 |
| 鉴权失败后 handler 照样执行了 | 只写了响应，没 `Abort`；或 `Abort` 后没 `return` | 两者都要 | 6 |
| panic 会导致进程退出 | 没有 Recovery 中间件（`gin.New()` 不带你） | 加 `gin.Recovery()` 或自建 | 6 |
| 堆栈出现在客户端响应里 | 用了默认 `gin.Recovery()` | 自建 Recovery，堆栈只进日志 | 6 |
| 中间件里起的 goroutine panic 崩了整个服务 | panic 只能被**同一 goroutine** 的 recover 捕获 | 在 goroutine 内部 `defer recover` | 6 |
| 日志里出现伪造的错误行 | 直接信任了客户端传来的 `X-Request-ID` 写进日志 | 白名单校验字符集与长度 | 6 |

## 四、CORS

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| 浏览器报「已被 CORS 策略阻止」，但配置看起来没错 | 配置里来源末尾多了 `/`，与 `Origin` 头不相等 | 统一去尾斜杠后比较 | 6 |
| 带 Cookie 的请求全部跨域失败 | `Allow-Origin: *` 与 credentials 不能共存 | 回显具体 Origin | 6 |
| 跨域「有时候成功有时候失败」 | 少了 `Vary: Origin`，被 CDN 缓存串了 | 加上 `Vary` | 6 |
| OPTIONS 请求 404 | 预检请求不会进 handler，需中间件直接返回 204 | 在 CORS 中间件里处理 OPTIONS | 6 |

## 五、优雅关停与超时

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| `Ctrl+C` 后在途请求失败 / 客户端连接重置 | 用了 `r.Run()`，没有关停逻辑 | 自建 `http.Server` + `Shutdown` | 3 |
| `Shutdown` 立刻返回，没等请求 | 复用了已取消的 context | 新建 `context.WithTimeout(Background(), ...)` | 3 |
| 每次正常停止都打印一条吓人的错误 | 没排除 `http.ErrServerClosed` | 用 `errors.Is` 判断后忽略 | 3 |
| 某个慢接口总是失败 | `WriteTimeout` 小于该接口耗时（它包含 handler 时间） | 调大 `WriteTimeout` 或优化接口 | 3 |
| 连接数缓慢涨到打满 | `http.Server` 零值 = 没有任何超时 | 设 `ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout` | 3 |
| 部署后几秒内接口 500 | 服务还在接流量时依赖没就绪 | 引入 `/readyz` 与两阶段下线 | 10 |

## 六、限流与状态

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| 多实例部署后限流失效（实际放行 N 倍） | 计数在进程内存里 | 状态必须放共享存储（Redis） | 6→8 |
| 限流「边界」处能打两倍请求 | 用了固定窗口计数 | 改令牌桶或滑动窗口 | 6 |
| 重启后登录态全失效 | session 存在内存里 | 移到 Redis 并设 TTL | 6→8 |

## 七、数据层（Step 7–9）

| 现象 | 原因 | 怎么查 | 步 |
| --- | --- | --- | --- |
| 软删除的数据仍然出现在列表里 | 软删除字段没用 `gorm.DeletedAt` 类型 | 检查模型定义；`Unscoped()` 才能查到已删除 | 7 |
| 「记录不存在」被当成 500 | `err != nil` 一刀切，没判 `gorm.ErrRecordNotFound` | 用 `errors.Is(err, gorm.ErrRecordNotFound)` | 7 |
| 发版后字段数据错乱/丢失 | 用 `AutoMigrate` 改了生产表结构 | 生产用版本化 SQL 迁移 | 7 |
| 缓存改了但页面还是旧数据 | 更新 DB 后「更新缓存」而非「删缓存」 | 改为删除缓存 | 8 |
| 冷启动时所有请求 500 | 把 `redis.Nil` 当成错误 | `errors.Is(err, redis.Nil)` 表示未命中 | 8 |
| Redis 挂了导致全站 500 | 缓存被当成强依赖 | 缓存故障要降级到 DB | 8 |
| 缓存集体过期，DB 被打穿 | TTL 完全相同 | TTL 加随机抖动 | 8 |
| 列表页慢查询 | `WHERE status=? ORDER BY created_at` 没走索引 | 建复合索引并 `EXPLAIN` | 7 |
| 「找不到」有时 404、有时 500 | 错误映射散落在各 handler | 集中成一个 `mapServiceError` | 9 |
| 两次写库之间失败，数据不一致 | 事务边界没定在 service | `WithTx(ctx, fn)` 包住 | 9 |
| 并发审核把计数加了两次 | 「先查后改」不是原子的 | 改「带条件的 UPDATE」看 RowsAffected | 9 |
| `service` 里出现 `gin.Context` | 分层被破坏 | grep 一下即可发现 | 9 |

---

## 十条通用规则

1. **`gin.Context` 永远传指针。** 没有例外。
2. **每个写响应的语句后面都要 `return`。** `Abort` 不阻止当前函数继续跑。
3. **中间件顺序 = `Use` 顺序**，`Recovery` 必须最外层。
4. **所有 `Use` 必须在路由注册之前。**
5. **由服务端决定的字段，绝不接受客户端输入**（评论状态、用户 ID、浏览量）。
6. **内部错误只进日志，不进响应体。**
7. **状态放在进程内存里 → 只能单实例。** 要扩展就得挪到共享存储。
8. **请求体只能读一次。**
9. **依赖外部服务的代码必须有降级路径。**
10. **凡是"我印象里应该可以"的行为，跑一条 curl 验证一遍。**
