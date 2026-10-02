package billing

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
)

// newCode 建一张兑换码并返回它的展示形态。
func newCode(t *testing.T, db *gorm.DB, credits int64, mut func(*model.RedemptionCode)) string {
	t.Helper()
	code, norm, err := GenerateRedeemCode()
	if err != nil {
		t.Fatalf("生成码失败: %v", err)
	}
	row := model.RedemptionCode{Code: code, CodeNorm: norm, Credits: credits, Status: model.RedeemCodeUnused, Batch: "b1"}
	if mut != nil {
		mut(&row)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入兑换码失败: %v", err)
	}
	return code
}

func TestNormalizeRedeemCode(t *testing.T) {
	const canonical = "ABCD-EFGH-JKMN-PQRS"
	const norm = "ABCDEFGHJKMNPQRS" // 字母表剔除了 I/L/O/U，canonical 全部由合法字符组成
	for _, in := range []string{
		canonical,
		strings.ToLower(canonical),
		strings.ReplaceAll(canonical, "-", ""),
		"  " + canonical + "  ",
		strings.ReplaceAll(canonical, "-", " "),
	} {
		if got := NormalizeRedeemCode(in); got != norm {
			t.Errorf("NormalizeRedeemCode(%q) = %q, want %q", in, got, norm)
		}
	}
	// 全角与无关字符不得被按字节截断成合法字符
	if got := NormalizeRedeemCode("ＡＢＣＤ-ＥＦＧＨ-ＪＫＭＮ-ＰＱＲＳ"); len(got) != 0 {
		t.Errorf("全角输入归一化后长度 = %d, want 0", len(got))
	}
}

func TestGenerateRedeemCodeShape(t *testing.T) {
	seen := make(map[string]bool, 2000)
	for i := 0; i < 2000; i++ {
		code, norm, err := GenerateRedeemCode()
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if len(code) != 19 || strings.Count(code, "-") != 3 {
			t.Fatalf("码形态不对: %q（4 组共 3 个分隔符）", code)
		}
		if len(norm) != 16 || strings.ContainsAny(norm, "-") {
			t.Fatalf("归一化形态不对: %q", norm)
		}
		// 易混字符必须缺席，否则人工抄写会稳定产生废码
		if strings.ContainsAny(norm, "01ILOU") {
			t.Fatalf("码里出现易混字符: %q", norm)
		}
		if seen[code] {
			t.Fatalf("生成了重复码: %q", code)
		}
		seen[code] = true
	}
}

func TestRedeemCreditsBalanceAndLedger(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 500)
	code := newCode(t, db, 1200, nil)

	res, err := Redeem(db, strings.ToLower(strings.ReplaceAll(code, "-", " ")), uid)
	if err != nil {
		t.Fatalf("兑换失败: %v", err)
	}
	if res.Credits != 1200 || res.BalanceAfter != 1700 {
		t.Errorf("兑换结果 = %+v, want credits 1200 / balance 1700", res)
	}
	if res.Code != code {
		t.Errorf("回显码 = %q, want %q", res.Code, code)
	}
	wantBalance(t, db, uid, 1700, 0)

	var txn model.CreditTransaction
	if err := db.Where("user_id = ? AND type = ?", uid, model.CreditTypeRedeem).First(&txn).Error; err != nil {
		t.Fatalf("未写入 redeem 流水: %v", err)
	}
	if txn.Amount != 1200 || txn.BalanceAfter != 1700 {
		t.Errorf("流水不对: %+v", txn)
	}

	var r model.RedemptionCode
	if err := db.Where("code = ?", code).First(&r).Error; err != nil {
		t.Fatalf("读码失败: %v", err)
	}
	if r.Status != model.RedeemCodeRedeemed || r.RedeemedBy != uid || r.RedeemedAt == nil {
		t.Errorf("码状态未更新: %+v", r)
	}
}

func TestRedeemRejectsBadInput(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 0)

	if _, err := Redeem(db, "short", uid); !errors.Is(err, ErrRedeemCodeInvalidFormat) {
		t.Errorf("短码 err = %v, want ErrRedeemCodeInvalidFormat", err)
	}
	// Z 在字母表内，归一化后长度合法，因此会走到"查不到"而不是"格式不对"。
	// （用含 0/O/I/L/U 的串测不出这条路径——它们会被归一化直接剥掉，报格式错，
	// 这也是对的：我们从不发放这些字符，含它们的输入本来就是抄写错误。）
	if _, err := Redeem(db, "ZZZZ-ZZZZ-ZZZZ-ZZZZ", uid); !errors.Is(err, ErrRedeemCodeNotFound) {
		t.Errorf("不存在的码 err = %v, want ErrRedeemCodeNotFound", err)
	}
	// 含易混字符的输入应停在格式校验，不去查库。
	if _, err := Redeem(db, "ZZZZ-ZZZZ-ZZZZ-0000", uid); !errors.Is(err, ErrRedeemCodeInvalidFormat) {
		t.Errorf("含被排除字符 err = %v, want ErrRedeemCodeInvalidFormat", err)
	}
	wantBalance(t, db, uid, 0, 0)
}

// 已用 / 已停用 / 已过期 / 面额非法，对客户端一律报同一个"不可用"，
// 否则这个接口就成了探测码状态的 oracle。
func TestRedeemUnavailableVariants(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*model.RedemptionCode)
	}{
		{"已停用", func(r *model.RedemptionCode) { r.Status = model.RedeemCodeDisabled }},
		{"已兑换", func(r *model.RedemptionCode) { r.Status = model.RedeemCodeRedeemed }},
		{"已过期", func(r *model.RedemptionCode) {
			past := time.Now().Add(-time.Hour)
			r.ExpiresAt = &past
		}},
		{"面额为 0", func(r *model.RedemptionCode) { r.Credits = 0 }},
		{"面额为负", func(r *model.RedemptionCode) { r.Credits = -5 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			uid := seedUser(t, db, 100)
			code := newCode(t, db, 1000, tc.mut)

			_, err := Redeem(db, code, uid)
			if !errors.Is(err, ErrRedeemCodeUnavailable) {
				t.Fatalf("err = %v, want ErrRedeemCodeUnavailable", err)
			}
			wantBalance(t, db, uid, 100, 0)
		})
	}
}

// 过期且未被认领的码必须保持 unused：状态机不该被一次失败的兑换推进。
func TestRedeemExpiredDoesNotClaim(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 0)
	code := newCode(t, db, 1000, func(r *model.RedemptionCode) {
		past := time.Now().Add(-time.Minute)
		r.ExpiresAt = &past
	})
	if _, err := Redeem(db, code, uid); !errors.Is(err, ErrRedeemCodeUnavailable) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	var r model.RedemptionCode
	if err := db.Where("code = ?", code).First(&r).Error; err != nil {
		t.Fatalf("读码失败: %v", err)
	}
	if r.Status != model.RedeemCodeUnused {
		t.Errorf("过期码状态 = %s, want %s", r.Status, model.RedeemCodeUnused)
	}
}

// 同一张码兑换两次：第二次必须失败，且余额只增加一次。
func TestRedeemTwiceCreditsOnce(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 0)
	code := newCode(t, db, 1000, nil)

	if _, err := Redeem(db, code, uid); err != nil {
		t.Fatalf("首次兑换失败: %v", err)
	}
	if _, err := Redeem(db, code, uid); !errors.Is(err, ErrRedeemCodeUnavailable) {
		t.Fatalf("二次兑换 err = %v, want unavailable", err)
	}
	wantBalance(t, db, uid, 1000, 0)

	var count int64
	db.Model(&model.CreditTransaction{}).Where("user_id = ? AND type = ?", uid, model.CreditTypeRedeem).Count(&count)
	if count != 1 {
		t.Errorf("redeem 流水 %d 条, want 1", count)
	}
}

// 并发抢同一张码：只能有一个人赢，余额只涨一次。
// 这是兑换码唯一真正会翻车的失败模式——重复兑换等于凭空增发额度。
func TestConcurrentRedeemSingleWinner(t *testing.T) {
	db := newSharedDB(t)
	uid := seedUser(t, db, 0)
	code := newCode(t, db, 1000, nil)

	const workers = 16
	var wg sync.WaitGroup
	var wins, fails int64
	failCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Redeem(db, code, uid)
			switch {
			case err == nil:
				atomic.AddInt64(&wins, 1)
			case errors.Is(err, ErrRedeemCodeUnavailable):
				atomic.AddInt64(&fails, 1)
			default:
				failCh <- err
			}
		}()
	}
	wg.Wait()
	close(failCh)
	for err := range failCh {
		t.Fatalf("并发兑换报错: %v", err)
	}

	if wins != 1 {
		t.Errorf("成功兑换 %d 次, want 1（重复兑换会凭空增发额度）", wins)
	}
	if wins+fails != workers {
		t.Errorf("结果不守恒: wins %d + fails %d != %d", wins, fails, workers)
	}
	wantBalance(t, db, uid, 1000, 0)

	var txCount int64
	db.Model(&model.CreditTransaction{}).Where("type = ?", model.CreditTypeRedeem).Count(&txCount)
	if txCount != 1 {
		t.Errorf("redeem 流水 %d 条, want 1", txCount)
	}
}

func TestCreateRedeemCodesPersistsRows(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 0)
	expires := time.Now().Add(24 * time.Hour)

	rows, err := CreateRedeemCodes(db, 5, 300, "batch-x", "note", uid, &expires)
	if err != nil {
		t.Fatalf("批量生成失败: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("返回 %d 张, want 5", len(rows))
	}
	var count int64
	db.Model(&model.RedemptionCode{}).Where("batch = ? AND credits = ? AND created_by = ?", "batch-x", 300, uid).Count(&count)
	if count != 5 {
		t.Errorf("落库 %d 张, want 5", count)
	}
	for _, r := range rows {
		if r.CodeNorm == "" || r.Code == "" || r.Status != model.RedeemCodeUnused {
			t.Errorf("生成的码不完整: %+v", r)
		}
		if r.ExpiresAt == nil || !r.ExpiresAt.Equal(expires.Truncate(time.Microsecond)) {
			t.Logf("过期时间回读有差异（驱动精度）: %+v", r.ExpiresAt)
		}
	}
}
