package billing

import (
	"testing"

	"github.com/glebarez/sqlite"
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
	if err := db.AutoMigrate(&model.User{}, &model.CreditTransaction{}); err != nil {
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
		name                         string
		prompt, completion           int
		promptPer1M, completionPer1M int64
		want                         int64
	}{
		{"全免费", 1000, 500, 0, 0, 0},
		{"整百万精确", 1_000_000, 1_000_000, 100, 200, 300},
		{"向上取整prompt", 1, 0, 1_000_000, 0, 1},    // 1 token × 1e6/1e6 = 1
		{"向上取整不足1积分", 1, 1, 1, 1, 2},             // ceil(1/1e6)=1 各一份
		{"混合定价", 1500, 2500, 2000, 4000, 3 + 10}, // ceil(1500*2000/1e6)=3, ceil(2500*4000/1e6)=10
		{"仅completion计费", 0, 1_000_000, 500, 300, 300},
		{"零用量", 0, 0, 1000, 1000, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CreditsForUsage(tc.prompt, tc.completion, tc.promptPer1M, tc.completionPer1M)
			if got != tc.want {
				t.Errorf("CreditsForUsage(%d,%d,%d,%d) = %d, want %d",
					tc.prompt, tc.completion, tc.promptPer1M, tc.completionPer1M, got, tc.want)
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
