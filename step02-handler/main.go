// Command server 是应用的入口。
//
// 与 Step 1 的 main.go 对比，这里发生了三件事：
//  1. 路由注册被搬到 internal/router，main 只负责「装配」；
//  2. 处理函数被搬到 internal/handler，不再以闭包形式写在 main 里；
//  3. 引入了 internal/model 存放共享的数据结构。
//
// 关键认识：main 包应该只做「把各个零件接起来」这一件事。
// 一旦 main 里出现了 if/for 的业务判断，就说明有代码放错地方了。
package main

import (
	"github.com/gin-gonic/gin"
	"ginlesson/step02/internal/router"
)

func main() {
	// 这里保留了 gin.Default()。
	//
	// 为什么不立刻换成 gin.New() + 自己写中间件？
	// 因为 Step 3 要先解决「配置」和「优雅关停」——那两个问题不解决，
	// 换成 gin.New() 只会让我们同时面对三个新问题，不符合「一步一个概念」的原则。
	// 自建中间件放 Step 6，那时 Logger/Recovery 会被我们自己的实现替换掉。
	engine := gin.Default()

	// 把「注册路由」这件事委托出去。
	//
	// 注意 router.Register 需要的只是 *gin.Engine，
	// 它不知道也不关心 engine 是怎么创建出来的。
	// 这种「只依赖需要的东西」的习惯，在 Step 7 注入数据库时会体现出价值。
	router.Register(engine)

	_ = engine.Run(":8080")
}
