// Package service 承载业务规则，并定义业务错误。
//
// 这一层是「HTTP 与数据库之间的那层」吗？不准确。
// 更准确的说法是：这一层是**唯一知道业务规则的地方**。
//
// 判断标准：
//   - 需要 HTTP 状态码？不属于这里（属于 handler）
//   - 需要知道 SQL？不属于这里（属于 repository）
//   - 关心「评论必须先审后发」「草稿不能被公开列表返回」？属于这里
package service

import (
	"errors"
	"fmt"
)

// Code 是业务错误码。
//
// 为什么业务层要有自己的错误码，而不是直接返回 HTTP 状态码：
//
// 如果 service 返回 404，那它就知道了 HTTP 的存在，于是
//   - 你将来做 gRPC 接口时，这个 service 没法复用
//   - 定时任务里调用它，收到一个「404」毫无意义
//
// 业务码表达的是「发生了什么」，HTTP 状态码是「怎么告诉客户端」。
// 这两件事的映射应该放在 handler 层（见 handler/errors.go）。
type Code string

const (
	// CodeNotFound 目标资源不存在。
	CodeNotFound Code = "NOT_FOUND"
	// CodeConflict 与现有状态冲突（比如 slug 重复）。
	CodeConflict Code = "CONFLICT"
	// CodeInvalidArgument 参数在业务层面不合法。
	CodeInvalidArgument Code = "INVALID_ARGUMENT"
	// CodeForbidden 业务规则不允许该操作（比如评论功能关闭）。
	CodeForbidden Code = "FORBIDDEN"
	// CodeInternal 未预期的内部错误。
	CodeInternal Code = "INTERNAL"

	// —— 以下两个是「技术性错误」，由 repository 触发、service 内部转换 ——
	// 把它们放在同一套码里，是为了让 handler 只需要处理一种错误类型。

	// CodeUnavailable 依赖不可用（数据库/缓存故障）。
	CodeUnavailable Code = "UNAVAILABLE"
)

// Error 是业务错误。
//
// 它同时承载「给用户看的消息」和「给运维看的内部原因」：
//   - Message 可以安全地返回给客户端
//   - Cause 只写日志，绝不出现在响应里
//
// 把这两者分开，是避免"数据库密码错误被返回给用户"这类事故的根本办法。
type Error struct {
	Code    Code
	Message string
	// Cause 是内部原因，可为 nil。
	Cause error
}

// Error 实现 error 接口。
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 让 errors.Is/As 能穿透到 Cause。
func (e *Error) Unwrap() error { return e.Cause }

// NewError 构造业务错误。
func NewError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// ---------- 预定义错误 ----------
//
// 这些是「可复用的哨兵值」。注意用函数而不是包级变量：
// 包级变量是可变的，某个调用方一旦改了 Message，所有调用方都会受影响。
// 用函数每次返回新实例，杜绝这种"共享状态被意外修改"的问题。
//
// 代价是每次调用会分配一次内存。对个人博客来说完全可以忽略；
// 如果这是每秒百万次的路径，可以用包级变量 + 只读约定。

// ErrPostNotFound 文章不存在。
func ErrPostNotFound() *Error {
	return &Error{Code: CodeNotFound, Message: "文章不存在"}
}

// ErrCommentNotFound 评论不存在或状态不允许该操作。
func ErrCommentNotFound() *Error {
	return &Error{Code: CodeNotFound, Message: "评论不存在，或已不是待审核状态"}
}

// ErrSlugConflict slug 已被占用。
func ErrSlugConflict() *Error {
	return &Error{Code: CodeConflict, Message: "slug 已被占用，换一个"}
}

// ErrInvalidStatus 状态值非法。
func ErrInvalidStatus() *Error {
	return &Error{Code: CodeInvalidArgument, Message: "status 取值不合法"}
}

// ErrInvalidPageSize 分页参数越界。
func ErrInvalidPageSize() *Error {
	return &Error{Code: CodeInvalidArgument, Message: "page_size 超出允许范围"}
}

// ErrCommentClosed 评论功能已关闭。
func ErrCommentClosed() *Error {
	return &Error{Code: CodeForbidden, Message: "该文章的评论已关闭"}
}

// ---------- 跨层转换 ----------

// FromRepoError 把 repository 层的语义错误转换成本层的业务错误。
//
// 为什么需要这个转换函数，而不是让 repository 直接返回 *service.Error：
//
// repository 不该 import service（那是反向依赖）。
// 它只能返回自己定义的哨兵错误（model.ErrNotFound 之类）。
// 由 service 来「翻译」，是依赖方向正确的做法：
//
//	service → repository → model
//
// 这也解释了为什么这个函数在 service 包里而不是 repository 包里。
func FromRepoError(err error) *Error {
	if err == nil {
		return nil
	}

	// 已经是业务错误就直接透传（比如 service 自己校验产生的）。
	var bizErr *Error
	if errors.As(err, &bizErr) {
		return bizErr
	}

	// 其他一切错误都归为内部错误：
	// 数据库连不上、SQL 语法错误、驱动 panic 后的包装……
	// 这些都不该让用户看到原文。
	//
	// 注意 Message 用的是固定文案而不是 err.Error()：
	// 一旦把内部错误拼进 Message，就等于泄露了表名/列名/SQL 片段。
	return &Error{
		Code:    CodeInternal,
		Message: "服务暂时不可用，请稍后再试",
		Cause:   err,
	}
}
