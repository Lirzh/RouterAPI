package health

import (
	"context"
	"net/http"
	"time"

	"github.com/routerapi/backend/internal/balancer"
	"github.com/routerapi/backend/internal/errkit"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/internal/provider"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Monitor 后台健康探测：对处于熔断冷却中的供应商做被动探测（借鉴 new-api passive_recovery）。
//   - 探测成功 → 立即清除冷却，供应商恢复参与路由（无需等待冷却自然到期）。
//   - 探测失败 → 延长冷却，避免真实请求继续打到已知故障供应商。
//
// 仅探测被自动熔断（cooldown_until 非空）且未被手动禁用（status=1）的供应商；
// 手动禁用（status=0）的供应商不会被自动恢复，尊重运维意图。
type Monitor struct {
	db           *gorm.DB
	bal          *balancer.Balancer
	interval     time.Duration
	probeTimeout time.Duration
	stop         chan struct{}
}

// NewMonitor 创建健康探测器；intervalMinutes<=0 时默认 5 分钟。
func NewMonitor(db *gorm.DB, bal *balancer.Balancer, intervalMinutes int) *Monitor {
	if intervalMinutes <= 0 {
		intervalMinutes = 5
	}
	return &Monitor{
		db:           db,
		bal:          bal,
		interval:     time.Duration(intervalMinutes) * time.Minute,
		probeTimeout: 15 * time.Second,
		stop:         make(chan struct{}),
	}
}

// Start 启动后台探测协程。
func (m *Monitor) Start() {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.probeOnce()
			case <-m.stop:
				return
			}
		}
	}()
	logrus.Infof("供应商健康探测已启动，间隔 %s", m.interval)
}

// Stop 停止后台探测。
func (m *Monitor) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// probeOnce 执行一轮探测。
func (m *Monitor) probeOnce() {
	var vendors []model.Vendor
	if err := m.db.Where("cooldown_until IS NOT NULL AND status = 1").Find(&vendors).Error; err != nil {
		logrus.Warnf("健康探测查询供应商失败: %v", err)
		return
	}
	if len(vendors) == 0 {
		return
	}
	for i := range vendors {
		v := vendors[i]
		if v.APIKey != "" {
			if dec, err := crypto.Decrypt(v.APIKey); err == nil {
				v.APIKey = dec
			}
		}
		prov, err := provider.NewFromVendor(&v)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), m.probeTimeout)
		_, perr := prov.Models(ctx)
		cancel()

		switch {
		case perr == nil:
			m.bal.RecoverVendor(v.ID)
		case probeInconclusive(perr):
			// 上游应答了，只是没实现模型发现端点（不少 Claude/Gemini 兼容网关只有对话端点）。
			// 此时既不能判健康（密钥可能已经死了），也绝不能延长冷却——那会把一个
			// 对话完全正常的供应商永久锁死在"探测失败 -> 续期"里。让它自然到期，交给真实流量判定。
			logrus.Debugf("健康探测：供应商 %d 无模型发现端点，保持冷却至自然到期", v.ID)
		default:
			m.bal.ExtendCooldown(v.ID, perr.Error())
			logrus.Debugf("健康探测：供应商 %d 仍不可用，延长冷却: %v", v.ID, perr)
		}
	}
}

// probeInconclusive 判定一次探测失败是否"说明不了健康问题"。
//
// 404/405 表示服务是活的（DNS/TLS/HTTP 都通了），只是该上游不提供模型列表；
// 其余（401/403/429/5xx/网络层错误）才是明确的"还没恢复"信号。
func probeInconclusive(err error) bool {
	code := errkit.FromError(err).StatusCode
	return code == http.StatusNotFound || code == http.StatusMethodNotAllowed
}
