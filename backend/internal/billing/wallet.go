// Package billing 实现简化积分钱包：按 Model 定价计算积分、请求后原子扣减、
// 积分流水账与管理员充值/调整。积分不挂钩现实货币，全部使用整数（int64）避免浮点误差。
package billing

import (
	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
)

// perMillion 定价基准：每 1M token 的积分单价。
const perMillion int64 = 1_000_000

// Pricing 一个模型的积分单价，单位：积分 / 1M token。
//
// CacheRead / CacheWrite 为 0 时沿用 Prompt 单价，即不给缓存折扣、也不对缓存写入加价。
// 这样在没有显式配置缓存定价时，收费金额与引入缓存维度之前逐分一致。
type Pricing struct {
	Prompt     int64
	Completion int64
	CacheRead  int64
	CacheWrite int64
}

// PricingOf 从模型目录提取定价。
func PricingOf(m *model.Model) Pricing {
	if m == nil {
		return Pricing{}
	}
	return Pricing{
		Prompt:     m.PromptCreditsPer1M,
		Completion: m.CompletionCreditsPer1M,
		CacheRead:  m.CacheReadCreditsPer1M,
		CacheWrite: m.CacheWriteCreditsPer1M,
	}
}

// cacheReadRate/ cacheWriteRate 未配置时回落到 prompt 单价。
func (p Pricing) cacheReadRate() int64 {
	if p.CacheRead > 0 {
		return p.CacheRead
	}
	return p.Prompt
}

func (p Pricing) cacheWriteRate() int64 {
	if p.CacheWrite > 0 {
		return p.CacheWrite
	}
	return p.Prompt
}

// CreditsForUsage 按 IR 归一化用量计算应扣积分（分段向上取整后相加）。
//
// 分段而不是先合再取整，是为了让每一段都不低于其真实应付额：
// 先合再整会在多段之间互相抵消掉小数，方向上是少收。
func CreditsForUsage(u ir.Usage, p Pricing) int64 {
	cached, cacheWrite := splitPrompt(u)
	fullPrompt := u.PromptTokens - cached - cacheWrite

	pc := ceilDiv(int64(fullPrompt)*p.Prompt, perMillion) +
		ceilDiv(int64(cached)*p.cacheReadRate(), perMillion) +
		ceilDiv(int64(cacheWrite)*p.cacheWriteRate(), perMillion)
	cc := ceilDiv(int64(u.CompletionTokens)*p.Completion, perMillion)
	return pc + cc
}

// splitPrompt 把缓存子集收缩到不会超过 prompt 总量的范围内。
//
// IR 契约要求 CachedTokens/CacheWriteTokens 是 PromptTokens 的子集，但上游可能返回
// 畸形用量（或某个适配器映射错）。不收缩的话全价 prompt 会算成负数，进而把整单
// 变成一个不该有的小额甚至 0——宁可让这一段按全价收，也不能让它为负。
func splitPrompt(u ir.Usage) (cached, cacheWrite int) {
	cached, cacheWrite = u.CachedTokens, u.CacheWriteTokens
	if cached < 0 {
		cached = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	over := cached + cacheWrite - u.PromptTokens
	if over <= 0 {
		return cached, cacheWrite
	}
	// 先削写入段（占比通常更小），仍超出再削读取段。
	if cut := min(cacheWrite, over); cut > 0 {
		cacheWrite -= cut
		over -= cut
	}
	if over > 0 {
		cached = max(cached-over, 0)
	}
	return cached, cacheWrite
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
	var balanceAfter int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		balanceAfter, err = applyDelta(tx, userID, delta, txType, note, operatorID)
		return err
	})
	return balanceAfter, err
}

// applyDelta 在给定事务内变动余额并写流水，返回变动后的余额。
// 从 Adjust 里拆出来是为了让"兑换码"这类需要与别的写操作同生共死的场景，
// 能复用同一套 clamp 与流水逻辑，而不是靠 GORM 的嵌套事务语义去猜。
func applyDelta(tx *gorm.DB, userID uint, delta int64, txType, note string, operatorID uint) (int64, error) {
	if delta == 0 {
		var u model.User
		if err := tx.Select("id", "credits").First(&u, userID).Error; err != nil {
			return 0, err
		}
		return u.Credits, nil
	}
	if txType == "" {
		if delta > 0 {
			txType = model.CreditTypeTopup
		} else {
			txType = model.CreditTypeAdjust
		}
	}
	// CASE 守卫：入账直接相加；扣减若会越界则清零（可移植，SQLite/PG 均支持）
	if err := tx.Model(&model.User{}).
		Where("id = ?", userID).
		Update("credits", gorm.Expr("CASE WHEN credits + ? < 0 THEN 0 ELSE credits + ? END", delta, delta)).Error; err != nil {
		return 0, err
	}
	var after model.User
	if err := tx.Select("id", "credits").First(&after, userID).Error; err != nil {
		return 0, err
	}
	if err := tx.Create(&model.CreditTransaction{
		UserID:       userID,
		Amount:       delta,
		Type:         txType,
		BalanceAfter: after.Credits,
		Note:         note,
		OperatorID:   operatorID,
	}).Error; err != nil {
		return 0, err
	}
	return after.Credits, nil
}
