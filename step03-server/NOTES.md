# Step 3：把「能跑」变成「能停」

## 这一步解决了什么问题

Step 1 和 Step 2 都用这一行启动服务：

```go
r.Run(":8080")
```

干净得让人不想改。但它藏着三个问题，都是上了线才会咬人的：

| 问题 | 后果 |
| --- | --- |
| 地址写死 | 换端口要改代码重新编译 |
| **没有任何超时** | 一个只连不发数据的客户端就能永久占住连接 |
| 无法优雅关停 | `Ctrl+C` 直接切断在途请求，用户看到报错，**数据库可能留下写了一半的数据** |

第二条最容易被忽略，因为它不报错、平时也看不出来。下面先讲它。

## 核心决策

### 决策 1：为什么 `r.Run()` 没有超时

`r.Run(addr)` 的源码只有一行实质内容：

```go
return http.ListenAndServe(addr, engine)
```

而 `ListenAndServe` 内部是这样：

```go
srv := &http.Server{Addr: addr, Handler: handler}
return srv.ListenAndServe()
```

注意它没有设置任何超时字段。`http.Server` 零值下的 `ReadTimeout`、`WriteTimeout`、
`IdleTimeout` 全部是 **0**，而 0 在这个类型里的含义是「**不限时**」。

所以默认状态下：

- 一个 TCP 连接可以永远不用发完请求头
- 一个 keep-alive 连接可以永远空转
- 每个这样的连接占一个文件描述符（Linux 默认 1024，容器里可能更小）

攻击者用几百个慢连接就能让你的服务无法接受新连接。这就是 **slowloris**。
更现实的情况是：不需要攻击，一批网络很差的移动端用户就能造成同样效果。

**结论：Gin 不提供超时配置，因为它把这件事留给了标准库。要用，就得自己构造
`http.Server` —— 这就是 `r.Run` 必须被替换掉的根本原因。**

### 决策 2：四个超时各自防什么

这四个值经常被抄来抄去而没人知道含义。它们的区别在于**计时起点不同**：

```
建立连接 ──> 收到请求头 ──> 收到完整请求 ──> handler 执行 ──> 响应写完
   │            │                │                │              │
   ├─ ReadHeaderTimeout ─────────┤                │              │
   │            │                │                │              │
   ├─ ReadTimeout ────────────────┤                │              │
   │            │                │                │              │
   │            │                ├─ WriteTimeout ────────────────┤
   │            │                │                │              │
空闲时 ├─ IdleTimeout（只对 keep-alive 的两次请求之间的间隔生效）
```

| 字段 | 计时窗口 | 防什么 |
| --- | --- | --- |
| `ReadHeaderTimeout` | 连接建立起 → 读完请求头 | slowloris（慢慢发 header） |
| `ReadTimeout` | 连接建立起 → 读完整个请求（含 body） | 慢慢发 body、超大 body |
| `WriteTimeout` | **读完请求头** → 响应写完 | 慢客户端不接收响应、handler 太慢 |
| `IdleTimeout` | 两次请求之间的空闲期 | keep-alive 连接大量空转吃 fd |

三个容易踩的细节：

**① `ReadTimeout` 包含客户端上传时间。**
如果将来支持上传大文件，这个值必须放大，否则大文件在慢网络下必然超时。
（更好的做法是上传接口单独用 `http.Server` 或调大超时。）

**② `WriteTimeout` 的计时起点是「读完请求头」，所以它已经包含了 handler 的执行时间。**
这意味着 `WriteTimeout` 必须大于你最慢接口的耗时。
步 3 的 `/slow` 接口默认睡 5 秒，而 `WriteTimeout` 是 30 秒——如果你把它改成 2 秒，
访问 `/slow` 就会失败。这个实验能让你直观感受到它们的耦合。

**③ 0 值 = 不限时。** 这跟大多数配置的直觉相反（通常 0 表示"用默认"）。
所以 `config.validate()` 里直接拒绝 0——写 0 几乎总是笔误。

### 决策 3：`Shutdown()` 和 `Close()` 的区别

这是本步最重要的概念。

```go
srv.Shutdown(ctx)   // 拉闸限行：停止接客，等在途请求做完
srv.Close()         // 紧急断电：立刻断开所有连接，在途请求全部中断
```

`Shutdown` 的动作顺序：

1. 关闭监听 socket —— 不再接受新连接
2. 关闭所有**空闲**连接
3. **阻塞等待**正在处理的请求完成
4. 等完了就返回 nil；超时了返回 context.DeadlineExceeded，此时才该用 `Close` 兜底

为什么第 3 步这么重要？看一个具体场景：

> 用户提交订单，handler 里正在执行：插入订单 → 扣库存 → 写日志。
> 进程在执行到「扣库存」时被 `Close()`。

结果：订单插入了，库存没扣。这条数据在数据库里静默存在，没人知道它不完整。

`Shutdown` 让这个 handler 能跑完，所以不会有半截数据。

**一个必须注意的细节**：传给 `Shutdown` 的 context **必须是新建的**。

```go
// 错误：用了可能已取消的 ctx
srv.Shutdown(parentCtx)

// 正确：新建一个只用于关停的 context
ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
srv.Shutdown(ctx)
```

原因：`Shutdown` 一旦发现传入的 context 已经取消，会**立刻返回**
（等同于没等）。用请求的 context 或者父 ctx 就会出现这种情况。

### 决策 4：`run() int` 这个模式

```go
func main() {
    os.Exit(run())
}
```

为什么不直接在 `run()` 里 `os.Exit` 或 `log.Fatal`？

**因为 `os.Exit` 会跳过所有 `defer`。** 包括：

```go
func run() int {
    f, _ := os.Open(...)
    defer f.Close()          // ← os.Exit 时这行永远不会执行
    ...
    os.Exit(1)               // 如果写在这里，f 不会被关闭
}
```

把 `os.Exit` 限制在 `main` 这一层，`run` 内部所有 `defer` 都能正常执行。
同时整个启动流程变成「返回一个数字」，测试里可以直接断言返回值。

顺带一个好处：错误处理方式统一了——每个失败分支都是「打印原因 + return 非 0」，
不需要你在脑海里维护「这里该 log.Fatal 还是 return err」。

### 决策 5：配置的三层来源

```
默认值（代码里） → 配置文件（config.toml） → 环境变量（GINBLOG_*）
```

顺序不能乱，理由分别是：

- **默认值在最前**：配置文件里少写一项时，不会得到 0 值。`ReadTimeout=0` 是灾难性的（不限时），而默认值 15 是安全的。
- **环境变量在最后**：容器部署时改配置不该重新打镜像。这也是 12-factor 的原则之一。

**为什么配置文件允许不存在？** 因为全走环境变量的部署方式很常见（Kubernetes），
强制要求文件存在反而是负担。但要注意：允许不存在 ≠ 允许缺失关键项，
所以 `validate()` 必须兜住。

**为什么环境变量要加 `GINBLOG_` 前缀？** 环境变量是全局命名空间。
直接用 `PORT` 迟早会和别的程序撞名，而撞名导致的故障极难定位
（你的服务读到了别人的端口号）。

## 动手实验

### 实验 A：验证优雅关停

```bash
cd step03-server
go mod tidy
go run .
```

开**两个**终端。

终端 1 发起一个 5 秒的请求：

```bash
curl -i "http://localhost:8080/api/v1/slow?ms=5000"
```

终端 2 在 1 秒内按 `Ctrl+C` 停止服务。

**你应该看到**：终端 1 最终收到了**正常的 200 响应**，而不是 `curl: (52) Empty reply
from server` 或连接重置。终端 2 打印「在途请求已处理完毕，服务正常退出」。

如果你看到的是「优雅关停超时」——那是另一种情况：请求比 15 秒还长。
用 `?ms=20000` 试试，能复现超时分支。

### 实验 B：验证超时的必要性

把 `configs/config.toml` 里的 `writeTimeout` 改成 `2`，重新运行：

```bash
curl -i "http://localhost:8080/api/v1/slow?ms=5000"
```

会失败。因为 handler 睡了 5 秒，超过了 `WriteTimeout` 的 2 秒——
服务端会断开连接，客户端拿不到完整响应。

这个实验说明：**`WriteTimeout` 包含 handler 执行时间，它必须大于最慢接口的耗时。**

### 实验 C：验证配置的失败即退

```bash
# 端口写错了（非数字）
GINBLOG_ADDR="abc" go run .
```

观察：服务仍会启动（因为 ListenAndServe 会自己报错），但打印的是
`HTTP 服务启动失败: listen tcp: address abc: unknown port`，并且退出码非 0。

```bash
# 超时设成 0
GINBLOG_SHUTDOWN_TIMEOUT=0 go run .
```

观察：直接被 `validate()` 拦下，打印配置错误，退出码 1，**服务根本没启动**。
这就是「fail fast」——带着错误配置启动成功才是最难查的。

### 实验 D：看退出码

```bash
go run . ; echo "退出码: $?"
# Ctrl+C 之后应该看到 退出码: 0
```

注意 `go run` 会把信号转发给子进程，但退出码可能被 `go run` 本身影响。
要精确验证退出码，先编译：

```bash
go build -o app.exe .
./app.exe ; echo "退出码: $?"
```

## 一个还没解决的问题

本步做得来「进程内部优雅」，但**进程外部还不优雅**：

在 Nginx 或负载均衡后面时，服务收到 SIGTERM 的那一刻，
LB 可能还在往这个实例转发新请求。`Shutdown()` 会关掉监听 socket，
这些新请求就会连接失败。

真正的做法是**两阶段下线**：
1. 先把 `/readyz` 设为不健康（LB 停止转发）
2. 等一个「传播延迟」（比如 5 秒）
3. 再调用 `Shutdown()`

这需要引入就绪探针，是 Step 10 的内容。

## 这一步的遗留问题

1. **每个接口都在手写 `c.JSON(400, gin.H{"error": ...})`** ——
   十几处格式不一致，前端无法统一解析；而且 `err.Error()` 会把
   `Field validation for 'Nickname' failed on the 'min' tag` 这种内部信息直接抛给用户。
   → **Step 4**
2. **`gin.Default()` 的日志格式改不了** —— 想加 `request_id` 串联一次请求的多条日志，
   想按状态码分级，想过滤探活接口的噪音，都做不到。→ **Step 6**
3. **管理接口和公开接口扁平地挂在一起**，没有任何「必须登录」的保障。→ **Step 5、Step 6**
