// Package response 定义统一响应体、错误码与业务错误类型。
//
// 为什么值得单独建一个包？
//
// 因为「响应格式」是被整个项目共享的契约：每个 handler 都要用它，
// 前端每个页面都依赖它。这种「被所有人依赖」的东西必须集中定义，
// 否则你今天写 {"error": "..."}、明天写 {"msg": "..."}，
// 前端就得为每个接口写一套解析逻辑。
//
// 这个包刻意不依赖项目里任何一个内部包——它只依赖 gin 和标准库。
// 这样将来抽成公共库给别的项目用时，不需要连带搬走一堆东西。
package response

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Body 是所有接口的统一响应结构。
//
// 三个字段的分工要说清楚，因为它们经常被混用：
//
//	Code    给程序看的：前端靠它判断「是哪种失败」，它的取值是稳定的、不应随意变
//	Message 给人看的：用户/开发者读的提示，可能出现措辞变化
//	Data    成功时的载荷
//
// 为什么 HTTP 状态码之外还需要 Code？
//
// 因为 HTTP 状态码只有几十个，而业务错误的种类远多于此。
// 例如「文章不存在」和「评论不存在」都是 404，前端如果想分别展示
// 「去打草稿箱看看」和「刷新页面重试」，就必须靠 Code 区分。
//
// 反之，只有 Code 也不行：HTTP 状态码是给代理、监控、网关、浏览器
// 这些**不理解你业务**的中间层看的。5xx 会触发告警，404 会被 CDN 缓存，
// 401 会让客户端跳登录页——这些行为都建立在状态码上。
type Body struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// 错误码。
//
// 分段的意义：看到数字就知道问题性质，不用去查文档。
//
//	0      成功
//	1xxx   参数问题（客户端改请求可能解决）
//	2xxx   认证问题（需要登录）
//	3xxx   权限问题（已登录但无权）
//	4xxx   业务问题（资源不存在、状态冲突）
//	5xxx   服务端问题（客户端无能为力，重试可能有用）
const (
	CodeOK = 0

	CodeInvalidParam = 1001
	CodeMissingField = 1002

	CodeUnauthorized = 2001

	CodeForbidden = 3001

	CodeNotFound      = 4001
	CodeAlreadyExists = 4002
	CodeCommentClosed = 4003

	CodeInternal = 5001
	CodeDatabase = 5002
)

// AppError 是贯穿各层的业务错误。
//
// 关键在于它有两个「消息」字段，而且它们的去向完全不同：
//
//	Message 会写进响应体，**用户能看到**，所以必须是人话、且不泄露内部细节
//	Err     只写日志，**永不外泄**，所以可以放 SQL 语句、连接串、堆栈
//
// 把这两者放进同一个类型，是为了避免最常见的那种事故：
// 某个分支顺手写了 c.JSON(500, gin.H{"error": err.Error()})，
// 于是数据库地址、表结构、内部路径全都跑到了用户屏幕上。
type AppError struct {
	HTTPStatus int
	Code       int
	Message    string
	Err        error
}

// Error 实现 error 接口。
//
// 注意它返回的是**对外**的 Message 加上内部错误——
// 这个字符串只应该出现在服务端日志里，永远不会被写进响应体。
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap 让 errors.Is / errors.As 能继续向下匹配内部错误。
//
// 典型用途：service 层返回 response.ErrNotFound.Wrap(gorm.ErrRecordNotFound)，
// 上层可以用 errors.Is(err, gorm.ErrRecordNotFound) 判断具体原因，
// 而不必依赖具体的 AppError 实例。
func (e *AppError) Unwrap() error { return e.Err }

// Wrap 附加内部错误，返回一个**新**的 AppError。
//
// 为什么不直接改 e.Err？
// 因为下面那些 Err* 变量是包级共享的。如果 Wrap 修改原对象，
// 一次请求的错误会粘到全局变量上，之后所有请求都会带上它——
// 这种 bug 在多 goroutine 下极难复现。返回新对象就没有这个问题。
func (e *AppError) Wrap(err error) *AppError {
	if err == nil {
		return e
	}
	return &AppError{HTTPStatus: e.HTTPStatus, Code: e.Code, Message: e.Message, Err: err}
}

// New 构造一个自定义错误。
func New(httpStatus, code int, message string) *AppError {
	return &AppError{HTTPStatus: httpStatus, Code: code, Message: message}
}

// 预定义错误。它们是「模板」，通过 Wrap 派生具体错误。
var (
	ErrInvalidParam = New(http.StatusBadRequest, CodeInvalidParam, "请求参数不合法")
	ErrMissingField = New(http.StatusBadRequest, CodeMissingField, "缺少必填字段")

	ErrUnauthorized = New(http.StatusUnauthorized, CodeUnauthorized, "请先登录")

	ErrForbidden = New(http.StatusForbidden, CodeForbidden, "没有权限执行此操作")

	ErrNotFound      = New(http.StatusNotFound, CodeNotFound, "资源不存在")
	ErrAlreadyExists = New(http.StatusConflict, CodeAlreadyExists, "资源已存在")
	ErrCommentClosed = New(http.StatusForbidden, CodeCommentClosed, "该文章的评论已关闭")

	ErrInternal = New(http.StatusInternalServerError, CodeInternal, "服务器内部错误，请稍后重试")
	ErrDatabase = New(http.StatusInternalServerError, CodeDatabase, "数据操作失败")
)

// OK 返回成功响应。
//
// 用 AbortWithStatusJSON 还是 JSON？
// 成功路径用 JSON 就够（后面没有别的 handler 要执行）。
// 失败路径必须用 AbortWithStatusJSON，见 Fail 的注释。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{Code: CodeOK, Message: "ok", Data: data})
}

// Created 返回 201，用于「创建了资源」的场景。
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Body{Code: CodeOK, Message: "created", Data: data})
}

// Fail 返回错误响应。
//
// 这里有三个关键动作，缺一个都会出问题：
//
//  1. 只把 AppError.Message 写进响应体。
//     err 的原始内容（可能是 SQL、可能是连接串）只进日志。
//
//  2. 用 AbortWithStatusJSON 而不是 JSON。
//     它的额外作用是 Abort：把当前请求的中间件链标记为「已结束」，
//     后续中间件和 handler 不会再执行（Step 6 会看到实际效果）。
//     只用 JSON 的话，如果你忘了在 handler 里 return，代码会继续往下跑，
//     然后第二次写响应——gin 会打印 "superfluous response.WriteHeader call"，
//     响应体变成两段 JSON 拼接，前端解析直接失败。
//
//  3. 4xx 和 5xx 用不同的日志级别。理由见 logIfNeeded。
func Fail(c *gin.Context, err error) {
	appErr := From(err)
	logIfNeeded(c, appErr)

	// AbortWithStatusJSON 在写入响应后会调用 c.Abort()。
	c.AbortWithStatusJSON(appErr.HTTPStatus, Body{
		Code:    appErr.Code,
		Message: appErr.Message,
		Data:    nil,
	})
}

// From 把任意 error 归一化成 *AppError。
//
// 为什么需要它：下层可能返回各种错误（GORM 的、标准库的、自定义的），
// handler 不可能为每种都写一个分支。统一在这里转换，
// 未知错误一律降级成 500 + 「服务器内部错误」，原始错误保留在 Err 字段里进日志。
//
// 这样有个重要的安全性质：**即使某个服务层同事忘了包装错误，也不会泄露内部信息**，
// 因为默认路径就是不外泄。
func From(err error) *AppError {
	if err == nil {
		return ErrInternal
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}

	// 未知错误：不把原始内容返回给客户端。
	return ErrInternal.Wrap(err)
}

// logIfNeeded 记日志，并按状态码分级。
//
// 为什么 4xx 用普通日志、5xx 才用「错误」？
//
// 因为 4xx 是**正常业务流转**的一部分：用户输错密码、请求不存在的文章、
// 参数填错——这些每天都在发生，数量可能远超成功请求。
// 如果它们都算「错误日志」，真正的故障（数据库连不上）会被淹没在噪音里，
// 告警也会因为误报太多而被无视。
//
// 只应该让 5xx（服务端自己的问题）触发告警。
func logIfNeeded(c *gin.Context, e *AppError) {
	// 没有内部错误可记的（比如纯粹的业务拒绝），不必打日志。
	if e.Err == nil {
		return
	}

	msg := fmt.Sprintf("%s %s -> %d/%d: %v",
		c.Request.Method, c.Request.URL.Path, e.HTTPStatus, e.Code, e.Err)

	if e.HTTPStatus >= 500 {
		log.Printf("[ERROR] %s", msg)
		return
	}
	log.Printf("[WARN] %s", msg)
}

// Page 是分页数据的统一结构。
//
// 用泛型（[T any]）而不是 []any：handler 拿到的就是 []Post 而不是一堆类型断言，
// 出错概率低得多。Go 1.18+ 支持。
type Page[T any] struct {
	List     []T   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	TotalPage int  `json:"total_page"`
}

// NewPage 组装分页结构，并自动算出总页数。
//
// list 为 nil 时替换成空切片：JSON 序列化时输出 [] 而不是 null。
// 这个细节很重要——前端拿到 null 去 .map() 会直接崩，拿到 [] 则不会有问题。
func NewPage[T any](list []T, total int64, page, pageSize int) Page[T] {
	if list == nil {
		list = make([]T, 0)
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	totalPage := 0
	if total > 0 {
		totalPage = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	return Page[T]{List: list, Total: total, Page: page, PageSize: pageSize, TotalPage: totalPage}
}
