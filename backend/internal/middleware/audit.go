package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// auditBodyMaxBytes 审计中间件缓冲请求体的上限（管理端 JSON 通常很小，超限截断仅用于提取白名单字段）。
const auditBodyMaxBytes = 64 * 1024

// auditExtraWhitelist 允许写入审计 Extra 的安全标量字段白名单。
// 绝不包含 password / api_key / key 等敏感字段。
var auditExtraWhitelist = []string{
	"name", "display_name", "protocol", "role", "status", "enabled",
	"model", "upstream_model", "base_url", "priority", "weight", "params",
	"amount", "type", "note", "max_rpm", "max_retry", "rate_limit_rpm",
	"credits_cap", "owner_user_id", "vendor_id", "auth_type",
}

// Audit 管理操作审计中间件：记录 admin 写操作（谁/何时/对哪个资源/做了什么/是否成功）。
// 采用请求体缓冲（读后还原）以便在 c.Next() 后从白名单字段提取 Extra，不影响下游绑定。
func Audit(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 缓冲请求体并还原，供下游 handler 正常读取
		var bodyBytes []byte
		if c.Request.Body != nil && (c.Request.Method == http.MethodPost || c.Request.Method == http.MethodPut || c.Request.Method == http.MethodPatch) {
			bodyBytes, _ = io.ReadAll(io.LimitReader(c.Request.Body, auditBodyMaxBytes))
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		c.Next()

		// 仅记录写操作（GET/HEAD/OPTIONS 不审计）
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return
		}

		fullPath := c.FullPath()
		if fullPath == "" {
			fullPath = c.Request.URL.Path
		}
		action := deriveAuditAction(c.Request.Method, fullPath)
		target := c.Param("id")
		if target == "" {
			target = c.Param("binding_id")
		}

		status := c.Writer.Status()
		entry := model.AuditLog{
			ActorUserID: c.GetUint("user_id"),
			ActorName:   c.GetString("username"),
			ActorRole:   c.GetString("role"),
			Action:      action,
			Method:      c.Request.Method,
			Path:        NormalizePersistentText(c.Request.URL.Path, 256),
			TargetID:    NormalizePersistentText(target, 64),
			RequestID:   GetRequestID(c),
			ClientIP:    c.ClientIP(),
			UserAgent:   GetUserAgent(c),
			StatusCode:  status,
			Success:     status >= 200 && status < 400,
			Extra:       buildAuditExtra(bodyBytes),
		}
		if err := db.Create(&entry).Error; err != nil {
			logrus.Errorf("写入审计日志失败: %v", err)
		}
	}
}

// deriveAuditAction 由 HTTP 方法 + 路由模式推导可读的审计动作（如 vendor.create / model.delete）。
func deriveAuditAction(method, fullPath string) string {
	p := strings.TrimPrefix(fullPath, "/api/admin/")
	segs := strings.Split(p, "/")
	var words []string
	for _, s := range segs {
		if s == "" || strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			continue
		}
		words = append(words, s)
	}
	verb := map[string]string{
		http.MethodPost: "create", http.MethodPut: "update",
		http.MethodPatch: "update", http.MethodDelete: "delete",
	}[method]
	if verb == "" {
		verb = strings.ToLower(method)
	}
	if len(words) == 0 {
		return verb
	}
	resource := singularResource(words[0])
	if len(words) >= 2 {
		last := words[len(words)-1]
		switch last {
		case "bindings":
			resource = "binding"
		case "import-models":
			return "model.import"
		case "reset-password":
			return resource + ".reset_password"
		case "credits":
			return resource + ".credits"
		case "recompute":
			return resource + ".recompute"
		case "test", "enable", "disable":
			return resource + "." + last
		}
	}
	return resource + "." + verb
}

// singularResource 将资源段规范化为单数下划线形式（api-keys -> api_key，vendors -> vendor）。
func singularResource(w string) string {
	w = strings.ReplaceAll(w, "-", "_")
	if w == "stats" {
		return "stats"
	}
	w = strings.TrimSuffix(w, "s")
	return w
}

// buildAuditExtra 从缓冲请求体中按白名单提取安全标量字段，脱敏截断后序列化为 JSON。
func buildAuditExtra(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	out := make(map[string]interface{}, len(auditExtraWhitelist))
	for _, k := range auditExtraWhitelist {
		v, ok := raw[k]
		if !ok {
			continue
		}
		switch tv := v.(type) {
		case string:
			out[k] = NormalizePersistentText(tv, 128)
		case bool, float64, int, int64:
			out[k] = tv
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}
