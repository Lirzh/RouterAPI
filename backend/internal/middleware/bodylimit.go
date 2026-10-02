package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimit 给所有入站请求体设上限。
//
// 网关必须假定客户端会发一个自称 1KB、实际无穷大的 body（或干脆不声明 Content-Length 一直喂）。
// 消费者侧尤其致命：/v1/chat/completions 的多模态请求本来就要收几十 MB 的 base64 图片，
// 所以"看起来很大"是被允许的，不设限就是任意大小的内存放大。
//
// 超限后读 body 会得到 *http.MaxBytesError，由读方决定回什么状态码
// （代理路由回 413，管理端因绑定失败回 400）。
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes > 0 && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
