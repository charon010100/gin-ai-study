//go:build ignore

// 这个文件不参与正常编译（顶部有 //go:build ignore）。
//
// 用途：单独跑它，亲眼看到「路由冲突会在启动时 panic」。
//
//	go run demo_conflict.go
//
// 你会看到类似这样的输出（gin v1.10）：
//
//	panic: ':id' in new path '/posts/:id' conflicts with existing wildcard
//	       ':slug' in existing prefix '/posts/:slug'
//
// 注意：这是**启动时**的 panic，不是请求时才报错。
// 也就是说只要你注册了两条冲突的路由，服务根本起不来——
// 这其实是好事，因为它不可能在生产环境里悄悄坏掉。
package main

import (
	"github.com/gin-gonic/gin"
)

func main() {
	engine := gin.New()

	// 同一个位置（/posts/ 之后）出现了两个**不同名字**的参数。
	// gin 的路由树只允许同一位置有一个参数名，因为它在匹配时
	// 不知道应该把 /posts/123 里的 "123" 赋给 slug 还是 id。
	engine.GET("/posts/:slug", func(c *gin.Context) {})
	engine.GET("/posts/:id", func(c *gin.Context) {}) // ← panic 在这里

	engine.Run(":8080")
}
