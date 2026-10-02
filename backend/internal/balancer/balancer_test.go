package balancer

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// newBalancer 建一个带单个"模型-绑定-供应商"链路的内存库，并返回已预热路由缓存的 Balancer。
func newBalancer(t *testing.T) (*Balancer, *gorm.DB, uint, uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if e := db.AutoMigrate(&model.Vendor{}, &model.Model{}, &model.ModelBinding{}); e != nil {
		t.Fatalf("迁移失败: %v", e)
	}

	v := model.Vendor{Name: "v1", Protocol: model.ProtocolOpenAICompat, Status: 1, Weight: 1}
	if e := db.Create(&v).Error; e != nil {
		t.Fatalf("建供应商失败: %v", e)
	}
	m := model.Model{Name: "m1", Enabled: true}
	if e := db.Create(&m).Error; e != nil {
		t.Fatalf("建模型失败: %v", e)
	}
	b := model.ModelBinding{ModelID: m.ID, VendorID: v.ID, UpstreamModel: "up1", Weight: 1, Priority: 0, Enabled: true}
	if e := db.Create(&b).Error; e != nil {
		t.Fatalf("建绑定失败: %v", e)
	}

	bal := New(db)
	if _, e := bal.Route("m1", 0, nil); e != nil {
		t.Fatalf("首次路由失败: %v", e)
	}
	return bal, db, v.ID, m.ID
}

// 未到熔断阈值时不得清空路由缓存。
//
// 原实现在每次供应商失败时无条件 `b.cache = make(...)`，且横跨两次 DB 写持有 b.mu。
// 上游 brownout 时每次失败都全量失效缓存 -> 每个请求回源查 model+bindings+vendor，
// 等于把一次上游故障放大成自我 DDOS。
func TestMarkVendorErrorKeepsCacheBelowThreshold(t *testing.T) {
	bal, db, vid, _ := newBalancer(t)

	for i := 0; i < cooldownErrorThreshold-1; i++ {
		bal.MarkVendorError(vid, "boom")
	}

	var v model.Vendor
	if err := db.First(&v, vid).Error; err != nil {
		t.Fatalf("读供应商失败: %v", err)
	}
	if v.ConsecutiveErrors != cooldownErrorThreshold-1 {
		t.Errorf("连续错误 = %d, want %d", v.ConsecutiveErrors, cooldownErrorThreshold-1)
	}
	if v.CooldownUntil != nil {
		t.Error("未达阈值就进入了冷却")
	}
	bal.mu.RLock()
	cacheLen := len(bal.cache)
	bal.mu.RUnlock()
	if cacheLen == 0 {
		t.Error("未达阈值却清空了路由缓存（会引发回源风暴）")
	}
}

// 达到阈值：置冷却、清零计数、并让缓存失效（冷却确实改变了路由可用性，必须失效）。
func TestMarkVendorErrorTripsCooldown(t *testing.T) {
	bal, db, vid, _ := newBalancer(t)

	for i := 0; i < cooldownErrorThreshold; i++ {
		bal.MarkVendorError(vid, "boom")
	}

	var v model.Vendor
	if err := db.First(&v, vid).Error; err != nil {
		t.Fatalf("读供应商失败: %v", err)
	}
	if v.CooldownUntil == nil || !v.CooldownUntil.After(time.Now()) {
		t.Errorf("未进入冷却: %v", v.CooldownUntil)
	}
	if v.ConsecutiveErrors != 0 {
		t.Errorf("冷却时计数应重置, got %d", v.ConsecutiveErrors)
	}
	bal.mu.RLock()
	cacheLen := len(bal.cache)
	bal.mu.RUnlock()
	if cacheLen != 0 {
		t.Error("进入冷却后未失效路由缓存，冷却中的供应商仍会被路由到")
	}
}

// 冷却中的供应商必须被 Route 排除；RecoverVendor 之后重新可路由。
func TestRouteSkipsCoolingDownVendor(t *testing.T) {
	bal, _, vid, _ := newBalancer(t)

	future := time.Now().Add(time.Minute)
	if err := bal.db.Model(&model.Vendor{}).Where("id = ?", vid).
		Update("cooldown_until", future).Error; err != nil {
		t.Fatalf("设置冷却失败: %v", err)
	}
	bal.invalidateCache()

	if _, err := bal.Route("m1", 0, nil); err == nil {
		t.Error("冷却中的供应商仍可被路由")
	}

	bal.RecoverVendor(vid)
	route, err := bal.Route("m1", 0, nil)
	if err != nil {
		t.Fatalf("恢复后路由失败: %v", err)
	}
	if route.Vendor.ID != vid {
		t.Errorf("路由到 %d, want %d", route.Vendor.ID, vid)
	}
}

// MarkVendorOK 清零错误计数，但不得清空缓存——consecutive_errors 不参与路由筛选，
// 清它只会白白制造回源。
func TestMarkVendorOKDoesNotInvalidateCache(t *testing.T) {
	bal, db, vid, _ := newBalancer(t)
	bal.MarkVendorError(vid, "boom") // 未达阈值，只累加计数

	bal.MarkVendorOK(vid)

	var v model.Vendor
	if err := db.First(&v, vid).Error; err != nil {
		t.Fatalf("读供应商失败: %v", err)
	}
	if v.ConsecutiveErrors != 0 {
		t.Errorf("成功未清零计数: %d", v.ConsecutiveErrors)
	}
	bal.mu.RLock()
	cacheLen := len(bal.cache)
	bal.mu.RUnlock()
	if cacheLen == 0 {
		t.Error("MarkVendorOK 不该清空路由缓存")
	}
}
