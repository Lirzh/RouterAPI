package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/provider"
	"github.com/routerapi/backend/pkg/crypto"
	"gorm.io/gorm"
)

// VendorHandler 供应商管理。
type VendorHandler struct {
	db *gorm.DB
}

func NewVendorHandler(db *gorm.DB) *VendorHandler {
	return &VendorHandler{db: db}
}

// Protocols 返回协议预设列表（供前端下拉）。
func (h *VendorHandler) Protocols(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": model.AllProtocolPresets()})
}

// maskVendor 脱敏供应商 API Key。
func maskVendor(v *model.Vendor) {
	if v.APIKey != "" {
		if decrypted, err := crypto.Decrypt(v.APIKey); err == nil {
			v.APIKeyMasked = crypto.MaskString(decrypted)
		} else {
			v.APIKeyMasked = "****"
		}
		v.APIKey = ""
	}
}

// List 供应商列表（按优先级、创建时间排序，脱敏）。
func (h *VendorHandler) List(c *gin.Context) {
	var vendors []model.Vendor
	h.db.Order("priority DESC, created_at DESC").Find(&vendors)
	for i := range vendors {
		maskVendor(&vendors[i])
	}
	c.JSON(http.StatusOK, gin.H{"data": vendors})
}

// Get 单个供应商（脱敏）。
func (h *VendorHandler) Get(c *gin.Context) {
	id := c.Param("id")
	var v model.Vendor
	if err := h.db.First(&v, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	maskVendor(&v)
	c.JSON(http.StatusOK, gin.H{"data": v})
}

// applyPresetDefaults 用协议预设填充空的鉴权/地址字段。
func applyPresetDefaults(v *model.Vendor) {
	preset := model.GetProtocolPreset(v.Protocol)
	if v.AuthType == "" {
		v.AuthType = preset.AuthType
	}
	if v.AuthHeader == "" {
		v.AuthHeader = preset.AuthHeader
	}
	if v.AuthPrefix == "" && preset.AuthPrefix != "" {
		v.AuthPrefix = preset.AuthPrefix
	}
	if v.BaseURL == "" {
		v.BaseURL = preset.DefaultBaseURL
	}
}

// Create 创建供应商。
func (h *VendorHandler) Create(c *gin.Context) {
	var v model.Vendor
	if err := c.ShouldBindJSON(&v); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if strings.TrimSpace(v.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "供应商名称不能为空"})
		return
	}
	if v.Protocol == "" {
		v.Protocol = model.ProtocolOpenAI
	}
	applyPresetDefaults(&v)
	if v.Weight <= 0 {
		v.Weight = 1
	}
	if v.MaxRetry <= 0 {
		v.MaxRetry = 3
	}
	if v.Status == 0 {
		v.Status = 1
	}

	if v.APIKey != "" {
		encrypted, err := crypto.Encrypt(v.APIKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "加密失败"})
			return
		}
		v.APIKey = encrypted
	}

	if err := h.db.Create(&v).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}
	maskVendor(&v)
	c.JSON(http.StatusOK, gin.H{"data": v})
}

// Update 更新供应商（api_key 为空或脱敏格式则不更新）。
func (h *VendorHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var v model.Vendor
	if err := h.db.First(&v, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if apiKey, ok := req["api_key"].(string); ok {
		if apiKey == "" || strings.Contains(apiKey, "****") {
			delete(req, "api_key")
		} else if encrypted, err := crypto.Encrypt(apiKey); err == nil {
			req["api_key"] = encrypted
		}
	}
	// 不允许通过 map 意外清空熔断/错误状态字段
	delete(req, "cooldown_until")
	delete(req, "consecutive_errors")

	if protocol, ok := req["protocol"].(string); ok && protocol != "" {
		// 协议变更时补齐鉴权默认（若未显式提供）
		preset := model.GetProtocolPreset(protocol)
		if _, has := req["auth_type"]; !has {
			req["auth_type"] = preset.AuthType
		}
		if _, has := req["auth_header"]; !has {
			req["auth_header"] = preset.AuthHeader
		}
	}

	req["updated_at"] = time.Now()
	h.db.Model(&v).Updates(req)
	h.db.First(&v, id)
	maskVendor(&v)
	c.JSON(http.StatusOK, gin.H{"data": v})
}

// Delete 删除供应商（连带其模型绑定）。
func (h *VendorHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	h.db.Where("vendor_id = ?", id).Delete(&model.ModelBinding{})
	h.db.Delete(&model.Vendor{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// setStatus 设置供应商启用/禁用状态。
func (h *VendorHandler) setStatus(c *gin.Context, status int) {
	id := c.Param("id")
	updates := map[string]interface{}{"status": status, "updated_at": time.Now()}
	if status == 1 {
		// 手动启用时同时清除熔断
		updates["cooldown_until"] = nil
		updates["consecutive_errors"] = 0
	}
	if err := h.db.Model(&model.Vendor{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// Enable 手动启用供应商。
func (h *VendorHandler) Enable(c *gin.Context) { h.setStatus(c, 1) }

// Disable 手动禁用供应商（不会被健康探测自动恢复）。
func (h *VendorHandler) Disable(c *gin.Context) { h.setStatus(c, 0) }

// loadDecrypted 读取并解密供应商，用于连通性测试/模型发现。
func (h *VendorHandler) loadDecrypted(id string) (*model.Vendor, error) {
	var v model.Vendor
	if err := h.db.First(&v, id).Error; err != nil {
		return nil, err
	}
	if v.APIKey != "" {
		if decrypted, err := crypto.Decrypt(v.APIKey); err == nil {
			v.APIKey = decrypted
		}
	}
	return &v, nil
}

// Test 测试供应商连通性（拉取上游模型列表）。
func (h *VendorHandler) Test(c *gin.Context) {
	v, err := h.loadDecrypted(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	prov, err := provider.NewFromVendor(v)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": err.Error()})
		return
	}
	start := time.Now()
	models, err := prov.Models(c.Request.Context())
	latency := time.Since(start).Milliseconds()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": err.Error(), "latency_ms": latency})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "models": models, "latency_ms": latency})
}

// DiscoverModels 发现供应商可用模型（供“模型”页导入使用）。
func (h *VendorHandler) DiscoverModels(c *gin.Context) {
	v, err := h.loadDecrypted(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	prov, err := provider.NewFromVendor(v)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	models, err := prov.Models(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "获取上游模型失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": models, "vendor_id": v.ID, "vendor_name": v.Name})
}
