// Package billing 实现简化积分钱包：按 Model 定价计算积分、请求后原子扣减、
// 积分流水账与管理员充值/调整。积分不挂钩现实货币，全部使用整数（int64）避免浮点误差。
package billing

import (
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
)

// perMillion 定价基准：每 1M token 的积分单价。
const perMillion int64 = 1_000_000

// CreditsForUsage 依据 token 用量与模型单价计算应扣积分（整数，向上取整）。
// prompt/completion 分别按各自单价计算后取整相加，避免小数积分与浮点误差。
// 单价为 0 表示该部分免费。
func CreditsForUsage(promptTokens, completionTokens int, promptPer1M, completionPer1M int64) int64 {
	pc := ceilDiv(int64(promptTokens)*promptPer1M, perMillion)
	cc := ceilDiv(int64(completionTokens)*completionPer1M, perMillion)
	return pc + cc
}

// ceilDiv 非负整数向上取整除法；被除数 <=0 或除数 <=0 时返回 0。
func ceilDiv(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// Balance 查询用户当前积分余额。
func Balance(db *gorm.DB, userID uint) (int64, error) {
	var u model.User
	if err := db.Select("id", "credits").First(&u, userID).Error; err != nil {
		return 0, err
	}
	return u.Credits, nil
}

// Deduct 请求成功后按消费额原子扣减积分并写流水（type=consume）。
// 采用相对扣减 credits = credits - deduct，并 clamp 到 0（余额不足扣至 0，绝不产生负值）。
// 返回扣减后的余额。amount<=0 时不扣减，直接返回当前余额。
func Deduct(db *gorm.DB, userID uint, amount int64, requestID, modelName string) (int64, error) {
	if amount <= 0 {
		return Balance(db, userID)
	}
	var balanceAfter int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Select("id", "credits").First(&u, userID).Error; err != nil {
			return err
		}
		// 余额不足则扣至 0（clamp），不产生负余额
		deduct := amount
		if u.Credits < deduct {
			deduct = u.Credits
		}
		if deduct < 0 {
			deduct = 0
		}
		if deduct > 0 {
			// WHERE credits >= deduct 守卫：并发下若余额已被其他事务扣低则本次不扣，防止越扣为负
			if err := tx.Model(&model.User{}).
				Where("id = ? AND credits >= ?", userID, deduct).
				Update("credits", gorm.Expr("credits - ?", deduct)).Error; err != nil {
				return err
			}
		}
		var after model.User
		if err := tx.Select("id", "credits").First(&after, userID).Error; err != nil {
			return err
		}
		balanceAfter = after.Credits
		return tx.Create(&model.CreditTransaction{
			UserID:       userID,
			Amount:       -deduct,
			Type:         model.CreditTypeConsume,
			BalanceAfter: after.Credits,
			RequestID:    requestID,
			Model:        modelName,
		}).Error
	})
	return balanceAfter, err
}

// Adjust 管理员充值/调整积分（delta 正=入账，负=扣减），并写流水。
// 扣减时 clamp 到 0，绝不让余额变为负值。返回调整后的余额。
func Adjust(db *gorm.DB, userID uint, delta int64, txType, note string, operatorID uint) (int64, error) {
	if delta == 0 {
		return Balance(db, userID)
	}
	if txType == "" {
		if delta > 0 {
			txType = model.CreditTypeTopup
		} else {
			txType = model.CreditTypeAdjust
		}
	}
	var balanceAfter int64
	err := db.Transaction(func(tx *gorm.DB) error {
		// CASE 守卫：入账直接相加；扣减若会越界则清零（可移植，SQLite/PG 均支持）
		if err := tx.Model(&model.User{}).
			Where("id = ?", userID).
			Update("credits", gorm.Expr("CASE WHEN credits + ? < 0 THEN 0 ELSE credits + ? END", delta, delta)).Error; err != nil {
			return err
		}
		var after model.User
		if err := tx.Select("id", "credits").First(&after, userID).Error; err != nil {
			return err
		}
		balanceAfter = after.Credits
		return tx.Create(&model.CreditTransaction{
			UserID:       userID,
			Amount:       delta,
			Type:         txType,
			BalanceAfter: after.Credits,
			Note:         note,
			OperatorID:   operatorID,
		}).Error
	})
	return balanceAfter, err
}
