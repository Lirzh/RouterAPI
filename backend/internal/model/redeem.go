package model

import "time"

// 兑换码状态
const (
	RedeemCodeUnused   = "unused"   // 未兑换
	RedeemCodeRedeemed = "redeemed" // 已被兑换
	RedeemCodeDisabled = "disabled" // 管理员主动作废
)

// RedemptionCode 积分兑换码：credits 的发放路径之一（另一条是管理员直接调整余额）。
//
// 一码一用。码值本身是 bearer 凭证——谁拿到谁就能兑换，因此：
//   - 审计日志的字段白名单里绝不能加 "code"（见 middleware/audit.go）
//   - 列表接口只回显脱敏后的码，完整码仅在生成响应里出现一次
type RedemptionCode struct {
	ID   uint   `json:"id" gorm:"primaryKey"`
	Code string `json:"code" gorm:"uniqueIndex;size:64;not null"` // 展示形态：XXXX-XXXX-XXXX-XXXX
	// 归一化形态（仅大写字母数字），用于兑换时匹配用户粘贴的任意写法。
	// 单独建唯一索引，避免查询时做函数运算导致索引失效。
	CodeNorm  string     `json:"-" gorm:"uniqueIndex;size:32;not null"`
	Credits   int64      `json:"credits"`                    // 面额：兑换后可获得的积分
	Batch     string     `json:"batch" gorm:"size:64;index"` // 批次标签，便于整批发放与停用
	Note      string     `json:"note" gorm:"size:255"`
	Status    string     `json:"status" gorm:"size:16;index;default:unused"`
	CreatedBy uint       `json:"created_by" gorm:"index"`
	ExpiresAt *time.Time `json:"expires_at"`

	RedeemedBy uint       `json:"redeemed_by" gorm:"index"`
	RedeemedAt *time.Time `json:"redeemed_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
