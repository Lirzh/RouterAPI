// 请求级积分预留：把"先干活后收钱"改成"先占额度、干完按实际结算、多退少补"。
//
// 原子性一律用「条件 UPDATE + RowsAffected 判定」表达，不使用 SELECT FOR UPDATE —
// 本项目要同时支持 SQLite 与 PostgreSQL，而 SQLite 不支持该语法（GORM 会静默不加锁）。
package billing

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	// ErrInsufficientCredits 可用余额（credits - reserved）不足以覆盖本次预留。
	ErrInsufficientCredits = errors.New("积分余额不足")
	// ErrReservationNotActive 预留行已不在 active 态：重复结算，或 TTL 已被清扫器回收。
	ErrReservationNotActive = errors.New("预留已失效")
)

// maxTxAttempts 遇 SQLite 锁冲突时的事务重试上限。
const maxTxAttempts = 5

// txRetry 跑一个可重入事务，对 SQLite 的忙等错误做指数退避 + 抖动重试。
//
// 为什么必须自己重试：SQLite 在 shared-cache 下把只读事务升级为写事务时，会直接返回
// SQLITE_BUSY（"database is deadlocked"），而 busy_timeout 对这种升级不生效。
// PostgreSQL 用行锁 + MVCC，不会走到这条路径。因为 SQLite 是本项目的默认库，
// 这里选择有限次重试，而不是要求生产环境必须换 PG 才能用预留。
//
// 传入的 fn 必须可重放：每次重试都是一个全新事务，上一轮已整体回滚。
func txRetry(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < maxTxAttempts; attempt++ {
		if err = db.Transaction(fn); err == nil || !isBusyErr(err) {
			return err
		}
		delay := time.Duration(2<<(attempt)) * time.Millisecond
		time.Sleep(delay + time.Duration(rand.IntN(int(delay)+1)))
	}
	return fmt.Errorf("重试 %d 次后仍被锁: %w", maxTxAttempts, err)
}

// isBusyErr 判定错误是否为"稍后可成功"的锁冲突。
// 只按错误匹配，绝不把 ErrInsufficientCredits 这类业务拒绝当成可重试。
func isBusyErr(err error) bool {
	if err == nil || errors.Is(err, ErrInsufficientCredits) || errors.Is(err, ErrReservationNotActive) {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "database is locked") ||
		strings.Contains(s, "database is deadlocked") ||
		strings.Contains(s, "sqlite_busy")
}

// DefaultReserveTTL 预留自动回收时限。
//
// 必须显著大于上游客户端超时（provider 侧为 300s），否则一次慢请求会在结算之前
// 被清扫器当成泄漏回收掉。取 6 倍余量。
const DefaultReserveTTL = 30 * time.Minute

// Estimate 计算一次请求应预留的积分。
//
// 故意复用 CreditsForUsage：预估与结算必须是同一套定价算术。两边各写一份的话，
// 以后任何一侧改了公式，另一侧只会悄悄失准而不是报错。
func Estimate(promptTokens, maxCompletion int, p Pricing) int64 {
	return CreditsForUsage(ir.Usage{PromptTokens: promptTokens, CompletionTokens: maxCompletion}, p)
}

// Available 返回扣掉在途预留后的可用余额。
func Available(db *gorm.DB, userID uint) (int64, error) {
	var u model.User
	if err := db.Select("id", "credits", "reserved_credits").First(&u, userID).Error; err != nil {
		return 0, err
	}
	return u.Credits - u.ReservedCredits, nil
}

// Reserve 为一次请求预留 amount 积分。
//
// amount <= 0（免费模型/未定价）时不落预留行、直接返回 nil，调用方仍可无条件 Settle/Release
// （会拿到 ErrReservationNotActive，按无预留路径处理）。
func Reserve(db *gorm.DB, userID, apiKeyID uint, requestID, modelName string, amount int64, ttl time.Duration) error {
	if amount <= 0 {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultReserveTTL
	}
	return txRetry(db, func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).
			Where("id = ? AND credits - reserved_credits >= ?", userID, amount).
			Update("reserved_credits", gorm.Expr("reserved_credits + ?", amount))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 要么余额不够，要么账户已被软删除（GORM 默认加 deleted_at IS NULL）。两者都拒。
			return ErrInsufficientCredits
		}
		return tx.Create(&model.QuotaReservation{
			RequestID: requestID,
			UserID:    userID,
			APIKeyID:  apiKeyID,
			Model:     modelName,
			Amount:    amount,
			Status:    model.ReservationActive,
			ExpiresAt: time.Now().Add(ttl),
		}).Error
	})
}

// SettleOutcome 一次结算的结果，供日志与审计标记。
type SettleOutcome struct {
	Reserved int64 // 预留额
	Actual   int64 // 按实际用量算出的应付额
	Charged  int64 // 真正从余额扣掉的积分
	Overage  int64 // Actual - Charged：未追扣的差额（预估偏低，或余额被管理员扣走）
}

// Settle 按实际用量结算一笔预留：扣减 credits、释放 reserved_credits。
//
// 溢出策略：Actual 超过预留额时按预留额封顶，不追扣。宁可少收，也不能让
// 一个已经拿到响应的请求在最后一步变成"欠费"体验；差额通过 Overage 暴露出来，
// 可观测、可据此回头调 Estimate 的系数。
func Settle(db *gorm.DB, requestID string, actual int64) (SettleOutcome, error) {
	var out SettleOutcome
	err := txRetry(db, func(tx *gorm.DB) error {
		// 重试会重跑整个闭包，先清掉上一轮的半成品，避免失败路径留下混合状态。
		out = SettleOutcome{}

		var r model.QuotaReservation
		if err := tx.Where("request_id = ?", requestID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrReservationNotActive
			}
			return err
		}
		out.Reserved = r.Amount

		// 先认领预留行：active -> settled 的翻转就是一次 CAS。
		// 抢不到说明已被 Settle/Release/Sweep 处理过，直接退出，避免重复扣款。
		claim := tx.Model(&model.QuotaReservation{}).
			Where("request_id = ? AND status = ?", requestID, model.ReservationActive).
			Updates(map[string]interface{}{"status": model.ReservationSettled, "updated_at": time.Now()})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return ErrReservationNotActive
		}

		final := actual
		if final > r.Amount {
			final = r.Amount
		}
		if final < 0 {
			final = 0
		}
		out.Actual = actual

		// 条件 UPDATE 保证"只扣得住确实还在的额度"：管理员在请求途中把余额调走时，
		// 守卫会失败，此时宁可一分不收并打出告警，也不写一条对不上账的流水。
		charge := tx.Model(&model.User{}).
			Where("id = ? AND credits >= ?", r.UserID, final).
			Updates(map[string]interface{}{
				"credits":          gorm.Expr("credits - ?", final),
				"reserved_credits": gorm.Expr("CASE WHEN reserved_credits < ? THEN 0 ELSE reserved_credits - ? END", r.Amount, r.Amount),
				"updated_at":       time.Now(),
			})
		if charge.Error != nil {
			return charge.Error
		}
		if charge.RowsAffected == 0 {
			if final > 0 {
				logrus.Warnf("[%s] 结算跳过：user=%d 余额已低于预留额 %d（可能被管理员扣减），本次未追扣", requestID, r.UserID, final)
			}
			// 仍要释放预留，否则额度被永久占住。
			if err := releaseReserved(tx, r.UserID, r.Amount); err != nil {
				return err
			}
			out.Overage = actual
			return nil
		}

		out.Charged = final
		out.Overage = actual - final
		if final > 0 {
			// 上面的条件 UPDATE 已持有该用户行锁直到本事务提交，此处回读得到的就是
			// 本次扣减后的真实余额（与 Deduct 里同样的写法保持一致）。
			var after model.User
			if err := tx.Select("id", "credits").First(&after, r.UserID).Error; err != nil {
				return err
			}
			return tx.Create(&model.CreditTransaction{
				UserID:       r.UserID,
				Amount:       -final,
				Type:         model.CreditTypeConsume,
				BalanceAfter: after.Credits,
				RequestID:    requestID,
				Model:        r.Model,
			}).Error
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
}

// Release 请求失败时全额退还预留，不动 credits。幂等。
func Release(db *gorm.DB, requestID string) error {
	return txRetry(db, func(tx *gorm.DB) error {
		var r model.QuotaReservation
		if err := tx.Where("request_id = ?", requestID).First(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 从未预留过，无需退还
			}
			return err
		}
		claim := tx.Model(&model.QuotaReservation{}).
			Where("request_id = ? AND status = ?", requestID, model.ReservationActive).
			Updates(map[string]interface{}{"status": model.ReservationReleased, "updated_at": time.Now()})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return nil // 已被处理过，不重复退
		}
		return releaseReserved(tx, r.UserID, r.Amount)
	})
}

// SweepExpired 回收 TTL 到期仍未结算的预留（进程崩溃、panic、连接被杀的唯一兜底）。
// 返回回收条数。
func SweepExpired(db *gorm.DB, now time.Time) (int, error) {
	var stale []model.QuotaReservation
	if err := db.Where("status = ? AND expires_at <= ?", model.ReservationActive, now).
		Find(&stale).Error; err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	swept := 0
	for _, r := range stale {
		err := txRetry(db, func(tx *gorm.DB) error {
			claim := tx.Model(&model.QuotaReservation{}).
				Where("request_id = ? AND status = ?", r.RequestID, model.ReservationActive).
				Updates(map[string]interface{}{"status": model.ReservationExpired, "updated_at": time.Now()})
			if claim.Error != nil {
				return claim.Error
			}
			if claim.RowsAffected == 0 {
				return nil // 刚好被正常路径结算掉了
			}
			logrus.Warnf("预留超时回收：request_id=%s user=%d model=%s amount=%d（请求未在 TTL 内结算）",
				r.RequestID, r.UserID, r.Model, r.Amount)
			return releaseReserved(tx, r.UserID, r.Amount)
		})
		if err != nil {
			logrus.Errorf("回收预留 %s 失败: %v", r.RequestID, err)
			continue
		}
		swept++
	}
	return swept, nil
}

// releaseReserved 把 amount 从已预留额度里放回去，下限钳到 0。
func releaseReserved(tx *gorm.DB, userID uint, amount int64) error {
	if amount <= 0 {
		return nil
	}
	return tx.Model(&model.User{}).Where("id = ?", userID).
		Update("reserved_credits",
			gorm.Expr("CASE WHEN reserved_credits < ? THEN 0 ELSE reserved_credits - ? END", amount, amount)).Error
}
