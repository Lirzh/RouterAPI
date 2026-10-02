// Package cleanup 提供后台维护任务：按 log_retention_days 分批清理过期调用日志，
// 以及回收泄漏的积分预留。
//
// 日志清理仅涉及 RequestLog（消费/错误日志）；AuditLog 长期保留，不参与清理。
// 预留回收则无条件运行——它守的是"钱被占住拿不回来"，不能跟着日志开关一起被关掉。
package cleanup

import (
	"time"

	"github.com/routerapi/backend/internal/billing"
	"github.com/routerapi/backend/internal/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// batchDeleteSize 每批删除的行数，避免长事务与锁表。
const batchDeleteSize = 1000

// reserveSweepInterval 预留回收轮询间隔。
//
// 比日志清理频繁得多：泄漏的额度在回收前是不可用的，而正常请求远快于 DefaultReserveTTL，
// 所以扫得勤只多几次空查询，却能把崩溃后的额度冻结时间压到分钟级。
const reserveSweepInterval = 5 * time.Minute

// reserveKeepFor 终态预留行的保留时长。
//
// 预留表是"每请求一行"，不清理会随流量线性膨胀。终态行可以激进删除：
// 钱已经落到 CreditTransaction，而那条流水本身就带 request_id，审计链不会断。
const reserveKeepFor = 24 * time.Hour

// Monitor 周期性执行日志保留清理与积分预留回收的后台任务。
type Monitor struct {
	db            *gorm.DB
	retentionDays int
	interval      time.Duration
	stop          chan struct{}
}

// NewMonitor 创建清理任务；retentionDays<=0 仅表示不清理日志，预留回收照常进行。
func NewMonitor(db *gorm.DB, retentionDays int) *Monitor {
	return &Monitor{db: db, retentionDays: retentionDays, interval: time.Hour, stop: make(chan struct{})}
}

// Start 启动后台任务（立即执行一轮，随后按各自节奏轮询）。
func (m *Monitor) Start() {
	if m.retentionDays <= 0 {
		logrus.Info("日志保留天数 <=0，已禁用日志清理（积分预留回收仍然运行）")
	}
	go func() {
		m.sweepReservations() // 启动即回收一次：崩溃前遗留的预留不该继续冻住用户额度
		if m.retentionDays > 0 {
			m.run()
		}

		sweepTicker := time.NewTicker(reserveSweepInterval)
		defer sweepTicker.Stop()
		logTicker := time.NewTicker(m.interval)
		defer logTicker.Stop()

		for {
			select {
			case <-m.stop:
				return
			case <-sweepTicker.C:
				m.sweepReservations()
			case <-logTicker.C:
				if m.retentionDays > 0 {
					m.run()
				}
			}
		}
	}()
	logrus.Infof("后台清理任务已启动（日志保留 %d 天，预留每 %s 回收一次）", m.retentionDays, reserveSweepInterval)
}

// Stop 停止后台清理。
func (m *Monitor) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// run 执行一次清理：删除 created_at 早于保留期的日志。
func (m *Monitor) run() {
	cutoff := time.Now().AddDate(0, 0, -m.retentionDays)
	deleted, err := deleteOlderThan(m.db, cutoff, batchDeleteSize)
	if err != nil {
		logrus.Errorf("日志清理失败: %v", err)
		return
	}
	if deleted > 0 {
		logrus.Infof("日志清理完成：删除 %d 条早于 %s 的调用日志", deleted, cutoff.Format("2006-01-02 15:04:05"))
	}
}

// sweepReservations 回收 TTL 到期仍未结算的预留，并删除过期的终态预留行。
func (m *Monitor) sweepReservations() {
	now := time.Now()
	if n, err := billing.SweepExpired(m.db, now); err != nil {
		logrus.Errorf("积分预留回收失败: %v", err)
	} else if n > 0 {
		logrus.Warnf("积分预留回收：退还 %d 笔超时未结算的预留额度", n)
	}
	// 终态行只作短期排障用，审计链在 CreditTransaction 里，删掉不丢证据。
	res := m.db.Where("status <> ? AND updated_at < ?", model.ReservationActive, now.Add(-reserveKeepFor)).
		Delete(&model.QuotaReservation{})
	if res.Error != nil {
		logrus.Errorf("清理终态预留失败: %v", res.Error)
	}
}

// deleteOlderThan 分批删除早于 cutoff 的调用日志（可移植：DELETE ... WHERE id IN (SELECT ... LIMIT)）。
// 返回累计删除行数。
func deleteOlderThan(db *gorm.DB, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for {
		sub := db.Model(&model.RequestLog{}).Select("id").Where("created_at < ?", cutoff).Limit(batch)
		res := db.Where("id IN (?)", sub).Delete(&model.RequestLog{})
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
		if res.RowsAffected < int64(batch) {
			break
		}
	}
	return total, nil
}
