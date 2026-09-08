package model

import "time"

// CreditTransaction 积分流水账：记录每一次余额变动（充值/消费/退款/调整），供团队审计。
type CreditTransaction struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	UserID       uint      `json:"user_id" gorm:"index"`
	Amount       int64     `json:"amount"`                  // 正=入账，负=扣减
	Type         string    `json:"type" gorm:"size:16;index"` // topup|consume|refund|adjust
	BalanceAfter int64     `json:"balance_after"`           // 变动后余额
	RequestID    string    `json:"request_id" gorm:"index;size:64"`
	Model        string    `json:"model" gorm:"size:128"`
	Note         string    `json:"note" gorm:"size:512"`
	OperatorID   uint      `json:"operator_id"`             // 操作者（充值/调整时的管理员）
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
}
