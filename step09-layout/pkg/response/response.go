// Package response 定义统一响应体。
//
// 为什么放在 pkg/ 而不是 internal/：
// pkg/ 在 Go 社区约定里表示「可以被其他项目复用」的包，
// internal/ 表示「只在本项目内使用」。
// 响应格式这种东西，如果你将来写第二个服务，是想复用的；
// 而 service/repository 是业务专属的，不该被别的项目引用。
//
// 需要说明的是：这个约定没有编译器强制（不像 internal/）。
// 放错位置不会报错，只会让依赖关系变模糊。
package response

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Body 是统一响应体。
//
// 字段设计的三个决定：
//   - code 用字符串而不是数字：可读、可搜索、前端不需要维护数字映射表
//   - request_id 回传：用户报错时截图就能定位到日志
//   - data 用 any：所有接口共用同一套解析逻辑
type Body struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id,omitempty"`
}

// 业务码常量。
//
// 与 service.Code 的字符串保持一致，但**不 import service**：
// response 是通用工具包，不该依赖业务包。
// 这种「重复」是刻意的 —— 它保证了 pkg/ 的独立性。
// 如果你觉得两处维护容易出错，说明这两个常量本来就该合并，
// 那时应该把码定义在 pkg/ 里，让 service 来引用它。
const (
	CodeOK             = "OK"
	CodeNotFound       = "NOT_FOUND"
	CodeConflict       = "CONFLICT"
	CodeInvalid        = "INVALID_ARGUMENT"
	CodeForbidden      = "FORBIDDEN"
	CodeUnauthorized   = "UNAUTHORIZED"
	CodeInternal       = "INTERNAL"
	CodeUnavailable    = "UNAVAILABLE"
)

// ContextKeyRequestID 是存放 request_id 的 gin context key。
const ContextKeyRequestID = "request_id"

// OK 返回 200。
func OK(c *gin.Context, data any) {
	write(c, http.StatusOK, CodeOK, "ok", data)
}

// Created 返回 201。
func Created(c *gin.Context, data any) {
	write(c, http.StatusCreated, CodeOK, "created", data)
}

// NoContent 返回 204。
//
// 注意 204 不允许有 body，所以这里不写 JSON。
// 写了会产生 "superfluous response.WriteHeader call" 警告，
// 而且客户端可能解析到一个空 body 而报错。
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Fail 返回业务失败。
func Fail(c *gin.Context, status int, code, message string) {
	write(c, status, code, message, nil)
	c.Abort()
}

// ServerError 返回 500，并把真实错误写进日志。
//
// 安全要点：错误详情**只进日志**。
// 数据库错误里常带表名、列名甚至 SQL 片段，
// 回给客户端等于给攻击者画图纸。
func ServerError(c *gin.Context, err error) {
	log.Printf("[ERROR] request_id=%s path=%s err=%v",
		requestID(c), c.Request.URL.Path, err)
	write(c, http.StatusInternalServerError, CodeInternal, "服务暂时不可用，请稍后再试", nil)
	c.Abort()
}

func write(c *gin.Context, status int, code, message string, data any) {
	c.JSON(status, Body{
		Code:      code,
		Message:   message,
		Data:      data,
		RequestID: requestID(c),
	})
}

func requestID(c *gin.Context) string {
	if v, ok := c.Get(ContextKeyRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
