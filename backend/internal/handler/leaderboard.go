package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/config"
	"gorm.io/gorm"
)

// LeaderboardHandler 全站用户排行榜：按 StatData 预聚合小表以 user_id 维度聚合，
// 公开可读。是否公开、是否匿名化、条数上限均由 LeaderboardConfig 决定。
type LeaderboardHandler struct {
	db  *gorm.DB
	cfg config.LeaderboardConfig
}

func NewLeaderboardHandler(db *gorm.DB, cfg config.LeaderboardConfig) *LeaderboardHandler {
	return &LeaderboardHandler{db: db, cfg: cfg}
}

// periodStartHour 把周期（today|week|month|all）折算为起始整点 Unix 秒。
// 返回 ok=false 表示不做时间过滤（all）。口径与看板 stats 保持一致（本地日/周/月）。
func periodStartHour(period string) (int64, bool) {
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var t time.Time
	switch period {
	case "today":
		t = startOfDay
	case "month":
		t = startOfDay.AddDate(0, 0, -29)
	case "all":
		return 0, false
	case "week":
		fallthrough
	default:
		t = startOfDay.AddDate(0, 0, -6)
	}
	return model.HourBucket(t.Unix()), true
}

// metricOrder 把排名维度映射到完整、静态的 ORDER BY 子句。
// 整段返回固定字面量、不与任何变量拼接，杜绝注入，也让静态扫描可证明安全。
func metricOrder(metric string) string {
	switch metric {
	case "tokens":
		return "tokens DESC, calls DESC"
	case "calls":
		return "calls DESC"
	default: // credits
		return "credits DESC, calls DESC"
	}
}

// maskUsername 服务端匿名化：只保留首字符 + 掩码。在数据离开进程前完成，
// 公开响应体里不再出现真实用户名（前端遮罩是假安全——名字仍在 JSON 里）。
func maskUsername(name string, userID uint) string {
	r := []rune(name)
	switch len(r) {
	case 0:
		return fmt.Sprintf("匿名用户#%d", userID)
	case 1:
		return string(r[0]) + "**"
	default:
		return string(r[0]) + "***"
	}
}

// zeroTotals 未开放时返回的空汇总，保证前端结构一致。
func zeroTotals() gin.H {
	return gin.H{"calls": 0, "tokens": 0, "credits": 0, "users": 0}
}

// List GET /api/leaderboard?period=week&metric=credits&limit=20
// 返回按所选维度排序的用户排行 + 全站汇总（供起始页数据条使用）。
func (h *LeaderboardHandler) List(c *gin.Context) {
	period := c.DefaultQuery("period", "week")
	switch period {
	case "today", "week", "month", "all":
	default:
		period = "week"
	}
	metric := c.DefaultQuery("metric", "credits")
	if metric != "tokens" && metric != "calls" {
		metric = "credits"
	}

	// 公开排行榜被关闭：结构保持一致，但数据清空、totals 归零，前端显示"未开放"。
	if !h.cfg.PublicEnabled() {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false, "anon": h.cfg.Anonymize,
			"period": period, "metric": metric, "data": []any{}, "totals": zeroTotals(),
		})
		return
	}

	// limit：缺省用配置的 DefaultLimit，并按 [1, MaxLimit] 夹紧。
	limit := h.cfg.DefaultLimit
	if s := c.Query("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > h.cfg.MaxLimit {
		limit = h.cfg.MaxLimit
	}

	// 1) 排行榜主体：按 user_id 聚合用量，过滤匿名（user_id=0）。
	type row struct {
		UserID  uint  `json:"user_id"`
		Calls   int64 `json:"calls"`
		Success int64 `json:"success"`
		Tokens  int64 `json:"tokens"`
		Credits int64 `json:"credits"`
	}
	base := h.db.Model(&model.StatData{}).Where("user_id <> 0")
	if startHour, ok := periodStartHour(period); ok {
		base = base.Where("hour >= ?", startHour)
	}

	var rows []row
	base.
		Select("user_id, SUM(count) AS calls, SUM(success) AS success, " +
			"COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(credits_cost),0) AS credits").
		Group("user_id").
		Order(metricOrder(metric)).
		Limit(limit).
		Scan(&rows)

	// 2) 批量取用户名（软删除用户自动被 GORM 排除）。
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	names := make(map[uint]string, len(ids))
	if len(ids) > 0 {
		var users []model.User
		h.db.Select("id", "username").Where("id IN ?", ids).Find(&users)
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}

	type entry struct {
		Rank     int    `json:"rank"`
		UserID   uint   `json:"user_id"`
		Username string `json:"username"`
		Calls    int64  `json:"calls"`
		Success  int64  `json:"success"`
		Tokens   int64  `json:"tokens"`
		Credits  int64  `json:"credits"`
	}
	entries := make([]entry, 0, len(rows))
	for i, r := range rows {
		name := names[r.UserID]
		if h.cfg.Anonymize {
			name = maskUsername(name, r.UserID)
		}
		entries = append(entries, entry{
			Rank:     i + 1,
			UserID:   r.UserID,
			Username: name,
			Calls:    r.Calls,
			Success:  r.Success,
			Tokens:   r.Tokens,
			Credits:  r.Credits,
		})
	}

	// 3) 全站汇总（同周期口径）：总调用/总 Token/总积分/上榜用户数。
	// 与排行榜保持同一口径：只统计已归属用户（user_id<>0）的流量。
	var totals struct {
		Calls   int64 `json:"calls"`
		Tokens  int64 `json:"tokens"`
		Credits int64 `json:"credits"`
		Users   int64 `json:"users"`
	}
	tq := h.db.Model(&model.StatData{}).Where("user_id <> 0")
	if startHour, ok := periodStartHour(period); ok {
		tq = tq.Where("hour >= ?", startHour)
	}
	tq.Select("COALESCE(SUM(count),0) AS calls, COALESCE(SUM(total_tokens),0) AS tokens, " +
		"COALESCE(SUM(credits_cost),0) AS credits, COUNT(DISTINCT user_id) AS users").Scan(&totals)

	c.JSON(http.StatusOK, gin.H{
		"enabled": true,
		"anon":    h.cfg.Anonymize,
		"period":  period,
		"metric":  metric,
		"data":    entries,
		"totals":  totals,
	})
}
