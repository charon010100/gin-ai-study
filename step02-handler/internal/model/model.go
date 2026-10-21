// Package model 存放跨层共享的数据结构。
//
// 为什么单独建一个包，而不是把 Post 定义在 handler 里？
//
// 因为从 Step 7 开始，这些结构会被三个不同的地方使用：
//   - handler  用它来返回 JSON
//   - service  用它来承载业务逻辑
//   - repository 用它来映射数据库
//
// 如果定义在 handler 包里，service 和 repository 就必须 import handler，
// 依赖方向就反了（数据层不该依赖 HTTP 层）。放在最底层的 model 包里，
// 谁都可以引用它，而它谁也不依赖——这是一个「没有出边」的节点。
package model

// Post 是一篇文章。
//
// 注意 ID 用 string 而不是 int：
// 本教程为了让你专注 HTTP 层，暂时不引入参数类型转换的复杂度；
// 真实项目里用自增整数或雪花 ID 时，这里会改成 uint64，
// 并在 handler 里做 strconv.ParseUint（那一步的失败必须返回 400，不能忽略）。
type Post struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Summary  string `json:"summary"`
	Content  string `json:"content,omitempty"`
	Author   string `json:"author"`
	Views    int64  `json:"views"`
	CreateAt string `json:"created_at"`
}

// Comment 是一条评论。
//
// 字段上的 binding tag 是给 Gin 的校验器用的，规则如下：
//
//	required   必须存在且非零值（空字符串算零值，所以会失败）
//	min=2      长度至少 2（对字符串是字符数，对数字是数值大小）
//	max=500    长度至多 500
//
// 一个常见的误解：这些 tag 只在调用 ShouldBindJSON 时生效，
// 如果你自己手写 json.Unmarshal，它们完全不会被检查。
// 这就是为什么参数绑定一定要交给 Gin 做，而不是自己 decode。
type Comment struct {
	Nickname string `json:"nickname" binding:"required,min=2,max=32"`
	Content  string `json:"content"  binding:"required,min=2,max=500"`
	// Email 不是必填，所以没有 required。但一旦填了就要符合格式。
	// omitempty 是给 json 序列化用的：值为空时不出现在响应里。
	Email string `json:"email" binding:"omitempty,email"`
}

// CommentResp 是评论创建成功后的返回体。
//
// 为什么不直接把 Comment 返回给前端？
//
// 因为 Comment 将来会带 Email、IP 这类不该外泄的字段（真实项目里一定会加）。
// 把「请求结构」和「响应结构」分开定义，是最省事的防泄露手段——
// 你不需要记得排除字段，而是根本不会泄露。
type CommentResp struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	Content  string `json:"content"`
	// Status 表明评论当前处于何种状态。
	// 这里固定返回 "pending"，理由见 handler.CreateComment 的注释。
	Status string `json:"status"`
}
