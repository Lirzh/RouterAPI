// Package cleanup 提供后台日志保留清理：按 log_retention_days 分批删除过期的调用日志。
// 仅清理 RequestLog（消费/错误日志）；AuditLog 长期保留，不参与清理。
package cleanup

import (
	"time"

	"github.com/routerapi/backend/internal/model"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// batchDeleteSize 每批删除的行数，避免长事务与锁表。
const batchDeleteSize = 1000

// Monitor 周期性清理过期日志的后台任务。
type Monitor struct {
	db            *gorm.DB
	retentionDays int
	interval      time.Duration
	stop          chan struct{}
}

// NewMonitor 创建清理任务；retentionDays<=0 表示不清理（Start 将直接返回）。
func NewMonitor(db *gorm.DB, retentionDays int) *Monitor {
	return &Monitor{db: db, retentionDays: retentionDays, interval: time.Hour, stop: make(chan struct{})}
}

// Start 启动后台清理（立即执行一次，随后按小时轮询）。
func (m *Monitor) Start() {
	if m.retentionDays <= 0 {
		logrus.Info("日志保留天数 <=0，已禁用日志清理任务")
		return
	}
	go func() {
		m.run() // 启动即清理一次
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.run()
			}
		}
	}()
	logrus.Infof("日志清理任务已启动，保留 %d 天，每小时检查一次", m.retentionDays)
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
