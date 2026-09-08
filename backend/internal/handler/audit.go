package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
)

// AuditHandler 管理操作审计日志查询（仅 admin，长期保留，供团队合规审计）。
type AuditHandler struct {
	db *gorm.DB
}

func NewAuditHandler(db *gorm.DB) *AuditHandler {
	return &AuditHandler{db: db}
}

// List 审计日志列表（多条件筛选 + 分页）。
func (h *AuditHandler) List(c *gin.Context) {
	page, pageSize := pagination(c)
	query := h.db.Model(&model.AuditLog{})

	if action := c.Query("action"); action != "" {
		query = query.Where("action = ?", action)
	}
	if actor := c.Query("actor_user_id"); actor != "" {
		query = query.Where("actor_user_id = ?", actor)
	}
	if rid := c.Query("request_id"); rid != "" {
		query = query.Where("request_id = ?", rid)
	}
	if success := c.Query("success"); success != "" {
		query = query.Where("success = ?", success == "1" || success == "true")
	}
	if dateFrom := c.Query("date_from"); dateFrom != "" {
		query = query.Where("created_at >= ?", dateFrom)
	}
	if dateTo := c.Query("date_to"); dateTo != "" {
		query = query.Where("created_at <= ?", dateTo+" 23:59:59")
	}

	var total int64
	query.Count(&total)

	var logs []model.AuditLog
	query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&logs)

	c.JSON(http.StatusOK, gin.H{
		"data":      logs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
