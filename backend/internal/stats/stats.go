// Package stats 提供按小时预聚合的用量统计：写入走幂等 upsert，看板查询走小表，
// 避免对海量 RequestLog 实时 GROUP BY。时间对齐到整点，SQLite 与 PostgreSQL 均可移植。
package stats

import (
	"time"

	"github.com/routerapi/backend/internal/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// batchSize Recompute 时分批读取 RequestLog 的批大小。
const batchSize = 1000

// Record 将一条调用日志增量并入其所属整点的预聚合行（幂等 upsert）。
// 冲突键为 (hour, model, vendor_id, api_key_id)；命中则各计数/用量/积分做相对累加。
func Record(db *gorm.DB, e model.RequestLog) {
	ts := e.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	var successInc int64
	if e.Success {
		successInc = 1
	}
	stat := model.StatData{
		Hour:             model.HourBucket(ts.Unix()),
		Model:            e.Model,
		VendorID:         e.VendorID,
		APIKeyID:         e.APIKeyID,
		UserID:           e.UserID,
		Count:            1,
		Success:          successInc,
		PromptTokens:     int64(e.TokenPrompt),
		CompletionTokens: int64(e.TokenCompletion),
		TotalTokens:      int64(e.TokenTotal),
		CreditsCost:      e.CreditsCost,
	}
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "hour"}, {Name: "model"}, {Name: "vendor_id"}, {Name: "api_key_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"count":             gorm.Expr("stat_data.count + ?", 1),
			"success":           gorm.Expr("stat_data.success + ?", successInc),
			"prompt_tokens":     gorm.Expr("stat_data.prompt_tokens + ?", stat.PromptTokens),
			"completion_tokens": gorm.Expr("stat_data.completion_tokens + ?", stat.CompletionTokens),
			"total_tokens":      gorm.Expr("stat_data.total_tokens + ?", stat.TotalTokens),
			"credits_cost":      gorm.Expr("stat_data.credits_cost + ?", stat.CreditsCost),
		}),
	}).Create(&stat).Error
	if err != nil {
		logrus.Errorf("写入预聚合统计失败: %v", err)
	}
}

// groupKey 预聚合分组键。
type groupKey struct {
	hour     int64
	model    string
	vendorID uint
	apiKeyID uint
}

// Recompute 重算 [from, to] 时间范围内的预聚合数据：先删除该范围内的 StatData，
// 再从 RequestLog 分批读取并在内存聚合后重写。用于修复历史数据或口径变更后的回填。
// 返回重建的聚合行数。
func Recompute(db *gorm.DB, from, to time.Time) (int, error) {
	fromHour := model.HourBucket(from.Unix())
	toHour := model.HourBucket(to.Unix())
	// 1) 删除范围内的旧聚合
	if err := db.Where("hour >= ? AND hour <= ?", fromHour, toHour).
		Delete(&model.StatData{}).Error; err != nil {
		return 0, err
	}

	// 2) 分批读取 RequestLog 并在内存聚合
	agg := make(map[groupKey]*model.StatData)
	var lastID uint
	for {
		var logs []model.RequestLog
		if err := db.Where("created_at >= ? AND created_at <= ? AND id > ?", from, to, lastID).
			Order("id ASC").Limit(batchSize).Find(&logs).Error; err != nil {
			return 0, err
		}
		if len(logs) == 0 {
			break
		}
		for _, e := range logs {
			lastID = e.ID
			ts := e.CreatedAt
			if ts.IsZero() {
				continue
			}
			k := groupKey{hour: model.HourBucket(ts.Unix()), model: e.Model, vendorID: e.VendorID, apiKeyID: e.APIKeyID}
			s, ok := agg[k]
			if !ok {
				s = &model.StatData{Hour: k.hour, Model: k.model, VendorID: k.vendorID, APIKeyID: k.apiKeyID, UserID: e.UserID}
				agg[k] = s
			}
			s.Count++
			if e.Success {
				s.Success++
			}
			s.PromptTokens += int64(e.TokenPrompt)
			s.CompletionTokens += int64(e.TokenCompletion)
			s.TotalTokens += int64(e.TokenTotal)
			s.CreditsCost += e.CreditsCost
		}
		if len(logs) < batchSize {
			break
		}
	}

	// 3) 批量写入（幂等 upsert，防止与并发 Record 撞键）
	if len(agg) == 0 {
		return 0, nil
	}
	rows := make([]model.StatData, 0, len(agg))
	for _, s := range agg {
		rows = append(rows, *s)
	}
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "hour"}, {Name: "model"}, {Name: "vendor_id"}, {Name: "api_key_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"count": gorm.Expr("stat_data.count"), "success": gorm.Expr("stat_data.success"),
			"prompt_tokens": gorm.Expr("stat_data.prompt_tokens"), "completion_tokens": gorm.Expr("stat_data.completion_tokens"),
			"total_tokens": gorm.Expr("stat_data.total_tokens"), "credits_cost": gorm.Expr("stat_data.credits_cost"),
		}),
	}).CreateInBatches(rows, 200).Error
	if err != nil {
		return 0, err
	}
	return len(rows), nil
}
