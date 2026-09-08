package handler

import (
	"encoding/json"
	"net"
	"net/http"
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

func maskAPIKey(k *model.APIKey) {
	if k.Key != "" {
		k.KeyMasked = crypto.MaskString(k.Key)
		k.Key = ""
	}
}

// List API Key 列表（脱敏）。
func (h *APIKeyHandler) List(c *gin.Context) {
	var keys []model.APIKey
	h.db.Order("created_at DESC").Find(&keys)
	for i := range keys {
		maskAPIKey(&keys[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": keys})
}

// Create 创建 API Key。
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

	if req.Key == "" {
		req.Key = "ra-" + generateRandomKey(32)
	} else if !strings.HasPrefix(req.Key, "ra-") {
		req.Key = "ra-" + req.Key
	}
	if req.Status == 0 {
		req.Status = 1
	}

	if err := h.db.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}
	// 创建时返回一次明文 Key（此后仅脱敏）
	c.JSON(http.StatusOK, gin.H{"data": req})
}

// Update 更新 API Key（不更新 key 本身）。
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
	delete(req, "key")
	h.db.Model(&key).Updates(req)
	h.db.First(&key, id)
	maskAPIKey(&key)
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
