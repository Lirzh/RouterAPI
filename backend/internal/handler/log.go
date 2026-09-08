package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/stats"
	"gorm.io/gorm"
)

// LogHandler 调用日志查询与统计（统计走 StatData 预聚合小表，避免对海量日志实时聚合）。
type LogHandler struct {
	db *gorm.DB
}

func NewLogHandler(db *gorm.DB) *LogHandler {
	return &LogHandler{db: db}
}

// List 日志列表（多条件筛选 + 分页）。
func (h *LogHandler) List(c *gin.Context) {
	page, pageSize := pagination(c)
	query := h.db.Model(&model.RequestLog{})

	if m := c.Query("model"); m != "" {
		query = query.Where("model = ?", m)
	}
	if vid := c.Query("vendor_id"); vid != "" {
		query = query.Where("vendor_id = ?", vid)
	}
	if kid := c.Query("api_key_id"); kid != "" {
		query = query.Where("api_key_id = ?", kid)
	}
	if t := c.Query("type"); t != "" {
		query = query.Where("type = ?", t)
	}
	if rid := c.Query("request_id"); rid != "" {
		query = query.Where("request_id = ?", rid)
	}
	if urid := c.Query("upstream_request_id"); urid != "" {
		query = query.Where("upstream_request_id = ?", urid)
	}
	if success := c.Query("success"); success != "" {
		query = query.Where("success = ?", success == "1" || success == "true")
	}
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + escapeLike(kw) + "%"
		query = query.Where("model LIKE ? ESCAPE '\\' OR error_message LIKE ? ESCAPE '\\'", like, like)
	}
	if dateFrom := c.Query("date_from"); dateFrom != "" {
		query = query.Where("created_at >= ?", dateFrom)
	}
	if dateTo := c.Query("date_to"); dateTo != "" {
		query = query.Where("created_at <= ?", dateTo+" 23:59:59")
	}

	var total int64
	query.Count(&total)

	var logs []model.RequestLog
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

// hourBounds 将 date_from/date_to（YYYY-MM-DD）转换为整点 Unix 秒区间 [lo, hi)。
func hourBounds(dateFrom, dateTo string) (lo, hi int64, hasLo, hasHi bool) {
	if dateFrom != "" {
		if t, err := time.ParseInLocation("2006-01-02", dateFrom, time.Local); err == nil {
			lo = model.HourBucket(t.Unix())
			hasLo = true
		}
	}
	if dateTo != "" {
		if t, err := time.ParseInLocation("2006-01-02", dateTo, time.Local); err == nil {
			hi = model.HourBucket(t.Unix()) + 86400 // 含当天：取次日零点为上界（开区间）
			hasHi = true
		}
	}
	return
}

// applyHourRange 按 hour 区间过滤 StatData 查询。
func applyHourRange(q *gorm.DB, dateFrom, dateTo string) *gorm.DB {
	lo, hi, hasLo, hasHi := hourBounds(dateFrom, dateTo)
	if hasLo {
		q = q.Where("hour >= ?", lo)
	}
	if hasHi {
		q = q.Where("hour < ?", hi)
	}
	return q
}

// Stats 仪表盘统计（数据源：StatData 预聚合）。
func (h *LogHandler) Stats(c *gin.Context) {
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")

	// 总览
	var sums struct {
		TotalCalls   int64
		SuccessCalls int64
		TotalTokens  int64
		TotalCredits int64
	}
	q := h.db.Model(&model.StatData{}).
		Select("COALESCE(SUM(count),0) AS total_calls, COALESCE(SUM(success),0) AS success_calls, " +
			"COALESCE(SUM(total_tokens),0) AS total_tokens, COALESCE(SUM(credits_cost),0) AS total_credits")
	applyHourRange(q, dateFrom, dateTo).Scan(&sums)

	// 今日调用（本地当日零点起）
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var todayCalls int64
	h.db.Model(&model.StatData{}).Where("hour >= ?", model.HourBucket(startOfDay.Unix())).
		Select("COALESCE(SUM(count),0)").Scan(&todayCalls)

	// 按供应商统计（StatData 存 vendor_id，二次查名）
	type vendorRow struct {
		VendorID uint  `json:"vendor_id"`
		Calls    int64 `json:"calls"`
		Success  int64 `json:"success"`
		Credits  int64 `json:"credits"`
	}
	var vendorRows []vendorRow
	vq := h.db.Model(&model.StatData{}).
		Select("vendor_id, SUM(count) AS calls, SUM(success) AS success, COALESCE(SUM(credits_cost),0) AS credits").
		Where("vendor_id <> 0").Group("vendor_id").Order("calls DESC").Limit(10)
	applyHourRange(vq, dateFrom, dateTo).Scan(&vendorRows)
	vendorNames := h.vendorNameMap(collectUint(func(i int) uint { return vendorRows[i].VendorID }, len(vendorRows)))
	type VendorStat struct {
		VendorID   uint   `json:"vendor_id"`
		VendorName string `json:"vendor_name"`
		Calls      int64  `json:"calls"`
		Success    int64  `json:"success"`
		Credits    int64  `json:"credits"`
	}
	vendorStats := make([]VendorStat, 0, len(vendorRows))
	for _, r := range vendorRows {
		vendorStats = append(vendorStats, VendorStat{r.VendorID, vendorNames[r.VendorID], r.Calls, r.Success, r.Credits})
	}

	// 按模型统计
	type ModelStat struct {
		Model   string `json:"model"`
		Calls   int64  `json:"calls"`
		Tokens  int64  `json:"tokens"`
		Credits int64  `json:"credits"`
	}
	var modelStats []ModelStat
	mq := h.db.Model(&model.StatData{}).
		Select("model, SUM(count) AS calls, COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(credits_cost),0) AS credits").
		Group("model").Order("calls DESC").Limit(10)
	applyHourRange(mq, dateFrom, dateTo).Scan(&modelStats)

	// 最近 7 天每日调用量（逐日 hour 区间求和，跨方言可移植）
	type DailyStat struct {
		Date  string `json:"date"`
		Calls int64  `json:"calls"`
	}
	dailyStats := make([]DailyStat, 0, 7)
	for i := 6; i >= 0; i-- {
		dayStart := startOfDay.AddDate(0, 0, -i)
		dayEnd := dayStart.AddDate(0, 0, 1)
		var cnt int64
		h.db.Model(&model.StatData{}).
			Where("hour >= ? AND hour < ?", model.HourBucket(dayStart.Unix()), model.HourBucket(dayEnd.Unix())).
			Select("COALESCE(SUM(count),0)").Scan(&cnt)
		dailyStats = append(dailyStats, DailyStat{Date: dayStart.Format("2006-01-02"), Calls: cnt})
	}

	c.JSON(http.StatusOK, gin.H{
		"total_calls":   sums.TotalCalls,
		"success_calls": sums.SuccessCalls,
		"total_tokens":  sums.TotalTokens,
		"total_credits": sums.TotalCredits,
		"today_calls":   todayCalls,
		"vendor_stats":  vendorStats,
		"model_stats":   modelStats,
		"daily_stats":   dailyStats,
	})
}

// Usage 按维度分组的用量与积分统计：group_by=model|vendor|api_key，from/to 为 YYYY-MM-DD。
func (h *LogHandler) Usage(c *gin.Context) {
	groupBy := c.Query("group_by")
	// 允许列表：仅接受固定分组值，杜绝 SQL 注入
	switch groupBy {
	case "vendor", "api_key":
	default:
		groupBy = "model"
	}

	type row struct {
		Calls   int64 `json:"calls"`
		Success int64 `json:"success"`
		Tokens  int64 `json:"tokens"`
		Credits int64 `json:"credits"`
	}
	dateFrom := c.Query("date_from")
	dateTo := c.Query("date_to")

	switch groupBy {
	case "model":
		type out struct {
			Model string `json:"model"`
			row
		}
		var rows []out
		q := h.db.Model(&model.StatData{}).
			Select("model, SUM(count) AS calls, SUM(success) AS success, COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(credits_cost),0) AS credits").
			Group("model").Order("credits DESC, calls DESC")
		applyHourRange(q, dateFrom, dateTo).Scan(&rows)
		c.JSON(http.StatusOK, gin.H{"group_by": groupBy, "data": rows})
	case "vendor":
		type out struct {
			VendorID   uint   `json:"vendor_id"`
			VendorName string `json:"vendor_name"`
			row
		}
		var base []struct {
			VendorID uint `json:"vendor_id"`
			row
		}
		q := h.db.Model(&model.StatData{}).
			Select("vendor_id, SUM(count) AS calls, SUM(success) AS success, COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(credits_cost),0) AS credits").
			Group("vendor_id").Order("credits DESC, calls DESC")
		applyHourRange(q, dateFrom, dateTo).Scan(&base)
		names := h.vendorNameMap(collectUint(func(i int) uint { return base[i].VendorID }, len(base)))
		rows := make([]out, 0, len(base))
		for _, b := range base {
			rows = append(rows, out{b.VendorID, names[b.VendorID], b.row})
		}
		c.JSON(http.StatusOK, gin.H{"group_by": groupBy, "data": rows})
	default: // api_key
		type out struct {
			APIKeyID   uint   `json:"api_key_id"`
			APIKeyName string `json:"api_key_name"`
			row
		}
		var base []struct {
			APIKeyID uint `json:"api_key_id"`
			row
		}
		q := h.db.Model(&model.StatData{}).
			Select("api_key_id, SUM(count) AS calls, SUM(success) AS success, COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(credits_cost),0) AS credits").
			Group("api_key_id").Order("credits DESC, calls DESC")
		applyHourRange(q, dateFrom, dateTo).Scan(&base)
		names := h.apiKeyNameMap(collectUint(func(i int) uint { return base[i].APIKeyID }, len(base)))
		rows := make([]out, 0, len(base))
		for _, b := range base {
			rows = append(rows, out{b.APIKeyID, names[b.APIKeyID], b.row})
		}
		c.JSON(http.StatusOK, gin.H{"group_by": groupBy, "data": rows})
	}
}

// Recompute 重算指定时间范围（默认最近 30 天）的预聚合数据（仅 admin）。
func (h *LogHandler) Recompute(c *gin.Context) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	_ = c.ShouldBindJSON(&req) // body 可选

	to := time.Now()
	from := to.AddDate(0, 0, -30)
	if req.From != "" {
		if t, err := time.ParseInLocation("2006-01-02", req.From, time.Local); err == nil {
			from = t
		}
	}
	if req.To != "" {
		if t, err := time.ParseInLocation("2006-01-02", req.To, time.Local); err == nil {
			to = t.AddDate(0, 0, 1) // 含当天
		}
	}
	if !to.After(from) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "时间范围无效"})
		return
	}

	n, err := stats.Recompute(h.db, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重算失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "重算完成", "rows": n,
		"from": from.Format("2006-01-02"), "to": to.Format("2006-01-02")})
}

// vendorNameMap 批量查询供应商 ID -> 名称。
func (h *LogHandler) vendorNameMap(ids []uint) map[uint]string {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	var vs []model.Vendor
	h.db.Select("id", "name").Where("id IN ?", ids).Find(&vs)
	for _, v := range vs {
		out[v.ID] = v.Name
	}
	return out
}

// apiKeyNameMap 批量查询 API Key ID -> 名称。
func (h *LogHandler) apiKeyNameMap(ids []uint) map[uint]string {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out
	}
	var ks []model.APIKey
	h.db.Select("id", "name").Where("id IN ?", ids).Find(&ks)
	for _, k := range ks {
		out[k.ID] = k.Name
	}
	return out
}

// collectUint 依据取值函数收集去重后的 uint 列表。
func collectUint(at func(i int) uint, n int) []uint {
	seen := make(map[uint]bool, n)
	out := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		id := at(i)
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
