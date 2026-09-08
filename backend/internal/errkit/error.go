package errkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 错误类型：决定默认是否重试与如何渲染。
const (
	TypeClient   = "client"   // 客户端错误（请求本身有问题，换供应商也无用）
	TypeUpstream = "upstream" // 上游返回的错误
	TypeChannel  = "channel"  // 供应商/渠道级错误（key 失效、限流、过载等，应换供应商重试）
	TypeInternal = "internal" // 网关内部错误
)

// channelCodePrefix 带此前缀的 ErrorCode 一律触发重试（借鉴 new-api 的 "channel:*" 约定）。
const channelCodePrefix = "channel:"

// APIError 统一错误载体：贯穿路由、重试判定、日志与按协议渲染。
type APIError struct {
	StatusCode int    // HTTP 状态码
	ErrorType  string // client|upstream|channel|internal
	ErrorCode  string // 稳定错误码
	Message    string // 面向用户的消息
	Err        error  // 底层错误
	SkipRetry  bool   // 显式标记不重试
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.ErrorCode
}

func (e *APIError) Unwrap() error { return e.Err }

// WithSkipRetry 标记该错误不参与重试。
func (e *APIError) WithSkipRetry() *APIError {
	e.SkipRetry = true
	return e
}

// ============ 构造函数 ============

// New 构造一个 APIError。
func New(statusCode int, errType, code, message string, err error) *APIError {
	return &APIError{StatusCode: statusCode, ErrorType: errType, ErrorCode: code, Message: message, Err: err}
}

// ClientError 客户端错误（默认不重试）。
func ClientError(statusCode int, message string) *APIError {
	return &APIError{StatusCode: statusCode, ErrorType: TypeClient, ErrorCode: "invalid_request", Message: message, SkipRetry: true}
}

// UpstreamError 上游错误（是否重试由状态码策略决定）。
func UpstreamError(statusCode int, message string, err error) *APIError {
	return &APIError{StatusCode: statusCode, ErrorType: TypeUpstream, ErrorCode: "upstream_error", Message: message, Err: err}
}

// ChannelError 供应商级错误（触发重试；code 自动加 channel: 前缀）。
func ChannelError(statusCode int, message string, err error) *APIError {
	code := "channel:error"
	return &APIError{StatusCode: statusCode, ErrorType: TypeChannel, ErrorCode: code, Message: message, Err: err}
}

// InternalError 网关内部错误。
func InternalError(message string, err error) *APIError {
	return &APIError{StatusCode: 500, ErrorType: TypeInternal, ErrorCode: "internal_error", Message: message, Err: err}
}

// FromError 将任意 error 归一为 APIError；已是 APIError 则原样返回。
func FromError(err error) *APIError {
	if err == nil {
		return nil
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return &APIError{StatusCode: 500, ErrorType: TypeInternal, ErrorCode: "internal_error", Message: err.Error(), Err: err}
}

// ============ 重试判定 ============

// IsChannelError 是否为供应商级错误（应换供应商重试）。
func IsChannelError(err error) bool {
	ae := FromError(err)
	if ae == nil {
		return false
	}
	return ae.ErrorType == TypeChannel || strings.HasPrefix(ae.ErrorCode, channelCodePrefix)
}

// alwaysSkipRetryStatusCodes 永不重试的状态码：网关超时类，重试只会放大延迟。
var alwaysSkipRetryStatusCodes = map[int]bool{504: true, 524: true}

// retryStatusRanges 默认可重试状态码区间（借鉴 new-api）：
// 400（请求本身错误）与 408（请求超时）不在区间内，故不重试。
var retryStatusRanges = [][2]int{
	{100, 199}, {300, 399}, {401, 407}, {409, 499}, {500, 503}, {505, 523}, {525, 599},
}

// ShouldRetryByStatusCode 依据状态码判断是否值得重试。
func ShouldRetryByStatusCode(code int) bool {
	if alwaysSkipRetryStatusCodes[code] {
		return false
	}
	if code >= 200 && code < 300 {
		return false
	}
	if code < 100 || code > 599 {
		// 非标准 HTTP 码（多为网络层错误），重试
		return true
	}
	for _, r := range retryStatusRanges {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

// ShouldRetry 综合判定是否应换供应商重试。
func ShouldRetry(err error) bool {
	ae := FromError(err)
	if ae == nil {
		return false
	}
	if IsChannelError(ae) {
		return true
	}
	if ae.SkipRetry {
		return false
	}
	return ShouldRetryByStatusCode(ae.StatusCode)
}

// ============ 按协议渲染错误响应 ============

// openAIErrorType 由状态码推导 OpenAI 风格 error.type。
func openAIErrorType(code int) string {
	switch {
	case code == 401:
		return "authentication_error"
	case code == 403:
		return "permission_error"
	case code == 404:
		return "not_found_error"
	case code == 429:
		return "rate_limit_error"
	case code >= 500:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}

// ToOpenAIError 渲染 OpenAI 兼容错误：{"error":{"message","type","param","code"}}。
func (e *APIError) ToOpenAIError() map[string]interface{} {
	msg := e.Error()
	return map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    openAIErrorType(e.StatusCode),
			"param":   nil,
			"code":    e.ErrorCode,
		},
	}
}

// claudeErrorType 由状态码推导 Anthropic 风格 error.type。
func claudeErrorType(code int) string {
	switch {
	case code == 401:
		return "authentication_error"
	case code == 403:
		return "permission_error"
	case code == 404:
		return "not_found_error"
	case code == 413:
		return "request_too_large"
	case code == 429:
		return "rate_limit_error"
	case code == 529:
		return "overloaded_error"
	case code >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

// ToClaudeError 渲染 Anthropic 兼容错误：{"type":"error","error":{"type","message"}}。
func (e *APIError) ToClaudeError() map[string]interface{} {
	return map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"type":    claudeErrorType(e.StatusCode),
			"message": e.Error(),
		},
	}
}

// geminiStatus 由状态码推导 Google API 风格 status 枚举。
func geminiStatus(code int) string {
	switch {
	case code == 400:
		return "INVALID_ARGUMENT"
	case code == 401:
		return "UNAUTHENTICATED"
	case code == 403:
		return "PERMISSION_DENIED"
	case code == 404:
		return "NOT_FOUND"
	case code == 429:
		return "RESOURCE_EXHAUSTED"
	case code >= 500 && code < 504:
		return "INTERNAL"
	case code == 504:
		return "DEADLINE_EXCEEDED"
	default:
		return "INTERNAL"
	}
}

// ToGeminiError 渲染 Gemini 兼容错误：{"error":{"code","message","status"}}。
func (e *APIError) ToGeminiError() map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    e.StatusCode,
			"message": e.Error(),
			"status":  geminiStatus(e.StatusCode),
		},
	}
}

// Render 按入站协议渲染错误体。protocol 取值：openai|anthropic|gemini。
func (e *APIError) Render(protocol string) map[string]interface{} {
	switch protocol {
	case "anthropic", "claude":
		return e.ToClaudeError()
	case "gemini":
		return e.ToGeminiError()
	default:
		return e.ToOpenAIError()
	}
}

// MessageWithRequestID 将 request-id 拼进消息，便于用户报障定位。
func MessageWithRequestID(msg, requestID string) string {
	if requestID == "" {
		return msg
	}
	return fmt.Sprintf("%s (request_id: %s)", msg, requestID)
}

// ============ 上游错误分类 ============

// ClassifyUpstream 依据上游 HTTP 状态码将错误分类为 channel/upstream，驱动重试与熔断：
//   - 401/403/429/5xx：供应商级问题（凭证失效、限流、上游故障）→ 换供应商重试 + 累计熔断
//   - 504/524：网关超时 → 不重试（避免放大延迟），但仍累计熔断
//   - 400/404/422：请求本身问题 → 不重试
//   - 其余：按状态码策略决定
func ClassifyUpstream(statusCode int, body string) *APIError {
	msg := ExtractUpstreamMessage(body, statusCode)
	switch {
	case statusCode == 504 || statusCode == 524:
		return UpstreamError(statusCode, msg, nil).WithSkipRetry()
	case statusCode == 401 || statusCode == 403 || statusCode == 429:
		return ChannelError(statusCode, msg, nil)
	case statusCode >= 500:
		return ChannelError(statusCode, msg, nil)
	case statusCode == 400 || statusCode == 404 || statusCode == 422:
		return UpstreamError(statusCode, msg, nil).WithSkipRetry()
	default:
		return UpstreamError(statusCode, msg, nil)
	}
}

// ExtractUpstreamMessage 尽力从上游错误响应体中提取人类可读消息，
// 兼容 OpenAI/Anthropic/Gemini 三种常见错误结构；失败则回退到截断的原始 body。
func ExtractUpstreamMessage(body string, statusCode int) string {
	body = strings.TrimSpace(body)
	if body != "" {
		// {"error":{"message":"..."}}  (OpenAI / Anthropic / Gemini 均可能)
		var wrapper struct {
			Error struct {
				Message string `json:"message"`
				Code    interface{} `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &wrapper); err == nil && wrapper.Error.Message != "" {
			return wrapper.Error.Message
		}
		// {"message":"..."}
		var simple struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(body), &simple); err == nil && simple.Message != "" {
			return simple.Message
		}
		if len(body) > 512 {
			body = body[:512]
		}
		return fmt.Sprintf("上游返回错误 %d: %s", statusCode, body)
	}
	return fmt.Sprintf("上游返回错误 %d", statusCode)
}
