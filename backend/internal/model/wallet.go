package model

import "time"

// 积分预留状态。
const (
	ReservationActive   = "active"   // 已预留，请求在途
	ReservationSettled  = "settled"  // 已按实际用量结算
	ReservationReleased = "released" // 请求失败，全额退还
	ReservationExpired  = "expired"  // 进程崩溃/泄漏兜底：TTL 到期由清扫器退还
)

// QuotaReservation 请求级积分预留。
//
// 为什么要预留：结算发生在请求之后，只靠"请求前查余额 > 0"守不住额度——余额 1 分
// 的账号可以打出一个巨额请求，扣减时被 clamp 到 0，差额直接蒸发。自用无所谓，
// 但转售定位下这就是资金漏损。
//
// 不用 SELECT FOR UPDATE：本项目要同时支持 SQLite 与 PostgreSQL，而 SQLite 不支持
// 该语法。原子性全部靠"条件 UPDATE + RowsAffected 判定"实现（见 billing 包）。
type QuotaReservation struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	RequestID string    `json:"request_id" gorm:"uniqueIndex;size:64"`
	UserID    uint      `json:"user_id" gorm:"index"`
	APIKeyID  uint      `json:"api_key_id" gorm:"index"`
	Model     string    `json:"model" gorm:"size:128"`
	Amount    int64     `json:"amount"`
	Status    string    `json:"status" gorm:"size:16;index;default:active"`
	ExpiresAt time.Time `json:"expires_at" gorm:"index"` // TTL：崩溃后由 SweepExpired 兜底
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreditTransaction 积分流水账：记录每一次余额变动（充值/消费/退款/调整），供团队审计。
type CreditTransaction struct {
	ID           uint      `json:"id" gorm:"primaryKey"`
	UserID       uint      `json:"user_id" gorm:"index"`
	Amount       int64     `json:"amount"`                    // 正=入账，负=扣减
	Type         string    `json:"type" gorm:"size:16;index"` // topup|consume|refund|adjust
	BalanceAfter int64     `json:"balance_after"`             // 变动后余额
	RequestID    string    `json:"request_id" gorm:"index;size:64"`
	Model        string    `json:"model" gorm:"size:128"`
	Note         string    `json:"note" gorm:"size:512"`
	OperatorID   uint      `json:"operator_id"` // 操作者（充值/调整时的管理员）
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
}
