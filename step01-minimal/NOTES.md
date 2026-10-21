# Step 1：最小可运行的服务

## 这一步解决了什么问题

在写任何业务代码之前，先确认一件事：**你能把一个 HTTP 服务跑起来，并且能访问到它。**

听起来很基础，但新手卡住的地方往往就在这里——不是代码不会写，而是不知道为什么访问不通。
这一步把「服务怎么起来的」这件事压到最小，方便你排除干扰。

## 运行

```bash
cd step01-minimal
go mod tidy      # 第一次必须执行，否则报 missing go.sum entry
go run .
```

## 你应该观察到什么

### 1. 启动时打印的路由表

```
[GIN-debug] GET    /ping   --> main.main.func1 (3 handlers)
[GIN-debug] Listening and serving HTTP on :8080
```

这两行是 DebugMode 特有的。第一行告诉你「注册了哪些路由、由哪个函数处理」，
线上绝对不想暴露这个（等于把内部结构告诉攻击者），所以生产环境必须关掉
——具体怎么关在 Step 3。

第二行的 `:8080` 就是你要监听的地址。

### 2. 请求后打印的访问日志

```
[GIN] 2024/01/01 - 12:00:00 | 200 |     12.5µs |  127.0.0.1 | GET      "/ping"
```

这行**不是你写的**，是 `gin.Default()` 里的 Logger 中间件打的。
状态码、耗时、客户端 IP、方法、路径，全都有了。

所以请记住：**你的 handler 里不需要写「访问日志」**，框架已经做了。
你需要写的是「业务日志」——比如「用户 123 发布失败，因为标题为空」。

### 3. 验证 Recovery 真的在生效

把 `/ping` 的处理函数临时改成这样：

```go
r.GET("/ping", func(c *gin.Context) {
    var m map[string]string
    m["x"] = "y"        // 往 nil map 里写，一定 panic
})
```

重新 `go run .` 再访问 `/ping`，你会看到：

- 客户端收到 `500 Internal Server Error`（而不是连接被重置）
- 服务端日志里打印了完整的 panic 堆栈
- **服务没有退出**，再访问一次 `/healthz`（如果注册了）依然正常

这就是 Recovery 的价值：**把一次请求的崩溃限制在那一次请求里**。

如果没有它，panic 会向上冒到 `main`，整个进程退出，
所有在线用户一起断线。这就是为什么生产服务**必须**有 Recovery。

## 需要理解清楚的三个点

### 点 1：`r.Run()` 是阻塞的

```go
r.Run(":8080")
fmt.Println("这行永远不会执行")   // 除非服务停了
```

`r.Run` 内部是 `http.ListenAndServe`，它会一直阻塞在 accept 循环里。
所以：

- **任何写在它后面的代码都是死代码**
- 想在启动后做点事（比如打印「服务已就绪」），要么写在 `Run` 之前，
  要么把 `Run` 放到单独的 goroutine 里
- 想「主动停止服务」，不能靠在后面写代码，只能靠信号（Step 3 讲）

### 点 2：为什么不用标准库 `net/http`

你已经会 Gin 了，但值得花 30 秒想想 Gin 到底帮你做了什么。
上面这段用标准库写是这样：

```go
http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    json.NewEncoder(w).Encode(map[string]string{"message": "pong"})
})
http.ListenAndServe(":8080", nil)
```

差别在哪：

| 事情 | 标准库 | Gin |
| --- | --- | --- |
| 手动设置 Content-Type | 每次都要写 | `c.JSON` 自动 |
| 路径参数 `/user/:id` | 要自己解析字符串 | `c.Param("id")` 直接拿 |
| 请求体绑定到结构体 | 手写 decode + 校验 | `c.ShouldBindJSON` 一步 |
| 中间件 | 要自己包一层又一层 | `r.Use()` 一个列表 |
| 访问日志 / panic 恢复 | 完全没有，自己写 | `gin.Default()` 自带 |

所以 Gin 的定位是**路由器 + 中间件框架 + 请求绑定器**，
HTTP 服务的底层（TCP 监听、连接管理、超时）它用的还是标准库。

这解释了一个后面会反复用到的结论：
**`r.Run()` 只是标准库的一层薄封装，所以标准库能做的事（自定义超时、优雅关停）
Gin 自己没提供，必须绕过 `Run` 直接用 `http.Server`。**

### 点 3：`gin.H` 只是语法糖

```go
gin.H{"message": "pong"}
```

`gin.H` 就是 `map[string]any` 的类型别名，没有任何魔法。
它的价值在于：你可以少打几个字，而且明确表示「这是个临时拼的 JSON」。

**但它是无序的**——map 的遍历顺序在 Go 里是随机的。
如果你需要字段按固定顺序输出（比如给别的系统做签名），
必须用 struct 而不是 `gin.H`：

```go
type PingResp struct {
    Message string `json:"message"`
    Time    int64  `json:"time"`
}
c.JSON(http.StatusOK, PingResp{Message: "pong", Time: time.Now().Unix()})
```

这一点很重要：**真实项目里几乎不该用 `gin.H`**，
因为接口返回结构需要被文档化、被前端引用、被测试断言。
从 Step 2 开始我们就换成 struct。

## 这一步的遗留问题

跑通之后，回头看看 `main.go`，问自己：

1. **所有路由都写在这里会怎样？** 一个博客有 20 个接口，`main.go` 会有 300 行，
   而且找 `/posts` 要滚半天。→ Step 2
2. **端口 8080 写死了。** 换台机器、换个环境就要改代码重新编译。
   而且线上用 `Ctrl+C` 停止服务时，正在处理的请求会被直接切断，
   用户看到报错，数据库里可能留下写了一半的数据。→ Step 3
3. **每个接口的返回格式都不一样。** 有的返回 `{"message":"pong"}`，
   有的返回 `{"error":"..."}`，前端要为每个接口写一套解析逻辑。→ Step 4

带着这三个问题进入下一步。
