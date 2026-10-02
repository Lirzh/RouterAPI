package billing

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// newTestDB 建立内存 SQLite 并迁移钱包相关表；单连接以保证 :memory: 一致。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.User{}, &model.CreditTransaction{}, &model.QuotaReservation{}, &model.RedemptionCode{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func seedUser(t *testing.T, db *gorm.DB, credits int64) uint {
	t.Helper()
	u := model.User{Username: "u", Role: model.RoleAdmin, Status: 1, Credits: credits}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return u.ID
}

func TestCreditsForUsage(t *testing.T) {
	cases := []struct {
		name    string
		usage   ir.Usage
		pricing Pricing
		want    int64
	}{
		{"全免费", ir.Usage{PromptTokens: 1000, CompletionTokens: 500}, Pricing{}, 0},
		{"整百万精确", ir.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}, Pricing{Prompt: 100, Completion: 200}, 300},
		{"向上取整prompt", ir.Usage{PromptTokens: 1}, Pricing{Prompt: 1_000_000}, 1},
		{"向上取整不足1积分", ir.Usage{PromptTokens: 1, CompletionTokens: 1}, Pricing{Prompt: 1, Completion: 1}, 2},
		{"混合定价", ir.Usage{PromptTokens: 1500, CompletionTokens: 2500}, Pricing{Prompt: 2000, Completion: 4000}, 3 + 10},
		{"仅completion计费", ir.Usage{CompletionTokens: 1_000_000}, Pricing{Prompt: 500, Completion: 300}, 300},
		{"零用量", ir.Usage{}, Pricing{Prompt: 1000, Completion: 1000}, 0},

		// 缓存维度：未配置独立单价时回落 prompt，金额与引入缓存前一致。
		{"缓存未配置价_等同全价", ir.Usage{PromptTokens: 1_000_000, CachedTokens: 800_000}, Pricing{Prompt: 100}, 100},
		{"缓存读取折扣", ir.Usage{PromptTokens: 1_000_000, CachedTokens: 800_000}, Pricing{Prompt: 100, CacheRead: 10},
			ceilDiv(200_000*100, 1e6) + ceilDiv(800_000*10, 1e6)},
		{"缓存写入加价", ir.Usage{PromptTokens: 1_000_000, CacheWriteTokens: 500_000}, Pricing{Prompt: 100, CacheWrite: 125},
			ceilDiv(500_000*100, 1e6) + ceilDiv(500_000*125, 1e6)},
		// 畸形子集不得把整单算成负数或小额：收缩后按全价 prompt 收。
		{"子集超出prompt_收缩到全价", ir.Usage{PromptTokens: 100, CachedTokens: 5000, CacheWriteTokens: 5000}, Pricing{Prompt: 100, CacheRead: 1}, 1},
		{"负数子集_忽略", ir.Usage{PromptTokens: 1_000_000, CachedTokens: -500}, Pricing{Prompt: 100}, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CreditsForUsage(tc.usage, tc.pricing)
			if got != tc.want {
				t.Errorf("CreditsForUsage(%+v, %+v) = %d, want %d", tc.usage, tc.pricing, got, tc.want)
			}
		})
	}
}

func TestDeductNormal(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)

	after, err := Deduct(db, uid, 300, "req-1", "gpt-test")
	if err != nil {
		t.Fatalf("Deduct 失败: %v", err)
	}
	if after != 700 {
		t.Errorf("余额 = %d, want 700", after)
	}

	bal, _ := Balance(db, uid)
	if bal != 700 {
		t.Errorf("Balance = %d, want 700", bal)
	}

	var txn model.CreditTransaction
	if err := db.Where("request_id = ?", "req-1").First(&txn).Error; err != nil {
		t.Fatalf("未写入流水: %v", err)
	}
	if txn.Amount != -300 || txn.Type != model.CreditTypeConsume || txn.BalanceAfter != 700 || txn.Model != "gpt-test" {
		t.Errorf("流水记录不正确: %+v", txn)
	}
}

func TestDeductClampToZero(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 100)

	// 扣减额超过余额 → clamp 到 0，绝不产生负余额
	after, err := Deduct(db, uid, 500, "req-2", "m")
	if err != nil {
		t.Fatalf("Deduct 失败: %v", err)
	}
	if after != 0 {
		t.Errorf("余额 = %d, want 0（clamp）", after)
	}

	var txn model.CreditTransaction
	db.Where("request_id = ?", "req-2").First(&txn)
	if txn.Amount != -100 {
		t.Errorf("实际扣减流水 = %d, want -100（按可用余额）", txn.Amount)
	}
	if txn.BalanceAfter != 0 {
		t.Errorf("流水余额 = %d, want 0", txn.BalanceAfter)
	}
}

func TestDeductZeroOrNegativeNoop(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 100)

	after, err := Deduct(db, uid, 0, "req-3", "m")
	if err != nil || after != 100 {
		t.Errorf("amount=0 应不扣减: after=%d err=%v", after, err)
	}
	var count int64
	db.Model(&model.CreditTransaction{}).Where("request_id = ?", "req-3").Count(&count)
	if count != 0 {
		t.Errorf("amount=0 不应写流水, got %d", count)
	}
}

func TestAdjustTopupAndClamp(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 0)

	// 充值
	after, err := Adjust(db, uid, 500, model.CreditTypeTopup, "首次充值", 1)
	if err != nil || after != 500 {
		t.Fatalf("充值失败: after=%d err=%v", after, err)
	}

	// 负向调整不超过余额
	after, _ = Adjust(db, uid, -200, model.CreditTypeAdjust, "扣减", 1)
	if after != 300 {
		t.Errorf("调整后 = %d, want 300", after)
	}

	// 负向调整超过余额 → clamp 到 0
	after, _ = Adjust(db, uid, -1000, model.CreditTypeAdjust, "超额扣减", 1)
	if after != 0 {
		t.Errorf("超额扣减后 = %d, want 0（clamp）", after)
	}

	// 流水条数：充值 + 两次调整 = 3
	var count int64
	db.Model(&model.CreditTransaction{}).Where("user_id = ?", uid).Count(&count)
	if count != 3 {
		t.Errorf("流水条数 = %d, want 3", count)
	}
}

func TestAdjustDefaultType(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 0)

	_, _ = Adjust(db, uid, 100, "", "", 0) // 正数默认 topup
	var topup model.CreditTransaction
	db.Where("user_id = ?", uid).Order("id DESC").First(&topup)
	if topup.Type != model.CreditTypeTopup {
		t.Errorf("正数默认类型 = %s, want %s", topup.Type, model.CreditTypeTopup)
	}

	_, _ = Adjust(db, uid, -50, "", "", 0) // 负数默认 adjust
	var adj model.CreditTransaction
	db.Where("user_id = ?", uid).Order("id DESC").First(&adj)
	if adj.Type != model.CreditTypeAdjust {
		t.Errorf("负数默认类型 = %s, want %s", adj.Type, model.CreditTypeAdjust)
	}
}
