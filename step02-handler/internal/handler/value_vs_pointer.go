// Package handler 的这个文件是一个**故意写错的示例**，用来演示
// 「gin.Context 该传值还是传指针」。
//
// 它不参与任何路由注册，不影响正常功能。
// 想亲眼看到现象，按 NOTES.md 里的说明把它临时接到路由上。
//
// ⚠️ 不要模仿本文件的写法，正确写法见同包的 post.go。
package handler

import (
	"github.com/gin-gonic/gin"
)

// BrokenHandler 的接收者参数是 gin.Context（值），而不是 *gin.Context（指针）。
//
// ── 先纠正常见误解 ──
//
// 很多文章说「传值会导致接口没有响应」。**这是错的**，我一开始也这么以为。
//
// 真实情况是：这段代码能编译，几乎总是能正常返回响应。原因在于
// gin.Context 内部的 Writer 字段是一个**接口**（ResponseWriter）：
//
//	type Context struct {
//	    writermem responseWriter      // 值类型，内含 Writer 和 status 等字段
//	    Request   *http.Request
//	    Writer    ResponseWriter      // 接口
//	    ...
//	}
//
// 按值复制 struct 时，接口字段复制的是**接口头**（类型指针 + 数据指针），
// 而它指向的底层对象是同一个。所以副本里的 c.JSON(...) 依然会把数据写进
// 本次请求真正的响应缓冲区 —— 客户端能收到响应。
//
// ── 那问题到底在哪？──
//
// 问题在于「Context 的语义被破坏了」，而且**大多数情况下不会立刻暴露**，
// 于是变成偶发、难查的怪现象。具体有四处：
//
//  1. c.Next() 失效。Context 内部用 index 字段记录中间件执行到了第几个，
//     副本有自己的 index。副本里调用 Next，推进的是副本，原件不受影响。
//     表现：本该在 handler 之后执行的中间件逻辑没跑，或跑的顺序乱了。
//
//  2. c.Set / c.Get —— 这一条**最容易讲错**，必须说准确。
//
//     Keys 是一个 map（引用类型）。按值复制 struct 时，复制的是 map 头
//     （指针），所以副本和原件**共享同一个 map**。也就是说：
//     中间件 c.Set 的值，副本里 c.Get **大概率还是能取到**。
//
//     证据在 gin 自己的 Copy() 里 —— 它之所以要显式 maps.Clone(c.Keys)，
//     正是因为裸拷贝会共享 Keys：
//
//         func (c *Context) Copy() *Context {
//             ...
//             c.mu.RLock()
//             cp.Keys = maps.Clone(c.Keys)   // ← 不 clone 就会共享
//             c.mu.RUnlock()
//             ...
//         }
//
//     「能用」不是好消息，而是坏消息：bug 被掩盖了。
//     真正的问题有两个：
//       a) 若 Keys 还是 nil 时在副本里 Set，副本会**自己新建**一个 map
//          （Set 里有 if c.Keys == nil { c.Keys = make(...) }），
//          这时原件确实看不到 —— 表现为「有时能取到、有时取不到」。
//       b) 更严重：复制了 sync.RWMutex。副本的锁和原件的锁是**两把不同的锁**，
//          却保护着同一个 map。并发读写时锁不再生效 → data race，
//          用 -race 能抓到，不加就静默出错（"concurrent map read and map write"）。
//
//  3. c.Error(err) 记录的错乱。Errors 是切片，分配在副本自己的字段上：
//     append 后的新切片头写回副本，原件 c.Errors 的长度没变，看不到追加项。
//     表现：错误被吞掉，日志里没有，但客户端收到了 4xx/5xx，无法定位原因。
//
//  4. 复制了 sync.RWMutex。gin.Context 在 v1.8+ 内部含锁（锁注释写得很明确：
//     "This mutex protects Keys map"）。复制含锁的类型本身就是错误用法，
//     `go vet` 会报 copylocks。
//
// 一句话总结：**不是「响应没了」，也不是「值全丢了」，而是「部分语义静默失效、
// 部分语义碰巧还能用，且原本受锁保护的 map 不再受保护」**。
// 三者混在一起，比彻底坏掉难查得多 —— 因为它不报错。
func BrokenHandler(c gin.Context) {
	// 这行大概率能正常工作 —— 但这不代表整个函数是对的。
	c.JSON(200, gin.H{"message": "响应能出来，但 c.Next/c.Error 已失效，c.Set/c.Get 有数据竞争风险"})
}

// CorrectHandler 是对照组：唯一区别是多了个星号。
func CorrectHandler(c *gin.Context) {
	c.JSON(200, gin.H{"message": "正确写法：func(c *gin.Context)"})
}

// 这个断言的用途：把「gin.Context 内部含锁」这个事实固定在代码里。
//
// 如果将来 gin 移除了内部的 mutex，go vet 不再报 copylocks，
// 那么用它反推「为什么不能传值」的论据就少了一条 ——
// 这个函数会因编译失败（方法签名变化）而提醒我们更新注释。
var _ = func() {
	// 只是引用类型，不实际调用。
	var _ *gin.Context
}
