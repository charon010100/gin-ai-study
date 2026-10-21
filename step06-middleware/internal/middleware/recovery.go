package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// Recovery 捕获 panic，记录堆栈，返回 500。
//
// gin 自带 gin.Recovery()，那为什么还要自己写？
//
// 因为自带的版本行为固定：它把堆栈直接写到 c.Writer（也就是**返回给客户端**），
// 并且走的是 gin 内部的日志。你需要的是：
//   - 堆栈只进服务端日志，绝不返回给客户端（堆栈里有文件路径、函数名、依赖版本）
//   - 记录 request_id，方便按 ID 找到这次请求的其他日志
//   - 返回统一的 JSON 错误格式（Step 4 定义的）
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		// defer 保证无论 handler 怎么退出（正常返回、panic、Abort），
		// 这里的代码都会执行。
		defer func() {
			if err := recover(); err != nil {
				// ---------- 只记录，不返回 ----------
				//
				// 这里打印的内容只到服务端日志。
				// 注意：debug.Stack() 拿到的是**当前 goroutine** 的堆栈，
				// 也就是发生 panic 的那个 goroutine（因为 recover 必须
				// 在同一个 goroutine 里才有效）。
				log.Printf("[PANIC] %s %s | rid=%s | err=%v\n%s",
					c.Request.Method, c.Request.URL.Path,
					RequestIDFrom(c.Request.Context()), err, debug.Stack())

				// ---------- 给客户端的响应 ----------
				//
				// 不透露任何内部细节。客户端只需要知道「服务端出错了，可以重试」。
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code":    5001,
					"message": "服务器内部错误，请稍后重试",
					"data":    nil,
				})
			}
		}()

		c.Next()
	}
}

// RecoveryInGoroutine 是给「handler 里自己起的 goroutine」用的。
//
// 这是一个非常容易被忽略的坑，值得单独说：
//
//	panic 只能被**同一个 goroutine** 里的 recover 捕获。
//
// 所以如果你在 handler 里写了：
//
//	go func() {
//	    doSomething()   // 这里 panic 了
//	}()
//
//	中间件里的 Recovery **捕获不到**。结果是整个进程崩溃——
//	注意，是进程崩溃，不是这一次请求失败。所有在线用户一起断线。
//
// 正确写法是把 recover 包在那个 goroutine 里面：
//
//	go func() {
//	    defer RecoveryInGoroutine(c)
//	    doSomething()
//	}()
//
// 但要先说清楚：**handler 里应该尽量避免起 goroutine**。
// 因为请求结束后 c 就失效了，而 goroutine 还在跑的话，
// 它会往一个已经提交的响应里写数据，行为不可预测。
// 真需要异步处理，应该用任务队列（Step 8 之后讨论）。
func RecoveryInGoroutine(c *gin.Context) {
	if err := recover(); err != nil {
		log.Printf("[PANIC-GOROUTINE] rid=%s | err=%v\n%s",
			RequestIDFrom(c.Request.Context()), err, debug.Stack())
	}
}
