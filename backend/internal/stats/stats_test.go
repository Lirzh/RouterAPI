package stats

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.RequestLog{}, &model.StatData{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func TestRecordUpsertSameGroup(t *testing.T) {
	db := newTestDB(t)
	ts := time.Date(2025, 1, 1, 10, 15, 0, 0, time.Local) // 同一整点

	Record(db, model.RequestLog{CreatedAt: ts, Model: "m1", VendorID: 1, APIKeyID: 2, UserID: 3,
		Success: true, TokenPrompt: 10, TokenCompletion: 20, TokenTotal: 30, CreditsCost: 5})
	Record(db, model.RequestLog{CreatedAt: ts.Add(20 * time.Minute), Model: "m1", VendorID: 1, APIKeyID: 2, UserID: 3,
		Success: false, TokenPrompt: 100, TokenCompletion: 200, TokenTotal: 300, CreditsCost: 50})

	var count int64
	db.Model(&model.StatData{}).Count(&count)
	if count != 1 {
		t.Fatalf("同组应聚合为 1 行, got %d", count)
	}
	var s model.StatData
	db.First(&s)
	if s.Count != 2 || s.Success != 1 {
		t.Errorf("count/success = %d/%d, want 2/1", s.Count, s.Success)
	}
	if s.PromptTokens != 110 || s.CompletionTokens != 220 || s.TotalTokens != 330 || s.CreditsCost != 55 {
		t.Errorf("聚合用量错误: %+v", s)
	}
	if s.Hour != model.HourBucket(ts.Unix()) {
		t.Errorf("hour = %d, want %d", s.Hour, model.HourBucket(ts.Unix()))
	}
}

func TestRecordDifferentGroups(t *testing.T) {
	db := newTestDB(t)
	ts := time.Date(2025, 1, 1, 10, 0, 0, 0, time.Local)
	Record(db, model.RequestLog{CreatedAt: ts, Model: "m1", VendorID: 1, APIKeyID: 1, Success: true})
	Record(db, model.RequestLog{CreatedAt: ts, Model: "m2", VendorID: 1, APIKeyID: 1, Success: true}) // 不同 model
	Record(db, model.RequestLog{CreatedAt: ts.Add(time.Hour), Model: "m1", VendorID: 1, APIKeyID: 1}) // 不同 hour

	var count int64
	db.Model(&model.StatData{}).Count(&count)
	if count != 3 {
		t.Errorf("不同组应为 3 行, got %d", count)
	}
}

func TestRecompute(t *testing.T) {
	db := newTestDB(t)
	base := time.Date(2025, 3, 10, 8, 0, 0, 0, time.Local)
	logs := []model.RequestLog{
		{CreatedAt: base, Model: "a", VendorID: 1, APIKeyID: 1, Success: true, TokenTotal: 100, CreditsCost: 10},
		{CreatedAt: base.Add(30 * time.Minute), Model: "a", VendorID: 1, APIKeyID: 1, Success: true, TokenTotal: 50, CreditsCost: 5},
		{CreatedAt: base.Add(time.Hour), Model: "a", VendorID: 2, APIKeyID: 1, Success: false, TokenTotal: 0, CreditsCost: 0},
		{CreatedAt: base.Add(48 * time.Hour), Model: "b", VendorID: 1, APIKeyID: 1, Success: true, TokenTotal: 7, CreditsCost: 1}, // 范围外
	}
	for i := range logs {
		if err := db.Create(&logs[i]).Error; err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	// 先制造一条错误的预聚合，验证 Recompute 会覆盖修正
	Record(db, model.RequestLog{CreatedAt: base, Model: "a", VendorID: 1, APIKeyID: 1, Success: true, TokenTotal: 999, CreditsCost: 999})

	from := base.Add(-time.Hour)
	to := base.Add(24 * time.Hour) // 仅覆盖前 3 条
	n, err := Recompute(db, from, to)
	if err != nil {
		t.Fatalf("Recompute 失败: %v", err)
	}
	// 期望分组：(8点,a,v1,k1) + (9点,a,v2,k1) = 2 组
	if n != 2 {
		t.Errorf("重建行数 = %d, want 2", n)
	}

	var s model.StatData
	if err := db.Where("model = ? AND vendor_id = ?", "a", 1).First(&s).Error; err != nil {
		t.Fatalf("未找到聚合行: %v", err)
	}
	if s.Count != 2 || s.Success != 2 || s.TotalTokens != 150 || s.CreditsCost != 15 {
		t.Errorf("重算后聚合错误(应覆盖旧的999): %+v", s)
	}

	// 范围外的 b 不应被聚合
	var bCount int64
	db.Model(&model.StatData{}).Where("model = ?", "b").Count(&bCount)
	if bCount != 0 {
		t.Errorf("范围外数据不应被重算, got %d 行", bCount)
	}
}
