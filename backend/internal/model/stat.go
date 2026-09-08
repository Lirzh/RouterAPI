package model

// StatData 按小时预聚合统计：看板查询走此小表，避免对海量 RequestLog 实时 GROUP BY。
// 复合唯一键 (hour, model, vendor_id, api_key_id)，写入用幂等 upsert（ON CONFLICT DO UPDATE）。
// 时间只精确到小时（Unix 秒对齐到整点），SQLite 与 PostgreSQL 均可移植。
type StatData struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	Hour     int64  `json:"hour" gorm:"uniqueIndex:idx_stat,priority:1;index:idx_stat_hour"` // 整点 Unix 秒
	Model    string `json:"model" gorm:"uniqueIndex:idx_stat,priority:2;size:128"`
	VendorID uint   `json:"vendor_id" gorm:"uniqueIndex:idx_stat,priority:3"`
	APIKeyID uint   `json:"api_key_id" gorm:"uniqueIndex:idx_stat,priority:4"`
	UserID   uint   `json:"user_id" gorm:"index"`

	Count             int64 `json:"count"`
	Success           int64 `json:"success"`
	PromptTokens      int64 `json:"prompt_tokens"`
	CompletionTokens  int64 `json:"completion_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
	CreditsCost       int64 `json:"credits_cost"`
}

// HourBucket 将时间戳对齐到整点（Unix 秒）。
func HourBucket(unixSec int64) int64 {
	return unixSec - unixSec%3600
}
