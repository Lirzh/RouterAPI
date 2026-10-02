package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/billing"
	"github.com/routerapi/backend/internal/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// WalletHandler 积分钱包：当前用户余额与流水、管理员充值/调整、全局流水查询。
type WalletHandler struct {
	db *gorm.DB
}

func NewWalletHandler(db *gorm.DB) *WalletHandler {
	return &WalletHandler{db: db}
}

// GetWallet 当前登录用户的积分余额 + 本人流水（分页）。所有角色可访问自己的钱包。
//
// 除 credits 外还要报 reserved/available：预留会把在途请求的额度先占住，
// 只报 credits 的话用户会看到"余额明明够却被告知余额不足"。
func (h *WalletHandler) GetWallet(c *gin.Context) {
	userID := c.GetUint("user_id")
	var u model.User
	if err := h.db.Select("id", "credits", "reserved_credits").First(&u, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "账户不存在"})
		return
	}
	balance := u.Credits
	reserved := u.ReservedCredits
	available := balance - reserved

	page, pageSize := pagination(c)
	query := h.db.Model(&model.CreditTransaction{}).Where("user_id = ?", userID)
	if t := c.Query("type"); t != "" {
		query = query.Where("type = ?", t)
	}

	var total int64
	query.Count(&total)

	var txns []model.CreditTransaction
	query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&txns)

	c.JSON(http.StatusOK, gin.H{
		"credits":   balance,
		"reserved":  reserved,
		"available": available,
		"data":      txns,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// AdjustCredits 管理员为指定用户充值/调整积分（正=入账，负=扣减）。
func (h *WalletHandler) AdjustCredits(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}

	var req struct {
		Amount int64  `json:"amount"` // 正=充值，负=扣减
		Type   string `json:"type"`   // topup | adjust | refund，留空按正负自动判定
		Note   string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if req.Amount == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "调整额度不能为 0"})
		return
	}

	txType := req.Type
	if txType == "" {
		if req.Amount > 0 {
			txType = model.CreditTypeTopup
		} else {
			txType = model.CreditTypeAdjust
		}
	}
	switch txType {
	case model.CreditTypeTopup, model.CreditTypeAdjust, model.CreditTypeRefund:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "类型必须为 topup / adjust / refund"})
		return
	}

	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	operatorID := c.GetUint("user_id")
	balance, err := billing.Adjust(h.db, uint(id), req.Amount, txType, req.Note, operatorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "调整失败: " + err.Error()})
		return
	}

	logrus.Infof("管理员调整用户 %s 积分 %+d（类型 %s，操作者ID %d）", user.Username, req.Amount, txType, operatorID)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"user_id": id,
		"amount":  req.Amount,
		"type":    txType,
		"credits": balance,
	}})
}

// ListTransactions 全局积分流水（仅 admin，用于团队审计；支持按用户/类型筛选）。
func (h *WalletHandler) ListTransactions(c *gin.Context) {
	page, pageSize := pagination(c)
	query := h.db.Model(&model.CreditTransaction{})
	if uid := c.Query("user_id"); uid != "" {
		query = query.Where("user_id = ?", uid)
	}
	if t := c.Query("type"); t != "" {
		query = query.Where("type = ?", t)
	}
	if rid := c.Query("request_id"); rid != "" {
		query = query.Where("request_id = ?", rid)
	}

	var total int64
	query.Count(&total)

	var txns []model.CreditTransaction
	query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&txns)

	c.JSON(http.StatusOK, gin.H{
		"data":      txns,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
