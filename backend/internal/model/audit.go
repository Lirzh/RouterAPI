package model

import "time"

// AuditLog 管理操作审计日志：记录谁在何时对哪个资源做了什么。
// 与消费日志(RequestLog)分离，长期保留，不参与常规日志清理。
type AuditLog struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	ActorUserID uint      `json:"actor_user_id" gorm:"index"`
	ActorName   string    `json:"actor_name" gorm:"size:64"`
	ActorRole   string    `json:"actor_role" gorm:"size:32"`
	Action      string    `json:"action" gorm:"size:64;index"` // 如 vendor.create / model.delete
	Method      string    `json:"method" gorm:"size:8"`
	Path        string    `json:"path" gorm:"size:256"`
	TargetID    string    `json:"target_id" gorm:"size:64;index"`
	RequestID   string    `json:"request_id" gorm:"index;size:64"`
	ClientIP    string    `json:"client_ip" gorm:"size:64"`
	UserAgent   string    `json:"user_agent" gorm:"size:512"`
	StatusCode  int       `json:"status_code"`
	Success     bool      `json:"success" gorm:"index"`
	Extra       string    `json:"extra" gorm:"type:text"` // JSON：白名单标量字段，脱敏截断
	CreatedAt   time.Time `json:"created_at" gorm:"index"`
}
