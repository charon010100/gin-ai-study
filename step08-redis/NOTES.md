# Step 8：接入 Redis（缓存、浏览量、限流）

## 这一步解决了什么问题

Step 7 留下的三个问题：

1. **每次访问详情都全量查库**。热门文章被反复读取，数据库一次都没省下。
2. **浏览量统计不了**。`views` 字段永远是 0，而「每次访问都 `UPDATE`」会把一个只读接口变成写接口 —— 热门文章被刷时，一篇 3000 字的文章能产生上万个写事务。
3. **没有防刷手段**。评论接口可以无限提交。

但这一步真正难的不是「怎么用 go-redis」，而是三个「出错时怎么办」：

- Redis 里没有这个 key —— 这是**正常**还是**故障**？
- Redis 连不上 —— 全站 500，还是降级继续跑？
- 想在进程内记限流计数 —— 部署两个实例会怎样？

## 目录结构

```
step08-redis/
├── main.go                              # 装配：DB → 仓储 → 装饰器 → handler
├── go.mod
├── .gitignore
└── internal/
    ├── model/model.go                   # 与 Step 7 相同
    ├── repository/
    │   ├── repository.go                # GORM 实现（含 IncrPostViews）
    │   └── cached.go                    # Cache-Aside 装饰器
    ├── cache/
    │   ├── client.go                    # Redis 封装（永不抛「不可用」错误）
    │   ├── jitter.go                    # TTL 抖动
    │   ├── views.go                     # 浏览量计数 + 批量回写
    │   └── ratelimit.go                 # 固定窗口限流算法（不含 gin）
    ├── ratelimit/ratelimit.go           # 限流的 Gin 中间件
    ├── bootstrap/db.go                  # 连接与种子数据
    ├── handler/post.go, dto.go
    └── router/router.go
```

注意 `cache/ratelimit.go` 与 `ratelimit/ratelimit.go` 的分工：
前者是**算法**，后者是**HTTP 适配**。缓存层不 import gin。

## 启动 Redis

```bash
docker run -d --name blog-redis -p 6379:6379 redis:7-alpine
```

不装 Docker 也行 —— 本步支持降级模式，见实验 5。

```bash
cd step08-redis
go mod tidy
go run .
```

## 核心决策

### 决策 1：`redis.Nil` 是「未命中」，不是「故障」

**方案**：用 `errors.Is(err, redis.Nil)` 单独判断，未命中时返回 `(false, nil)` 给上层。

**理由**：`redis.Nil` 是 go-redis 表示「键不存在」的哨兵错误。它实现了 `error` 接口，但语义上完全正常 —— 缓存是空的，仅此而已。

**不这样写会出什么问题**：

```go
val, err := rdb.Get(ctx, key).Result()
if err != nil {
    return nil, err          // ← 灾难
}
```

后果是**服务刚上线那段时间所有请求都 500**：

- 缓存一开始是空的，每次查询都未命中
- 每次未命中都返回错误 → handler 映射成 500
- 等有请求成功回填了缓存，才逐渐恢复正常

现象是「刚发布完一片 5xx，过几分钟自己好了」。如果你从没想过这个可能，会去查数据库、查网络、查发布流程 —— 全都查不到东西。

同样危险的是把所有错误都当未命中：

```go
if err != nil {
    return nil, nil     // ← 反向错误：数据库连不上也当成"没数据"
}
```

这样 Redis 挂了会「看起来一切正常」，只是每次都回源；如果恰好 DB 也挂了，你会收到一堆 200 + 空数据，而不是 500。

**正确做法是把两种错误分开**，而本步更进一步：Redis 的任何错误都降级为「未命中」，但会记一笔日志（见决策 5）。

### 决策 2：Cache-Aside 的四步

**方案**：

```
读：查缓存 → 命中就返回
        → 未命中 → 查 DB → 回填缓存 → 返回
写：写 DB → 删缓存
```

**理由**：读多写少时命中率最高，实现最简单，且缓存故障不影响正确性（最坏情况是每次都查库）。

**为什么不是「写时更新缓存」**（step 的另一个常见做法）：

```
写：写 DB → 把新值写入缓存
```

看起来更好（下次读直接命中），但有两个问题：

1. **并发写会写坏缓存**。请求 A 和 B 同时改同一篇文章：
   ```
   A 写 DB (值=1) → B 写 DB (值=2) → B 写缓存 (2) → A 写缓存 (1)
   ```
   缓存最终是 1，DB 是 2 —— 缓存和 DB 永久不一致，直到这个 key 过期。

2. **写多读少时是纯浪费**。写 10 次只有 1 次读，你做了 10 次缓存写入，其中 9 次是没人看的。

**删缓存**则不会出现第一种情况：无论谁最后删，缓存都只是"空了"，下次读会从 DB 拿到最新值。这就是「删除比更新安全」的全部理由。

**不这样写会出什么问题**：用「更新缓存」方案，在并发写场景下会出现「刷新页面看到旧值，再刷一次又是新值」的诡异现象。而且它只在并发时出现，测试环境几乎复现不了。

### 决策 3：TTL 加随机抖动

**方案**：`cache.Jitter(ttl)` 返回 `ttl ± 10%`。

**理由**：不加抖动的话，同一批写入的 key（比如服务启动后处理的第一批请求）TTL 相同，会在**同一秒**集体过期。那一瞬间所有请求同时未命中、同时回源，数据库瞬时压力可能是平时的几十倍。这叫**缓存雪崩**。

**不这样写会出什么问题**：压测时一切正常（压测通常是预热过的），上线后每隔一段固定时间出现一次数据库负载尖峰。日志上看不出问题，因为每个请求都成功了 —— 只是慢。

**注意**：抖动只解决「同一时刻过期」，不解决「大量并发同时未命中」。后者的正解是 singleflight（把并发的相同查询合并成一次），本步未实现，属于下一步的优化点。

### 决策 4：缓存穿透与空标记

**方案**：查不到的数据也写一个 `{not_found: true}` 的空标记，TTL 只有 1 分钟。

**理由**：如果只缓存"存在的数据"，那么查询一个不存在的 slug 会**每次都穿透到 DB**。攻击者用随机 slug 请求一万次，你就得查一万次库 —— 而且缓存完全帮不上忙，因为「未命中」正是攻击者想要的结果。

空标记把「不存在」也变成一个可缓存的结果。

**为什么 TTL 必须很短（1 分钟）**：空标记期间如果用户新建了这篇文章，它在标记过期前都查不到。30 分钟的空标记会让「刚发布的文章打不开」变成一个可复现的 bug。

**不这样写会出什么问题**：一个爬虫遍历 `/posts/<随机字符串>`，数据库 CPU 打满，而缓存命中率统计显示"一切正常"（因为确实没有脏数据）。

### 决策 5：Redis 挂了要降级，不要让全站 500

**方案**：`cache.Connect` 即使连不上也返回 `(client, nil)`；所有缓存读写方法**不返回**「Redis 不可用」错误，而是当作未命中并记日志。

**理由**：Redis 是可选组件。它的价值是「快」，不是「能用」。如果 Redis 挂了导致全站 500，那就等于把一个缓存重启放大成了服务中断。

对比两种策略：

| 策略 | Redis 故障时 | 代价 |
| --- | --- | --- |
| fail-closed（报错） | 全站 500 | 一次缓存故障 = 一次服务故障 |
| fail-open（降级） | 接口变慢，功能正常 | DB 压力上升，可能被限流未挡住 |

对个人博客，选 fail-open 是显然的。对支付系统，可能选 fail-closed（宁可拒绝也不出错）。**没有统一答案，重要的是你明确选过并知道代价。**

**实现上的三个细节**：

1. `GetJSON` 返回 `(bool, error)`，其中 `error` 只表示「缓存内容坏了」（如反序列化失败），不表示「Redis 连不上」。
2. 降级日志做了频率限制（10 秒一次）。不做的话，Redis 挂掉期间每个请求打一行日志，几秒钟就能把磁盘写满 —— 一次缓存故障演变成磁盘写满，这是真实发生过的事故模式。
3. 反序列化失败时**删掉脏 key** 并当作未命中。发版后旧的缓存结构可能和新代码不兼容，让它持续 500 没有意义。

**不这样写会出什么问题**：Redis 做一次主从切换（几秒钟），你的服务在切换期间对所有请求返回 500。用户看到的是「网站挂了」，而实际上数据库好好的。

### 决策 6：浏览量用 INCR + 批量回写

**方案**：访问时 `HINCRBY blog:views:pending <postID> 1`，后台每分钟把增量刷进 DB。

**为什么不能每次访问 `UPDATE`**：

- 每来一个访问就一个写事务。热门文章被分享到社交平台时，一个只读接口被放大成写瓶颈。
- SQLite 的写锁是**整库级**的，这个影响会直接表现为「全站变慢」，而不只是那篇文章变慢。
- MySQL 上同理：高频更新同一行会争同一把行锁，阻塞其他事务。

**回写的原子性**：用 Lua 脚本做「读取并清空」：

```lua
local data = redis.call('HGETALL', KEYS[1])
if #data == 0 then return {} end
redis.call('DEL', KEYS[1])
return data
```

拆成两次调用会丢数据：`HGETALL` 和 `DEL` 之间进来的 `HINCRBY` 会被 `DEL` 干掉。

**回写失败时的策略**：本步选择**丢弃本批增量**并记日志。

这是刻意的取舍：浏览量不是钱，少统计一分钟可以接受；为它引入「失败后加回 Redis」的补偿逻辑，会带来重复计数的风险，而且会在故障时额外增加 Redis 压力。如果这是「订单金额」，答案会完全不同。

**不这样写会出什么问题**：`UPDATE` 方案下，你的博客被某个大 V 转发，几分钟内几万次访问，SQLite 的写锁让评论、审核、后台全卡住。而你只是"加了个浏览量统计"。

### 决策 7：限流必须用 Redis，内存版多实例会失效

**方案**：固定窗口，`INCR` + `PEXPIRE` 用 Lua 合成原子操作。

**为什么内存版会失效**：

```go
// 看起来完全够用
var hits = map[string]int{}
```

这类实现只在**单实例**下成立。一旦：

- **部署了 2 个实例**：攻击者打到哪个实例就单独计数，实际放行量 = limit × 2
- **滚动发布**：老实例的计数在新实例里不存在，限流瞬间归零
- **进程重启**：所有计数清零，攻击者等一次发布即可

「压测通过、上线出事」的典型成因就是这类假设。

**为什么用 Lua 而不是两行命令**：

```
INCR key          → 1
<进程崩溃>
PEXPIRE key 60    → 没执行
```

这个 key 永不过期，计数器永久累加。该 IP 从此**永远**返回 429，直到你手动去 Redis 删 key。用户会来投诉"我这个 IP 什么都打不开"，而你查遍代码也找不到原因。

**不这样写会出什么问题**：见上。另外，如果你用「先 GET 判断再 INCR」，在并发下两个请求可能同时读到 4（阈值 5），于是都放行 —— 实际放行了 6 次。Lua 把判断和自增合成一步，没有这个窗口。

### 决策 8：限流器挂在哪一层

**方案**：

```go
// 只挂「提交评论」这一条
posts.POST("/:slug/comments", ratelimit.Middleware(limiter), h.CreateComment)
```

而不是挂在整个 `posts` 分组上。

**理由**：

- 读接口和写接口的合理阈值完全不同。读接口一分钟 60 次很正常，写接口一分钟 60 次就是灌水。
- 如果把限流挂在分组上，你只有一个阈值。设宽了挡不住灌水，设严了正常读者刷几次文章就被 403。

**不这样写会出什么问题**：把 `limiter` 挂到整个 `posts` 分组，阈值设 5 次/分钟。结果读者连翻 6 页就收到 429，而灌水的人分 10 个 IP 提交仍然畅通。

### 决策 9：为什么缓存逻辑用装饰器，不写进 handler

**方案**：

```go
baseRepo := repository.NewGormRepo(db)
cachedRepo := repository.NewCachedRepo(baseRepo, redisClient)  // 装饰器
h := handler.New(cachedRepo, redisClient)
```

**理由**：如果缓存写在 handler 里，每个读接口都要重复「查缓存 → 查库 → 回填」，每个写接口都要记得「删缓存」。代码重复是次要的，**漏删缓存**是主要的 —— 新增一个写接口时忘了删，那个 key 就会一直返回旧值，直到 TTL 过期。

装饰器把所有缓存操作收敛到一个文件里，`ListPosts`/`GetPostBySlug`/`CreatePost`/`DeletePost` 的缓存行为一目了然。

**不这样写会出什么问题**：一个 handler 里写了 `db.Find`，另一个写了 `repo.List`，第三个加了缓存但忘删。三个月后没人能说清「哪些接口有缓存、TTL 多久、什么时候失效」。这类项目里，缓存最终会变成「不敢动的东西」—— 而不敢动的东西迟早会坏。

## 动手实验

### 实验 0：启动

```bash
docker run -d --name blog-redis -p 6379:6379 redis:7-alpine
cd step08-redis
go mod tidy
go run .
```

如果 Redis 已有同类容器，先 `docker rm -f blog-redis`。

### 实验 1：命中缓存后数据库不再被查

这个实验最直观：把 GORM 的日志级别打开，看第二次请求是否还打 SQL。

修改 `bootstrap/db.go` 里 `DefaultDatabase()` 的 `LogLevel`：

```go
LogLevel: logger.Info,   // 临时改成 Info，会打印每条 SQL
```

然后：

```bash
# 第一次：应该看到 SELECT ... WHERE slug = ?
curl -s http://localhost:8080/api/v1/posts/why-not-db-in-handler > /dev/null

# 第二次：不应该再出现 SELECT
curl -s http://localhost:8080/api/v1/posts/why-not-db-in-handler > /dev/null
```

再做反向验证 —— 删掉缓存后应该重新查库：

```bash
docker exec blog-redis redis-cli DEL "blog:post:why-not-db-in-handler"
curl -s http://localhost:8080/api/v1/posts/why-not-db-in-handler > /dev/null
# 应该又出现 SELECT
```

### 实验 2：看看缓存里到底存了什么

```bash
# key 列表
docker exec blog-redis redis-cli KEYS "blog:*"

# 文章详情的缓存内容
docker exec blog-redis redis-cli GET "blog:post:why-not-db-in-handler"

# TTL（应该看到 1600-2000 秒之间，且不是整数 —— 那就是抖动）
docker exec blog-redis redis-cli TTL "blog:post:why-not-db-in-handler"
```

### 实验 3：缓存穿透防护

```bash
# 请求一个不存在的 slug
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/posts/this-slug-does-not-exist
# 预期：404

# 看空标记是否写进去了
docker exec blog-redis redis-cli GET "blog:post:this-slug-does-not-exist"
# 预期：{"not_found":true}

# TTL 应该很短
docker exec blog-redis redis-cli TTL "blog:post:this-slug-does-not-exist"
# 预期：小于 60 秒
```

### 实验 4：浏览量不写库，但会显示实时值

```bash
# 连续访问 5 次
for i in 1 2 3 4 5; do curl -s http://localhost:8080/api/v1/posts/why-not-db-in-handler > /dev/null; done

# 看 Redis 里的待回写增量
docker exec blog-redis redis-cli HGETALL "blog:views:pending"
# 预期：能看到 articleID -> 增量

# 看接口返回值（应该是 DB 旧值 + 增量）
curl -s http://localhost:8080/api/v1/posts/why-not-db-in-handler | jq '.views'
```

等一分钟（默认回写间隔），再看：

```bash
docker exec blog-redis redis-cli HGETALL "blog:views:pending"
# 预期：空了

# 数据库里的值应该更新了
sqlite3 data/blog.db "SELECT views FROM posts WHERE slug='why-not-db-in-handler';"
```

### 实验 5：Redis 挂了会怎样（本步最重要的实验）

```bash
# 停掉 Redis
docker stop blog-redis

# 服务应该仍然可用，只是变慢
curl -s -o /dev/null -w "状态码: %{http_code}  耗时: %{time_total}s\n" \
  http://localhost:8080/api/v1/posts/why-not-db-in-handler
# 预期：200，耗时应比之前明显变长（每次都要查库）
```

多请求几次，观察服务端日志：应该看到 `[WARN] Redis 操作降级`，但**不会**刷屏（10 秒一次）。

评论接口也还能用（限流降级为放行）：

```bash
for i in 1 2 3 4 5 6 7; do
  curl -s -o /dev/null -w "%{http_code} " -X POST \
    http://localhost:8080/api/v1/posts/why-not-db-in-handler/comments \
    -H "Content-Type: application/json" \
    -d "{\"nickname\":\"测试$i\",\"content\":\"降级测试\"}"
done
echo
# 预期：全是 201（本该第 6 条开始 429，但 Redis 不可用时 fail-open）
```

恢复 Redis 后：

```bash
docker start blog-redis
# 限流应立刻恢复生效
```

再加一个对照实验 —— 用 `-noredis` 参数启动（连到不存在的端口）：

```bash
go run . -noredis
# 应该看到 "[INFO] 已通过 -noredis 跳过 Redis，直接进入降级模式"
# 服务照常可用，所有请求都走数据库
```

### 实验 6：限流生效

```bash
# 连续提交 7 条评论，阈值是 5 次/分钟
for i in 1 2 3 4 5 6 7; do
  curl -s -o /dev/null -w "%{http_code} " -X POST \
    http://localhost:8080/api/v1/posts/why-not-db-in-handler/comments \
    -H "Content-Type: application/json" \
    -d "{\"nickname\":\"测试$i\",\"content\":\"测试限流第 $i 条\"}"
done
echo
# 预期：201 201 201 201 201 429 429
```

看响应头：

```bash
curl -s -i -X POST http://localhost:8080/api/v1/posts/why-not-db-in-handler/comments \
  -H "Content-Type: application/json" \
  -d '{"nickname":"测试","content":"看响应头"}' | grep -i "x-ratelimit\|retry-after"
```

### 实验 7：验证写操作会删缓存

```bash
# 先读一次，让缓存存在
curl -s http://localhost:8080/api/v1/posts/gorm-soft-delete-pitfall > /dev/null
docker exec blog-redis redis-cli EXISTS "blog:post:gorm-soft-delete-pitfall"
# 预期：1

# 删掉这篇文章
curl -s -o /dev/null -w "%{http_code}\n" -X DELETE http://localhost:8080/api/v1/posts/gorm-soft-delete-pitfall

# 缓存应该被删了
docker exec blog-redis redis-cli EXISTS "blog:post:gorm-soft-delete-pitfall"
# 预期：0

# 再访问应该 404（而不是命中缓存返回旧数据）
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/posts/gorm-soft-delete-pitfall
# 预期：404
```

## 这一步的遗留问题

1. **列表缓存的失效不完整**。`CreatePost` 只删了详情 key，没有让列表缓存失效 —— 所以新建文章后，列表页要等 5 分钟才能看到它。正确做法是「版本号」：key 里嵌一个全局版本号，发文时版本号 +1。（本步的 `CreatePost` 里有注释说明，但没实现）
2. **审核通过后文章详情的评论数会旧**。评论审核不改变文章本身，但会改变详情页展示的评论数。严格做法是让 `ApproveComment` 返回 `postID`，然后精确删那一个 key。本步选择承认这个缺口。
3. **没有 singleflight**。热点 key 过期瞬间，N 个并发请求会同时回源，缓存起不到"合并请求"的作用。
4. **限流只按 IP**。共享出口（公司/校园网）的用户会互相影响；配合 `X-Forwarded-For` 伪造时还能绕过。
5. **handler 里仍然混着业务规则**（默认 status=published、评论强制 pending）。这些规则在 Step 9 会下沉到 service 层。
6. **没有优雅关停**。`Ctrl+C` 时在途请求会被切断，`stopFlusher()` 也不一定跑得到。这是 Step 3 的内容。

## 关于「未验证」

本步的代码与实验预期输出**我没有在你的机器上运行验证过**。具体不确定的地方：

- `docker exec ... redis-cli HGETALL` 的返回格式在不同版本下可能略有差异，但键值对的形式是稳定的
- 实验 5 里「降级后每个请求都会明显变慢」的幅度取决于你的磁盘和 SQLite 版本
- 实验 1 中 GORM 的 SQL 日志需要把 `LogLevel` 改成 `Info`，这个改动我写在了文档里但没实测你的输出格式

如果实际结果与文档不一致，以你的机器为准并告诉我差异 —— 尤其是「降级后仍然 500」这类问题，那说明我的降级实现有遗漏。
