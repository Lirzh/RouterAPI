// 积分兑换码：credits 的发放路径之一。
//
// 兑换码是 bearer 凭证——谁拿到码谁就能兑换。因此本文件之外还有两条配套约束：
//   - middleware/audit.go 的字段白名单不得加入 "code"
//   - 管理端列表接口只回显脱敏码，完整码仅在批量生成的响应里出现一次
package billing

import (
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
)

var (
	// ErrRedeemCodeNotFound 码不存在。与"不可用"分开报错会让枚举有效码变得可行，
	// 但这里两者对客户端返回同一句话（见 handler），分开只是为了服务端日志可诊断。
	ErrRedeemCodeNotFound = errors.New("兑换码不存在")
	// ErrRedeemCodeUnavailable 码已被兑换、已停用或已过期。
	ErrRedeemCodeUnavailable = errors.New("兑换码不可用")
	// ErrRedeemCodeInvalidFormat 输入归一化后不是一个合法形态的码。
	ErrRedeemCodeInvalidFormat = errors.New("兑换码格式不正确")
)

// redeemAlphabet 去掉了易混字符（0/O、1/I/L、U），避免人工抄写产生的废码。
const redeemAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// redeemGroups / redeemGroupLen 码的展示形态：XXXX-XXXX-XXXX-XXXX。
const (
	redeemGroups    = 4
	redeemGroupLen  = 4
	redeemCharCount = redeemGroups * redeemGroupLen // 16 字符 ≈ 78 bit 熵（30 字符表）
)

// GenerateRedeemCode 生成一个新码的展示形态与归一化形态。
// 用 crypto/rand：兑换码等同现金券，math/rand 可预测不是能接受的风险。
func GenerateRedeemCode() (code, codeNorm string, err error) {
	max := big.NewInt(int64(len(redeemAlphabet)))
	buf := make([]byte, redeemCharCount)
	for i := range buf {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", "", err
		}
		buf[i] = redeemAlphabet[n.Int64()]
	}
	norm := string(buf)
	groups := make([]string, 0, redeemGroups)
	for i := 0; i < len(norm); i += redeemGroupLen {
		groups = append(groups, norm[i:i+redeemGroupLen])
	}
	return strings.Join(groups, "-"), norm, nil
}

// NormalizeRedeemCode 把用户可能粘贴成的任意写法（小写、带空格、带连字符、全角）
// 归一化到 CodeNorm 形态。零填充被排除在字母表外，所以不需要额外去歧义处理。
func NormalizeRedeemCode(input string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(input)) {
		if r > 127 {
			continue // 全角/非 ASCII 字符直接忽略，不做 byte 截断（会把多字节码点切成 ASCII）
		}
		if strings.IndexByte(redeemAlphabet, byte(r)) >= 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RedeemResult 一次成功兑换的结果。
type RedeemResult struct {
	Code         string
	Credits      int64
	BalanceAfter int64
}

// Redeem 原子地兑换一个码：先 CAS 认领，再入账，全在一个事务里。
//
// 认领语句是 `WHERE id = ? AND status = 'unused'` 的条件 UPDATE，靠 RowsAffected 判定。
// 两个人同时抢同一个码时，只有一个能改到行，另一个拿到 ErrRedeemCodeUnavailable —
// 不会重复入账，也不需要 SELECT FOR UPDATE（SQLite 不支持该语法）。
func Redeem(db *gorm.DB, input string, userID uint) (RedeemResult, error) {
	var out RedeemResult
	norm := NormalizeRedeemCode(input)
	if len(norm) != redeemCharCount {
		return out, ErrRedeemCodeInvalidFormat
	}

	err := txRetry(db, func(tx *gorm.DB) error {
		var c model.RedemptionCode
		if err := tx.Where("code_norm = ?", norm).First(&c).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRedeemCodeNotFound
			}
			return err
		}

		now := time.Now()
		// 过期条件与认领写进同一条 SQL：既避免"先查再判"的时间窗，
		// 也让过期的码根本不会被认领，无需回滚。
		claim := tx.Model(&model.RedemptionCode{}).
			Where("id = ? AND status = ? AND credits > 0", c.ID, model.RedeemCodeUnused).
			Where("expires_at IS NULL OR expires_at > ?", now).
			Updates(map[string]interface{}{
				"status":      model.RedeemCodeRedeemed,
				"redeemed_by": userID,
				"redeemed_at": now,
				"updated_at":  now,
			})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			// 已被兑换、已停用、已过期或面额非法，都会走到这里；不区分返回，
			// 免得给外部一个"探测码状态"的 oracle。服务端日志留细节。
			return ErrRedeemCodeUnavailable
		}

		balance, err := applyDelta(tx, userID, c.Credits, model.CreditTypeRedeem, "redeem:"+c.Batch, userID)
		if err != nil {
			return err
		}
		out = RedeemResult{Code: c.Code, Credits: c.Credits, BalanceAfter: balance}
		return nil
	})
	return out, err
}

// CreateRedeemCodes 批量生成兑换码。count 与 credits 的上限由 handler 侧校验。
func CreateRedeemCodes(db *gorm.DB, count int, credits int64, batch, note string, createdBy uint, expiresAt *time.Time) ([]model.RedemptionCode, error) {
	created := make([]model.RedemptionCode, 0, count)
	err := db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			code, norm, err := GenerateRedeemCode()
			if err != nil {
				return err
			}
			row := model.RedemptionCode{
				Code: code, CodeNorm: norm, Credits: credits, Batch: batch, Note: note,
				Status: model.RedeemCodeUnused, CreatedBy: createdBy, ExpiresAt: expiresAt,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			created = append(created, row)
		}
		return nil
	})
	return created, err
}
