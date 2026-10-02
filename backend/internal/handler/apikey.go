package handler

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"gorm.io/gorm"
)

// APIKeyHandler 对外调用密钥管理。
type APIKeyHandler struct {
	db *gorm.DB
}

func NewAPIKeyHandler(db *gorm.DB) *APIKeyHandler {
	return &APIKeyHandler{db: db}
}

// minAPIKeyLen 自定义密钥的最短长度。允许管理员填 "test" 就等于把 bearer 凭证退回可猜，
// 而库里只存摘要反而给人一种"已经安全了"的错觉。
const minAPIKeyLen = 24

// List API Key 列表。库里只有摘要与脱敏形态，明文无从返回。
func (h *APIKeyHandler) List(c *gin.Context) {
	var keys []model.APIKey
	h.db.Order("created_at DESC").Find(&keys)
	c.JSON(http.StatusOK, gin.H{"data": keys})
}

// Create 创建 API Key。明文只在本响应里出现一次，之后谁也拿不回来。
func (h *APIKeyHandler) Create(c *gin.Context) {
	var req model.APIKey
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "名称不能为空"})
		return
	}

	// 归属人：显式指定则用之，否则默认为当前操作者
	if req.OwnerUserID == 0 {
		req.OwnerUserID = c.GetUint("user_id")
	}

	secret := strings.TrimSpace(req.Key)
	if secret == "" {
		secret = "ra-" + generateRandomKey(32)
	} else if !strings.HasPrefix(secret, "ra-") {
		secret = "ra-" + secret
	}
	if len(secret) < minAPIKeyLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "自定义密钥至少需要 " + strconv.Itoa(minAPIKeyLen) + " 个字符"})
		return
	}
	if req.Status == 0 {
		req.Status = 1
	}

	// 只把摘要与脱敏形态交给 GORM；明文永不入库，也不进任何日志。
	req.Key = ""
	req.KeyHash = crypto.HashSecret(secret)
	req.KeyMasked = crypto.MaskString(secret)

	created := req
	if err := h.db.Create(&created).Error; err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "该密钥已存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}
	created.Key = secret
	c.JSON(http.StatusOK, gin.H{"data": created})
}

// apiKeyUpdatable 是更新允许改动的列。白名单而非透传请求体：
// 直接把 JSON map 交给 Updates，调用方就能写 key_hash/status 之类不该由请求决定的列。
var apiKeyUpdatable = []string{
	"name", "owner_user_id", "status", "credits_cap", "rate_limit_rpm",
	"allowed_vendors", "allowed_models", "allow_ips", "expires_at",
}

// Update 更新 API Key 的配置。密钥本身不可改——明文已经不存在了，
// 要换密钥就重建一条（旧的对立刻失效）。
func (h *APIKeyHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var key model.APIKey
	if err := h.db.First(&key, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	patch := make(map[string]interface{}, len(apiKeyUpdatable))
	for _, col := range apiKeyUpdatable {
		if v, ok := req[col]; ok {
			patch[col] = v
		}
	}
	if len(patch) > 0 {
		if err := h.db.Model(&key).Updates(patch).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "更新失败: " + err.Error()})
			return
		}
	}
	h.db.First(&key, id)
	c.JSON(http.StatusOK, gin.H{"data": key})
}

// Delete 删除 API Key。
func (h *APIKeyHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	h.db.Delete(&model.APIKey{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ============ 授权判定辅助（供代理层复用） ============

// parseStringList 解析 JSON 字符串数组；非法或空返回 nil。
func parseStringList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" || s == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// parseUintList 解析 JSON 数字数组为 uint 列表。
func parseUintList(s string) []uint {
	s = strings.TrimSpace(s)
	if s == "" || s == "[]" || s == "null" {
		return nil
	}
	var raw []uint
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	return raw
}

// KeyAllowsModel 判定 API Key 是否有权访问某虚拟模型。
// 授权规则：AllowedModels 命中模型名，或 AllowedVendors 命中该模型任一绑定供应商。
// 两者皆为空 = 允许全部（通配 "*" 亦允许全部）。
func KeyAllowsModel(k *model.APIKey, modelName string, boundVendorIDs []uint) bool {
	allowedModels := parseStringList(k.AllowedModels)
	allowedVendors := parseUintList(k.AllowedVendors)

	// 均为空 → 允许全部
	if len(allowedModels) == 0 && len(allowedVendors) == 0 {
		return true
	}
	for _, m := range allowedModels {
		if m == "*" || m == modelName {
			return true
		}
	}
	if len(allowedVendors) > 0 {
		vset := make(map[uint]bool, len(allowedVendors))
		for _, vid := range allowedVendors {
			vset[vid] = true
		}
		for _, vid := range boundVendorIDs {
			if vset[vid] {
				return true
			}
		}
	}
	return false
}

// KeyAllowsIP 判定客户端 IP 是否在 API Key 白名单内（空=不限，支持单 IP 与 CIDR）。
func KeyAllowsIP(k *model.APIKey, clientIP string) bool {
	allow := parseStringList(k.AllowIps)
	if len(allow) == 0 {
		return true
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}
	for _, entry := range allow {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, ipnet, err := net.ParseCIDR(entry); err == nil && ipnet.Contains(ip) {
				return true
			}
			continue
		}
		if net.ParseIP(entry) != nil && entry == ip.String() {
			return true
		}
	}
	return false
}
