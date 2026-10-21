package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"ginlesson/step04/pkg/response"
)

// BindJSON 绑定 JSON 请求体，并把校验失败翻译成**给用户看**的错误。
//
// 为什么不能直接 return err？
//
// 因为 gin 的绑定错误长这样：
//
//	Key: 'Comment.Nickname' Error:Field validation for 'Nickname' failed on the 'min' tag
//
// 把它塞进响应体有三个问题：
//  1. 用户看不懂（这是给开发者看的）
//  2. 泄露了内部结构（结构体名、字段名、校验规则）
//  3. 前端无法用程序处理（要拿正则去抠字段名）
//
// 正确的做法是：内部保留原始 error（进日志），对外给一句人话 + 字段名。
func BindJSON(c *gin.Context, dst any) *response.AppError {
	if err := c.ShouldBindJSON(dst); err != nil {
		return translateBindError(err)
	}
	return nil
}

// translateBindError 把 validator 的错误翻译成可读文案。
//
// 这里只处理最常见的两种情况。生产项目里更完整的做法是用
// validator 的 universal-translator 注册中文翻译器，
// 但那个包配置较长，会盖住本步的重点（响应结构），所以这里手写。
func translateBindError(err error) *response.AppError {
	// 请求体不是合法 JSON（多了个逗号、少了个引号……）
	// 这类错误是 *json.SyntaxError 或 *json.UnmarshalTypeError，
	// 用 errors.As 拿不到 validator 的类型，交给分支末尾兜底。
	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) && len(validationErrs) > 0 {
		// 只报告第一个错误。
		//
		// 为什么不全报？因为用户一次只可能改一处，列出一屏错误没有帮助，
		// 反而让人不知道从哪下手。前端逐字段展示时再逐个校验即可。
		first := validationErrs[0]

		// Field() 返回 Go 结构体字段名（Nickname）。
		// 转小写让它看起来像参数名，和 json tag 的命名习惯一致。
		// 注意对 PageSize 这种大驼峰字段会得到 "pagesize"，
		// 与 json tag 的 "page_size" 不完全一致——这是本项目简化的代价，
		// 要严格一致的话应该反射读 json tag（见 NOTES 里的说明）。
		field := strings.ToLower(first.Field())

		msg := describeRule(field, first)
		return response.New(
			http.StatusBadRequest,
			response.CodeInvalidParam,
			msg,
		).Wrap(err)
	}

	// 兜底：JSON 语法错误等。
	return response.New(
		http.StatusBadRequest,
		response.CodeInvalidParam,
		"请求体格式不正确，应为合法的 JSON",
	).Wrap(err)
}

// describeRule 把 validator 的规则名翻译成人话。
//
// 规则名是给程序看的（min、max、required），而 message 是给用户看的。
// 直接拼接成"不满足 min 规则"虽然能用，但用户不知道 min 是什么。
func describeRule(field string, fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "缺少必填参数：" + field
	case "min":
		// 对字符串，Param() 是字符数；对数字，是数值下限。
		// 这里不区分类型，用「长度/大小」统一表述。
		if _, ok := fe.Value().(string); ok {
			return field + " 长度不能少于 " + fe.Param() + " 个字符"
		}
		return field + " 不能小于 " + fe.Param()
	case "max":
		if _, ok := fe.Value().(string); ok {
			return field + " 长度不能超过 " + fe.Param() + " 个字符"
		}
		return field + " 不能大于 " + fe.Param()
	case "email":
		return field + " 不是合法的邮箱地址"
	default:
		return field + " 不合法（" + fe.Tag() + "）"
	}
}
