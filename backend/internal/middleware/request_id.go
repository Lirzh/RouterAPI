package middleware

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// HeaderRequestID 请求追踪 ID 的 HTTP 头名（入站可携带，出站必回传）。
	HeaderRequestID = "X-Request-Id"
	// CtxRequestID gin.Context 中存放 request-id 的键。
	CtxRequestID = "request_id"
	// CtxUserAgent gin.Context 中存放归一化 User-Agent 的键。
	CtxUserAgent = "user_agent"

	maxRequestIDLen = 64
	maxUserAgentLen = 512
)

// ctxKey 用于 context.Context 传递 request-id（避免与其他包键冲突）。
type ctxKey struct{}

// RequestID 全链路请求追踪中间件：
// 接收客户端传入的 X-Request-Id（无则生成），写入 gin.Context、context.Context 与响应头。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderRequestID)
		if id == "" {
			id = uuid.New().String()
		}
		id = NormalizePersistentText(id, maxRequestIDLen)
		if id == "" {
			id = uuid.New().String()
		}

		c.Set(CtxRequestID, id)
		ua := NormalizePersistentText(c.Request.UserAgent(), maxUserAgentLen)
		c.Set(CtxUserAgent, ua)

		ctx := context.WithValue(c.Request.Context(), ctxKey{}, id)
		c.Request = c.Request.WithContext(ctx)

		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// GetRequestID 从 gin.Context 取 request-id。
func GetRequestID(c *gin.Context) string {
	return c.GetString(CtxRequestID)
}

// RequestIDFromContext 从 context.Context 取 request-id（供上游请求/日志使用）。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// GetUserAgent 取归一化后的客户端 User-Agent。
func GetUserAgent(c *gin.Context) string {
	return c.GetString(CtxUserAgent)
}

// NormalizePersistentText 归一化将要持久化/记录的文本：
// 截断到 maxBytes，并强制为合法 UTF-8，防止日志注入与畸形字节污染（借鉴 sub2api）。
func NormalizePersistentText(s string, maxBytes int) string {
	if s == "" {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	// 去掉可能干扰日志格式的控制字符（换行/回车）
	s = strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
	if maxBytes > 0 && len(s) > maxBytes {
		// 按字节截断后再次修正 UTF-8 边界
		s = strings.ToValidUTF8(s[:maxBytes], "")
	}
	return strings.TrimSpace(s)
}

// AbortWithError 以统一 JSON 结构中止请求。
func AbortWithError(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}
