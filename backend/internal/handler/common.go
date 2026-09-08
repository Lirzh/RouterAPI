package handler

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/pkg/crypto"
)

// CryptoSetKey 设置 AES-GCM 加密密钥（供 main/router 调用）。
func CryptoSetKey(key string) {
	crypto.SetKey(key)
}

// generateRandomKey 生成 length 字节的十六进制随机串。
func generateRandomKey(length int) string {
	b := make([]byte, length)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pagination 解析分页参数，返回 page、pageSize（已夹取到合法范围）。
func pagination(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return
}

// escapeLike 转义 LIKE 通配符（\、%、_），配合 ESCAPE '\' 使用，
// 防止用户输入被当作通配符导致全表扫描或语义偏差。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
