package billing

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/routerapi/backend/internal/ir"
	"github.com/routerapi/backend/internal/model"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// dbSeq 给每个并发测试分配唯一的命名内存库。
var dbSeq int64

// newSharedDB 建立允许多连接的内存库，用于真正并发地跑条件 UPDATE 守卫。
// newTestDB 把连接数钉在 1，能验证语义但验证不了竞态。
func newSharedDB(t *testing.T) *gorm.DB {
	t.Helper()
	// DSN 必须每次唯一：cache=shared 的命名内存库会在整个进程内复用，
	// go test -count=N 重跑时撞上 username 唯一索引。
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared&_pragma=busy_timeout(10000)",
		strings.ReplaceAll(t.Name(), "/", "_"), atomic.AddInt64(&dbSeq, 1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("打开共享内存库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	// cache=shared 的内存库会在最后一个物理连接关闭时被销毁，
	// 所以空闲连接不能裁剪；保留 4 个常驻连接即可让库活过整个测试。
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxIdleTime(0)
	sqlDB.SetConnMaxLifetime(0)
	if err := db.AutoMigrate(&model.User{}, &model.CreditTransaction{}, &model.QuotaReservation{}, &model.RedemptionCode{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

func userBalance(t *testing.T, db *gorm.DB, uid uint) (int64, int64) {
	t.Helper()
	var u model.User
	if err := db.Select("id", "credits", "reserved_credits").First(&u, uid).Error; err != nil {
		t.Fatalf("读余额失败: %v", err)
	}
	return u.Credits, u.ReservedCredits
}

func wantBalance(t *testing.T, db *gorm.DB, uid uint, credits, reserved int64) {
	t.Helper()
	gotC, gotR := userBalance(t, db, uid)
	if gotC != credits || gotR != reserved {
		t.Errorf("余额 = credits %d / reserved %d, want credits %d / reserved %d", gotC, gotR, credits, reserved)
	}
}

// 预估与结算必须是同一套算术：这条不变量保证改任何一边都会在这里暴露，
// 而不是让预留悄悄偏离实际收费。
func TestEstimateUsesSameArithmeticAsSettle(t *testing.T) {
	p := Pricing{Prompt: 3000, Completion: 12000}
	usage := ir.Usage{PromptTokens: 4321, CompletionTokens: 987}
	if got, want := Estimate(4321, 987, p), CreditsForUsage(usage, p); got != want {
		t.Errorf("Estimate = %d, CreditsForUsage = %d, 两者必须一致", got, want)
	}
}

func TestReserveRejectsWhenInsufficient(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 100)

	if err := Reserve(db, uid, 1, "req-a", "m", 101, time.Minute); !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("超额预留 err = %v, want ErrInsufficientCredits", err)
	}
	wantBalance(t, db, uid, 100, 0)

	var count int64
	db.Model(&model.QuotaReservation{}).Where("request_id = ?", "req-a").Count(&count)
	if count != 0 {
		t.Errorf("被拒的预留留下了 %d 行预留记录, want 0", count)
	}
}

func TestReserveReservesWithoutSpending(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)

	if err := Reserve(db, uid, 1, "req-b", "m", 400, time.Minute); err != nil {
		t.Fatalf("Reserve 失败: %v", err)
	}
	wantBalance(t, db, uid, 1000, 400) // 预留不动 credits

	avail, err := Available(db, uid)
	if err != nil || avail != 600 {
		t.Errorf("Available = %d (%v), want 600", avail, err)
	}
	// 只剩 600 可用：601 必须被拒，600 必须成功。
	if err := Reserve(db, uid, 1, "req-c", "m", 601, time.Minute); !errors.Is(err, ErrInsufficientCredits) {
		t.Errorf("预留 601 err = %v, want ErrInsufficientCredits", err)
	}
	if err := Reserve(db, uid, 1, "req-d", "m", 600, time.Minute); err != nil {
		t.Errorf("预留 600 应成功, got %v", err)
	}
}

func TestSettleChargesActualAndFreesReservation(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)
	if err := Reserve(db, uid, 7, "req-e", "gpt-test", 500, time.Minute); err != nil {
		t.Fatalf("Reserve 失败: %v", err)
	}

	out, err := Settle(db, "req-e", 300)
	if err != nil {
		t.Fatalf("Settle 失败: %v", err)
	}
	if out.Reserved != 500 || out.Charged != 300 || out.Overage != 0 {
		t.Errorf("结算结果 = %+v, want reserved 500 / charged 300 / overage 0", out)
	}
	wantBalance(t, db, uid, 700, 0) // 只按实际扣，预留全部释放

	var txn model.CreditTransaction
	if err := db.Where("request_id = ?", "req-e").First(&txn).Error; err != nil {
		t.Fatalf("未写入消费流水: %v", err)
	}
	if txn.Amount != -300 || txn.Type != model.CreditTypeConsume || txn.BalanceAfter != 700 || txn.Model != "gpt-test" {
		t.Errorf("流水不正确: %+v", txn)
	}
}

// 溢出策略 (a)：实际超过预留时按预留封顶、不追扣，余额绝不变负。
func TestSettleCapsAtReservedAndReportsOverage(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)
	if err := Reserve(db, uid, 1, "req-f", "m", 200, time.Minute); err != nil {
		t.Fatalf("Reserve 失败: %v", err)
	}

	out, err := Settle(db, "req-f", 900)
	if err != nil {
		t.Fatalf("Settle 失败: %v", err)
	}
	if out.Charged != 200 {
		t.Errorf("Charged = %d, want 200（按预留封顶）", out.Charged)
	}
	if out.Actual != 900 || out.Overage != 700 {
		t.Errorf("溢出未暴露: actual=%d overage=%d, want 900 / 700", out.Actual, out.Overage)
	}
	wantBalance(t, db, uid, 800, 0)
}

func TestSettleIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)
	if err := Reserve(db, uid, 1, "req-g", "m", 500, time.Minute); err != nil {
		t.Fatalf("Reserve 失败: %v", err)
	}
	if _, err := Settle(db, "req-g", 100); err != nil {
		t.Fatalf("首次 Settle 失败: %v", err)
	}
	if _, err := Settle(db, "req-g", 100); !errors.Is(err, ErrReservationNotActive) {
		t.Fatalf("重复 Settle err = %v, want ErrReservationNotActive", err)
	}

	wantBalance(t, db, uid, 900, 0) // 只扣了一次
	var count int64
	db.Model(&model.CreditTransaction{}).Where("request_id = ?", "req-g").Count(&count)
	if count != 1 {
		t.Errorf("消费流水 %d 条, want 1", count)
	}
}

func TestReleaseRefundsWithoutCharging(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)
	if err := Reserve(db, uid, 1, "req-h", "m", 500, time.Minute); err != nil {
		t.Fatalf("Reserve 失败: %v", err)
	}
	if err := Release(db, "req-h"); err != nil {
		t.Fatalf("Release 失败: %v", err)
	}

	wantBalance(t, db, uid, 1000, 0) // 失败请求一分不收，额度还回去

	var count int64
	db.Model(&model.CreditTransaction{}).Where("request_id = ?", "req-h").Count(&count)
	if count != 0 {
		t.Errorf("未发生余额变动却有 %d 条流水, want 0", count)
	}
	// 退过款的预留不能再被结算（否则变成"退了又扣"）。
	if _, err := Settle(db, "req-h", 100); !errors.Is(err, ErrReservationNotActive) {
		t.Errorf("释放后 Settle err = %v, want ErrReservationNotActive", err)
	}
	if err := Release(db, "req-h"); err != nil { // 幂等
		t.Errorf("重复 Release err = %v, want nil", err)
	}
}

func TestSweepExpiredReclaimsLeakedReservation(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 1000)
	if err := Reserve(db, uid, 1, "req-i", "m", 700, time.Minute); err != nil {
		t.Fatalf("Reserve 失败: %v", err)
	}
	wantBalance(t, db, uid, 1000, 700)

	// 直接把到期时间拨到过去，模拟"进程崩溃 / panic 导致结算从未发生"。
	// 不能靠给 Reserve 传负 TTL 来造这个场景——它会兜底成 DefaultReserveTTL。
	if err := db.Model(&model.QuotaReservation{}).Where("request_id = ?", "req-i").
		Update("expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("拨快到期时间失败: %v", err)
	}

	n, err := SweepExpired(db, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("SweepExpired = (%d, %v), want 1", n, err)
	}
	wantBalance(t, db, uid, 1000, 0) // 只退额度，不产生消费

	var r model.QuotaReservation
	if err := db.Where("request_id = ?", "req-i").First(&r).Error; err != nil {
		t.Fatalf("读预留失败: %v", err)
	}
	if r.Status != model.ReservationExpired {
		t.Errorf("预留状态 = %s, want %s", r.Status, model.ReservationExpired)
	}
	// 钱已还给用户，迟到的结算必须被拒。
	if _, err := Settle(db, "req-i", 100); !errors.Is(err, ErrReservationNotActive) {
		t.Errorf("回收后 Settle err = %v, want ErrReservationNotActive", err)
	}
	if n, err := SweepExpired(db, time.Now()); err != nil || n != 0 {
		t.Errorf("二次清扫 = (%d, %v), want 0", n, err)
	}
}

func TestZeroAmountReserveIsNoop(t *testing.T) {
	db := newTestDB(t)
	uid := seedUser(t, db, 100)

	if err := Reserve(db, uid, 1, "req-j", "m", 0, time.Minute); err != nil {
		t.Errorf("免费模型预留应直接放行, got %v", err)
	}
	wantBalance(t, db, uid, 100, 0)
	var count int64
	db.Model(&model.QuotaReservation{}).Count(&count)
	if count != 0 {
		t.Errorf("零额度预留落了 %d 行, want 0", count)
	}
}

// 预留守卫的超扣测试：只做预留、不结算，这样"同时挂在账上的预留之和"
// 成为一个可直接断言的量。守卫失效时 reserved_credits 会冲到余额之上。
func TestConcurrentReserveGuardHonorsBalance(t *testing.T) {
	db := newSharedDB(t)
	uid := seedUser(t, db, 1000)

	const workers = 32
	const per = 100 // 32 * 100 = 3200 > 1000，必然出现拒绝

	var wg sync.WaitGroup
	var okCount, rejectCount int64
	failCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := Reserve(db, uid, 1, fmt.Sprintf("g-%d", i), "m", per, time.Hour)
			switch {
			case err == nil:
				atomic.AddInt64(&okCount, 1)
			case errors.Is(err, ErrInsufficientCredits):
				atomic.AddInt64(&rejectCount, 1)
			default:
				failCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(failCh)
	for err := range failCh {
		t.Fatalf("并发 Reserve 报了非预期错误: %v", err)
	}

	ok := atomic.LoadInt64(&okCount)
	if ok == 0 {
		t.Fatal("没有任何预留成功，测试没跑到并发路径")
	}
	if ok+atomic.LoadInt64(&rejectCount) != workers {
		t.Errorf("尝试数不守恒: ok %d + reject %d != %d", ok, rejectCount, workers)
	}
	if ok > 10 {
		t.Errorf("成功预留 %d 笔 × %d = %d，突破初始余额 1000", ok, per, ok*per)
	}
	credits, reserved := userBalance(t, db, uid)
	if reserved != ok*per {
		t.Errorf("reserved_credits = %d, want %d（与成功笔数一致）", reserved, ok*per)
	}
	if credits != 1000 {
		t.Errorf("未结算时 credits = %d, want 1000（预留不动余额）", credits)
	}
}

// 预留与结算交错时的资金守恒：余额不变负、预留最终归零、
// 流水合计恰好等于余额变动、且每笔成功预留只结算一次。
func TestConcurrentReserveSettleConservesMoney(t *testing.T) {
	db := newSharedDB(t)
	uid := seedUser(t, db, 1000)

	const workers = 32
	const per = 100
	const charged = per / 2

	var wg sync.WaitGroup
	var okCount int64
	failCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rid := fmt.Sprintf("s-%d", i)
			if err := Reserve(db, uid, 1, rid, "m", per, time.Hour); err != nil {
				if !errors.Is(err, ErrInsufficientCredits) {
					failCh <- err
				}
				return
			}
			atomic.AddInt64(&okCount, 1)
			out, err := Settle(db, rid, charged)
			if err != nil {
				failCh <- err
			} else if out.Charged != charged {
				failCh <- fmt.Errorf("%s Charged = %d, want %d", rid, out.Charged, charged)
			}
		}(i)
	}
	wg.Wait()
	close(failCh)
	for err := range failCh {
		t.Fatalf("并发路径报错: %v", err)
	}

	ok := atomic.LoadInt64(&okCount)
	credits, reserved := userBalance(t, db, uid)
	if credits < 0 {
		t.Errorf("余额变负: %d", credits)
	}
	if reserved != 0 {
		t.Errorf("全部结算后预留未归零: %d", reserved)
	}
	if want := int64(1000) - ok*charged; credits != want {
		t.Errorf("credits = %d, want %d", credits, want)
	}

	// 账实核对：余额变动必须等于 consume 流水合计，且流水条数等于成功预留笔数。
	var ledger struct{ Total int64 }
	if err := db.Model(&model.CreditTransaction{}).
		Where("user_id = ? AND type = ?", uid, model.CreditTypeConsume).
		Select("COALESCE(SUM(-amount), 0) AS total").Scan(&ledger).Error; err != nil {
		t.Fatalf("汇总流水失败: %v", err)
	}
	if ledger.Total != 1000-credits {
		t.Errorf("账实不符：流水合计 %d, 余额变动 %d", ledger.Total, 1000-credits)
	}
	var txCount int64
	db.Model(&model.CreditTransaction{}).Where("user_id = ?", uid).Count(&txCount)
	if txCount != ok {
		t.Errorf("流水 %d 条, want %d（每笔成功预留恰好结算一次）", txCount, ok)
	}
}
