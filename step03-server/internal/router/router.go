// Package router 负责 URL → 处理函数 的映射。
// 与 Step 2 相比只多了两个演示用的接口，结构完全没变。
package router

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// Register 注册全部路由。
func Register(engine *gin.Engine) {
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := engine.Group("/api/v1")
	{
		api.GET("/posts", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"list": []string{}})
		})

		// 这个接口是专门为本步骤的实验造的：它会睡一会儿再返回。
		//
		// 用途：在它睡眠期间按 Ctrl+C，观察「优雅关停」和「直接杀进程」的区别。
		// 真实项目里对应的是「一个耗时 3 秒的报表查询」——关停时它不该被粗暴切断。
		//
		// ms 用查询参数控制时长，默认 5 秒。
		api.GET("/slow", func(c *gin.Context) {
			ms, err := strconv.Atoi(c.DefaultQuery("ms", "5000"))
			if err != nil || ms < 0 {
				ms = 5000
			}
			if ms > 20000 {
				ms = 20000 // 上限，防止实验时把自己等太久
			}

			// 注意：time.Sleep 是「不可中断」的，即使请求的 context 被取消，
			// 它也会睡满。真实项目里这里应该用 select 监听 c.Request.Context().Done()，
			// 让「客户端断开」或「服务正在关停」能立刻结束等待。
			// 教学这里故意保留 Sleep，因为我们要验证的是「关停会等它做完」。
			time.Sleep(time.Duration(ms) * time.Millisecond)

			c.JSON(http.StatusOK, gin.H{
				"message": "这个响应在你按下 Ctrl+C 之后才返回，但它没有丢",
				"slept_ms": ms,
			})
		})
	}
}
