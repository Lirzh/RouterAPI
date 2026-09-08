package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
)

// ModelHandler 虚拟模型目录 + 模型-供应商绑定管理。
type ModelHandler struct {
	db *gorm.DB
}

func NewModelHandler(db *gorm.DB) *ModelHandler {
	return &ModelHandler{db: db}
}

// bindingView 绑定对外视图（含供应商摘要，不含任何密钥）。
func bindingView(bd model.ModelBinding) gin.H {
	return gin.H{
		"id":             bd.ID,
		"model_id":       bd.ModelID,
		"vendor_id":      bd.VendorID,
		"vendor_name":    bd.Vendor.Name,
		"vendor_protocol": bd.Vendor.Protocol,
		"upstream_model": bd.UpstreamModel,
		"params":         bd.Params,
		"weight":         bd.Weight,
		"priority":       bd.Priority,
		"enabled":        bd.Enabled,
	}
}

// modelView 模型对外视图（含绑定列表）。
func modelView(m model.Model, bindings []model.ModelBinding) gin.H {
	bv := make([]gin.H, 0, len(bindings))
	for _, bd := range bindings {
		bv = append(bv, bindingView(bd))
	}
	return gin.H{
		"id":                     m.ID,
		"name":                   m.Name,
		"display_name":           m.DisplayName,
		"description":            m.Description,
		"enabled":                m.Enabled,
		"prompt_credits_per_1m":  m.PromptCreditsPer1M,
		"completion_credits_per_1m": m.CompletionCreditsPer1M,
		"created_at":             m.CreatedAt,
		"updated_at":             m.UpdatedAt,
		"bindings":               bv,
	}
}

// List 模型列表（含各自的绑定）。
func (h *ModelHandler) List(c *gin.Context) {
	q := h.db.Model(&model.Model{})
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR display_name LIKE ?", like, like)
	}
	if en := c.Query("enabled"); en != "" {
		q = q.Where("enabled = ?", en == "true" || en == "1")
	}

	var models []model.Model
	q.Order("created_at DESC").Find(&models)

	// 一次性查出所有相关绑定（含供应商），在内存分组，避免 N+1 与大表 join
	ids := make([]uint, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	bindingsByModel := make(map[uint][]model.ModelBinding)
	if len(ids) > 0 {
		var bindings []model.ModelBinding
		h.db.Where("model_id IN ?", ids).Preload("Vendor").Find(&bindings)
		for _, bd := range bindings {
			bindingsByModel[bd.ModelID] = append(bindingsByModel[bd.ModelID], bd)
		}
	}

	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		data = append(data, modelView(m, bindingsByModel[m.ID]))
	}
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// Get 单个模型（含绑定）。
func (h *ModelHandler) Get(c *gin.Context) {
	id := c.Param("id")
	var m model.Model
	if err := h.db.First(&m, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	var bindings []model.ModelBinding
	h.db.Where("model_id = ?", m.ID).Preload("Vendor").Find(&bindings)
	c.JSON(http.StatusOK, gin.H{"data": modelView(m, bindings)})
}

// Create 创建模型。
func (h *ModelHandler) Create(c *gin.Context) {
	var m model.Model
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "模型名不能为空"})
		return
	}
	if m.DisplayName == "" {
		m.DisplayName = m.Name
	}
	var count int64
	h.db.Model(&model.Model{}).Where("name = ?", m.Name).Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "模型名已存在"})
		return
	}
	if err := h.db.Create(&m).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": modelView(m, nil)})
}

// Update 更新模型（名称/展示名/描述/启用/定价）。
func (h *ModelHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var m model.Model
	if err := h.db.First(&m, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if name, ok := req["name"].(string); ok {
		name = strings.TrimSpace(name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "模型名不能为空"})
			return
		}
		var count int64
		h.db.Model(&model.Model{}).Where("name = ? AND id <> ?", name, m.ID).Count(&count)
		if count > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "模型名已存在"})
			return
		}
		req["name"] = name
	}
	h.db.Model(&m).Updates(req)
	h.db.First(&m, id)
	var bindings []model.ModelBinding
	h.db.Where("model_id = ?", m.ID).Preload("Vendor").Find(&bindings)
	c.JSON(http.StatusOK, gin.H{"data": modelView(m, bindings)})
}

// Delete 删除模型（连带绑定）。
func (h *ModelHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	h.db.Where("model_id = ?", id).Delete(&model.ModelBinding{})
	h.db.Delete(&model.Model{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ============ 绑定 ============

// ListBindings 列出某模型的所有绑定。
func (h *ModelHandler) ListBindings(c *gin.Context) {
	modelID := c.Param("id")
	var bindings []model.ModelBinding
	h.db.Where("model_id = ?", modelID).Preload("Vendor").Find(&bindings)
	data := make([]gin.H, 0, len(bindings))
	for _, bd := range bindings {
		data = append(data, bindingView(bd))
	}
	c.JSON(http.StatusOK, gin.H{"data": data})
}

// CreateBinding 为模型新增一个供应商绑定。
func (h *ModelHandler) CreateBinding(c *gin.Context) {
	modelID, _ := strconv.Atoi(c.Param("id"))
	var bd model.ModelBinding
	if err := c.ShouldBindJSON(&bd); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	bd.ModelID = uint(modelID)
	bd.UpstreamModel = strings.TrimSpace(bd.UpstreamModel)
	if bd.UpstreamModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "上游模型名不能为空"})
		return
	}
	// 校验模型与供应商存在
	var m model.Model
	if err := h.db.First(&m, bd.ModelID).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "模型不存在"})
		return
	}
	var v model.Vendor
	if err := h.db.First(&v, bd.VendorID).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "供应商不存在"})
		return
	}
	// 权重/优先级默认沿用供应商设置
	if bd.Weight <= 0 {
		bd.Weight = v.Weight
		if bd.Weight <= 0 {
			bd.Weight = 1
		}
	}
	if bd.Priority == 0 {
		bd.Priority = v.Priority
	}

	var count int64
	h.db.Model(&model.ModelBinding{}).
		Where("model_id = ? AND vendor_id = ? AND upstream_model = ?", bd.ModelID, bd.VendorID, bd.UpstreamModel).
		Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该绑定已存在"})
		return
	}
	if err := h.db.Create(&bd).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}
	bd.Vendor = v
	c.JSON(http.StatusOK, gin.H{"data": bindingView(bd)})
}

// UpdateBinding 更新绑定。
func (h *ModelHandler) UpdateBinding(c *gin.Context) {
	id := c.Param("binding_id")
	var bd model.ModelBinding
	if err := h.db.First(&bd, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到"})
		return
	}
	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	delete(req, "model_id") // 不允许跨模型移动绑定
	h.db.Model(&bd).Updates(req)
	h.db.Preload("Vendor").First(&bd, id)
	c.JSON(http.StatusOK, gin.H{"data": bindingView(bd)})
}

// DeleteBinding 删除绑定。
func (h *ModelHandler) DeleteBinding(c *gin.Context) {
	id := c.Param("binding_id")
	h.db.Delete(&model.ModelBinding{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Import 从供应商发现的模型批量导入：为每个上游模型创建（或复用）虚拟模型 + 绑定。
// 供应商 ID 取自路径参数 :id（路由 POST /vendors/:id/import-models）。
func (h *ModelHandler) Import(c *gin.Context) {
	var req struct {
		Models []string `json:"models" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	vendorID, _ := strconv.Atoi(c.Param("id"))
	var v model.Vendor
	if err := h.db.First(&v, vendorID).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "供应商不存在"})
		return
	}

	created, skipped := 0, 0
	for _, name := range req.Models {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// 查找或创建虚拟模型
		var m model.Model
		err := h.db.Where("name = ?", name).First(&m).Error
		if err != nil {
			m = model.Model{Name: name, DisplayName: name, Enabled: true}
			if cerr := h.db.Create(&m).Error; cerr != nil {
				skipped++
				continue
			}
		}
		// 创建绑定（若不存在）
		var count int64
		h.db.Model(&model.ModelBinding{}).
			Where("model_id = ? AND vendor_id = ? AND upstream_model = ?", m.ID, v.ID, name).
			Count(&count)
		if count > 0 {
			skipped++
			continue
		}
		weight := v.Weight
		if weight <= 0 {
			weight = 1
		}
		bd := model.ModelBinding{
			ModelID: m.ID, VendorID: v.ID, UpstreamModel: name,
			Weight: weight, Priority: v.Priority, Enabled: true,
		}
		if err := h.db.Create(&bd).Error; err != nil {
			skipped++
			continue
		}
		created++
	}
	c.JSON(http.StatusOK, gin.H{"message": "导入完成", "created": created, "skipped": skipped})
}
