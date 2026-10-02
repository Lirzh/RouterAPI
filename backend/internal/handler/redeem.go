package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/billing"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/ratelimit"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// RedeemHandler 兑换码：管理员批量生成与停用，成员自助兑换到自己的余额。
type RedeemHandler struct {
	db      *gorm.DB
	limiter *ratelimit.Limiter
}

func NewRedeemHandler(db *gorm.DB, limiter *ratelimit.Limiter) *RedeemHandler {
	if limiter == nil {
		limiter = ratelimit.New()
	}
	return &RedeemHandler{db: db, limiter: limiter}
}

// 兑换尝试限流：码是 78 bit，爆破不现实，但失败尝试需要被计费和排障噪声控制住。
const redeemLimitPerMinute = 10

const (
	redeemMaxBatchSize = 500  // 单次最多生成多少张
	redeemMaxCredits   = 1e12 // 单张最大面额，防止误输入把余额撑爆
)

// maskRedeemCode 保留首尾两组，中间打码：管理员核对是哪张码够用，拿到列表也拼不出完整码。
func maskRedeemCode(code string) string {
	parts := strings.Split(code, "-")
	if len(parts) < 2 {
		return "****"
	}
	masked := make([]string, 0, len(parts))
	for i, p := range parts {
		if i == 0 || i == len(parts)-1 {
			masked = append(masked, p)
			continue
		}
		masked = append(masked, strings.Repeat("*", len(p)))
	}
	return strings.Join(masked, "-")
}

// Create 批量生成兑换码。完整码值只在本响应里出现一次，之后列表接口只有脱敏形态。
func (h *RedeemHandler) Create(c *gin.Context) {
	var req struct {
		Credits   int64  `json:"credits"`
		Count     int    `json:"count"`
		Batch     string `json:"batch"`
		Note      string `json:"note"`
		ExpiresAt string `json:"expires_at"` // RFC3339，留空表示不过期
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > redeemMaxBatchSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次最多生成 " + strconv.Itoa(redeemMaxBatchSize) + " 张"})
		return
	}
	if req.Credits <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "面额必须大于 0"})
		return
	}
	if req.Credits > redeemMaxCredits {
		c.JSON(http.StatusBadRequest, gin.H{"error": "面额超出允许上限"})
		return
	}

	var expiresAt *time.Time
	if s := strings.TrimSpace(req.ExpiresAt); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at 需为 RFC3339 格式"})
			return
		}
		expiresAt = &t
	}

	codes, err := billing.CreateRedeemCodes(h.db, req.Count, req.Credits,
		strings.TrimSpace(req.Batch), strings.TrimSpace(req.Note), c.GetUint("user_id"), expiresAt)
	if err != nil {
		logrus.Errorf("生成兑换码失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": codes, "count": len(codes)})
}

// List 兑换码列表（脱敏）。可按状态与批次过滤。
func (h *RedeemHandler) List(c *gin.Context) {
	page, pageSize := pagination(c)
	query := h.db.Model(&model.RedemptionCode{})
	if s := c.Query("status"); s != "" {
		query = query.Where("status = ?", s)
	}
	if b := c.Query("batch"); b != "" {
		query = query.Where("batch = ?", b)
	}

	var total int64
	query.Count(&total)

	var rows []model.RedemptionCode
	if err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}

	type view struct {
		model.RedemptionCode
		CodeMasked string `json:"code_masked"`
	}
	data := make([]view, 0, len(rows))
	for _, r := range rows {
		v := view{RedemptionCode: r, CodeMasked: maskRedeemCode(r.Code)}
		v.RedemptionCode.Code = "" // 列表永不回显完整码
		data = append(data, v)
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "total": total, "page": page, "page_size": pageSize})
}

// Disable 作废一张未兑换的码。已兑换的码不允许改状态，否则审计链会与实际入账脱节。
func (h *RedeemHandler) Disable(c *gin.Context) {
	id := c.Param("id")
	res := h.db.Model(&model.RedemptionCode{}).
		Where("id = ? AND status = ?", id, model.RedeemCodeUnused).
		Updates(map[string]interface{}{"status": model.RedeemCodeDisabled, "updated_at": time.Now()})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该码不存在或已被兑换，无法停用"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已停用"})
}

// Delete 删除一张码（仅未兑换的）。
func (h *RedeemHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	res := h.db.Where("id = ? AND status = ?", id, model.RedeemCodeUnused).Delete(&model.RedemptionCode{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该码不存在或已被兑换，无法删除"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Redeem 当前登录用户兑换一张码，积分计入自己的余额。
//
// 对"不存在"和"不可用"返回同一句话：两者可区分时，这个接口就变成一个兑换码探测 oracle。
func (h *RedeemHandler) Redeem(c *gin.Context) {
	userID := c.GetUint("user_id")

	limitKey := "redeem:" + strconv.FormatUint(uint64(userID), 10)
	if !h.limiter.Allow(limitKey, redeemLimitPerMinute) {
		c.Header("Retry-After", strconv.Itoa(h.limiter.RetryAfter(limitKey)))
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "兑换尝试过于频繁，请稍后再试"})
		return
	}

	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	res, err := billing.Redeem(h.db, req.Code, userID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{
			"credits": res.Credits, "balance": res.BalanceAfter, "message": "兑换成功",
		})
		return
	case errors.Is(err, billing.ErrRedeemCodeInvalidFormat):
		c.JSON(http.StatusBadRequest, gin.H{"error": billing.ErrRedeemCodeInvalidFormat.Error()})
		return
	case errors.Is(err, billing.ErrRedeemCodeNotFound), errors.Is(err, billing.ErrRedeemCodeUnavailable):
		logrus.Infof("兑换失败(user=%d): %v", userID, err) // 细节只进服务端日志
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "兑换码无效或已被使用"})
		return
	default:
		logrus.Errorf("兑换异常(user=%d): %v", userID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "兑换失败，请稍后重试"})
		return
	}
}
