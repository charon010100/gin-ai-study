package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"ginlesson/step09/internal/service"
	"ginlesson/step09/pkg/response"
)

// 本文件是「业务错误 → HTTP 状态码」的唯一映射点。
//
// 为什么要集中在一处：
// 如果每个 handler 里各写一套 switch，三个月后
// 「NOT_FOUND 应该返回 404 还是 200」的答案会有三个版本。
// 更常见的情况是：新加的错误码在某个 handler 里忘了处理，
// 于是悄悄变成了 500。
//
// 集中映射的另一个好处：你能一眼看完整个 API 的错误语义。
func mapServiceError(c *gin.Context, err error) {
	var bizErr *service.Error
	if !errors.As(err, &bizErr) {
		// 不是业务错误 = 代码里漏了转换。
		// 这种情况一律 500 并记录原始错误，绝不把 err.Error() 返回客户端。
		response.ServerError(c, err)
		return
	}

	switch bizErr.Code {
	case service.CodeNotFound:
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, bizErr.Message)

	case service.CodeConflict:
		// 409 而不是 400：请求本身没问题，是和现有资源冲突。
		// 这个区分对客户端有用 —— 400 该提示用户改输入，
		// 409 该提示「这个标识已被占用」。
		response.Fail(c, http.StatusConflict, response.CodeConflict, bizErr.Message)

	case service.CodeInvalidArgument:
		response.Fail(c, http.StatusBadRequest, response.CodeInvalid, bizErr.Message)

	case service.CodeForbidden:
		response.Fail(c, http.StatusForbidden, response.CodeForbidden, bizErr.Message)

	case service.CodeUnavailable:
		// 503 而不是 500：告诉调用方「依赖暂时不可用，可以重试」。
		response.Fail(c, http.StatusServiceUnavailable, response.CodeUnavailable, bizErr.Message)

	default:
		// CodeInternal 以及将来新增但忘了处理的码。
		//
		// 注意这里要把 Cause 传给日志：bizErr.Error() 里含 Cause，
		// 而 response.ServerError 只把 err 写日志、不返回给客户端。
		response.ServerError(c, bizErr)
	}
}

// bindQuery 是 ShouldBindQuery 的统一包装。
func bindQuery(c *gin.Context, dst any) bool {
	if err := c.ShouldBindQuery(dst); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalid, "查询参数不合法")
		return false
	}
	return true
}

// bindJSON 是 ShouldBindJSON 的统一包装。
//
// 用 Should 而不是 Bind：后者会在失败时自动写响应并 abort，
// 你无法控制格式，也无法统一成 response.Body。
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalid, "请求体格式不正确")
		return false
	}
	return true
}
