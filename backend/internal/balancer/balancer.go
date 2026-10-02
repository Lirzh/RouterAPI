package balancer

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// 熔断策略常量（P0 内置默认，后续可提为配置）
const (
	// cooldownErrorThreshold 连续错误达到该阈值后触发熔断冷却
	cooldownErrorThreshold = 3
	// cooldownDuration 熔断冷却时长，到期后由健康探测或时间自动放行
	cooldownDuration = 60 * time.Second
)

// RouteResult 路由结果：选中的模型 + 绑定 + 供应商（供应商 APIKey 已解密）。
type RouteResult struct {
	Model   *model.Model
	Binding *model.ModelBinding
	Vendor  *model.Vendor
}

// routeCacheTTL 路由缓存存活时间：管理端配置变更最多在该时长后生效；
// 供应商熔断/恢复等状态变更会立即失效缓存。
const routeCacheTTL = 3 * time.Second

// routeEntry 缓存的路由数据（模型 + 其绑定，供应商密钥仍为密文，选中后才解密）。
type routeEntry struct {
	model    model.Model
	bindings []model.ModelBinding
	expires  time.Time
}

// candidate 参与负载均衡的候选（绑定 + 其供应商 + 生效权重/优先级）。
type candidate struct {
	binding  model.ModelBinding
	vendor   model.Vendor
	priority int
	weight   int
}

// Balancer 负载均衡器。
type Balancer struct {
	db    *gorm.DB
	mu    sync.RWMutex
	cache map[string]*routeEntry
}

func New(db *gorm.DB) *Balancer {
	return &Balancer{db: db, cache: make(map[string]*routeEntry)}
}

// getRouteData 返回模型及其启用绑定（含供应商，密钥为密文），带短 TTL 缓存。
func (b *Balancer) getRouteData(modelName string) (*model.Model, []model.ModelBinding, error) {
	b.mu.RLock()
	if e, ok := b.cache[modelName]; ok && time.Now().Before(e.expires) {
		m := e.model
		bds := e.bindings
		b.mu.RUnlock()
		return &m, bds, nil
	}
	b.mu.RUnlock()

	var m model.Model
	if err := b.db.Where("name = ? AND enabled = ?", modelName, true).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.New("模型不存在或已停用: " + modelName)
		}
		return nil, nil, err
	}
	var bindings []model.ModelBinding
	if err := b.db.Where("model_id = ? AND enabled = ?", m.ID, true).
		Preload("Vendor").Find(&bindings).Error; err != nil {
		return nil, nil, err
	}

	b.mu.Lock()
	b.cache[modelName] = &routeEntry{model: m, bindings: bindings, expires: time.Now().Add(routeCacheTTL)}
	b.mu.Unlock()
	return &m, bindings, nil
}

// invalidateCache 清空路由缓存（供应商熔断/恢复等状态变更时调用）。
func (b *Balancer) invalidateCache() {
	b.mu.Lock()
	b.cache = make(map[string]*routeEntry)
	b.mu.Unlock()
}

// Route 为虚拟模型名选择一个 (绑定 + 供应商)。
//   - retry：重试层级，0=最高优先级层，逐层降级（借鉴 new-api "retry 即降级"）。
//   - excludeBindingIDs：本次请求已尝试过的绑定，需排除以避免重复选中。
//
// 过滤条件：Model.Enabled、Binding.Enabled、Vendor.Status==1、Vendor 不在冷却期。
func (b *Balancer) Route(modelName string, retry int, excludeBindingIDs map[uint]bool) (*RouteResult, error) {
	// 1-2) 查模型 + 绑定（走短 TTL 路由缓存，减少热路径 DB 压力）
	m, bindings, err := b.getRouteData(modelName)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, errors.New("模型没有可用的供应商绑定: " + modelName)
	}

	now := time.Now()
	// 3) 过滤出可调度候选
	candidates := make([]candidate, 0, len(bindings))
	for _, bd := range bindings {
		if excludeBindingIDs != nil && excludeBindingIDs[bd.ID] {
			continue
		}
		v := bd.Vendor
		if v.ID == 0 || v.Status != 1 || v.IsCoolingDown(now) {
			continue
		}
		w := bd.Weight
		if w <= 0 {
			w = 1 // 权重下限 1，等价于 new-api 全 0 权重退化为均匀随机
		}
		candidates = append(candidates, candidate{binding: bd, vendor: v, priority: bd.Priority, weight: w})
	}
	if len(candidates) == 0 {
		return nil, errors.New("所有供应商均不可用（禁用/熔断/已尝试）: " + modelName)
	}

	// 4) 优先级分层（降序），retry 映射到第几层
	uniq := uniquePrioritiesDesc(candidates)
	layer := retry
	if layer < 0 {
		layer = 0
	}
	if layer >= len(uniq) {
		layer = len(uniq) - 1 // 越界钳制到最低层
	}
	targetPriority := uniq[layer]

	layerCandidates := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.priority == targetPriority {
			layerCandidates = append(layerCandidates, c)
		}
	}
	if len(layerCandidates) == 0 {
		layerCandidates = candidates // 兜底：理论上不会发生
	}

	// 5) 同层加权随机
	selected := weightedSelect(layerCandidates)

	// 6) 解密供应商 API Key
	vendor := selected.vendor
	if vendor.APIKey != "" {
		decrypted, err := crypto.Decrypt(vendor.APIKey)
		if err != nil {
			logrus.Warnf("解密供应商 %d 的 API Key 失败: %v", vendor.ID, err)
			return nil, errors.New("供应商密钥解密失败，无法路由模型: " + modelName)
		}
		vendor.APIKey = decrypted
	}
	binding := selected.binding

	return &RouteResult{Model: m, Binding: &binding, Vendor: &vendor}, nil
}

// uniquePrioritiesDesc 提取候选中去重后的优先级，降序排列。
func uniquePrioritiesDesc(candidates []candidate) []int {
	seen := make(map[int]bool, len(candidates))
	uniq := make([]int, 0, len(candidates))
	for _, c := range candidates {
		if !seen[c.priority] {
			seen[c.priority] = true
			uniq = append(uniq, c.priority)
		}
	}
	// 简单插入排序（候选优先级数量通常很小），降序
	for i := 1; i < len(uniq); i++ {
		for j := i; j > 0 && uniq[j] > uniq[j-1]; j-- {
			uniq[j], uniq[j-1] = uniq[j-1], uniq[j]
		}
	}
	return uniq
}

// weightedSelect 加权随机选择（权重已 floor 到 >=1，比例精确）。
func weightedSelect(candidates []candidate) candidate {
	total := 0
	for _, c := range candidates {
		total += c.weight
	}
	if total <= 0 {
		return candidates[0]
	}
	r := rand.IntN(total)
	for _, c := range candidates {
		r -= c.weight
		if r < 0 {
			return c
		}
	}
	return candidates[0]
}

// ============ 供应商状态维护 ============

// MarkVendorError 记录供应商错误：累加连续错误，超阈值则进入熔断冷却。
//
// 两点刻意的设计：
//  1. 计数走 SQL 相对自增而非"读-改-写"。供应商抖动时多个请求会并发失败，
//     读改写会互相吞掉计数，熔断因此比配置的阈值更晚才触发。
//  2. 不持有 b.mu。那是路由缓存的锁，原来把它横跨两次 DB 写持有，等于一次失败
//     就把所有并发的 Route 读者堵住；而且原先无条件清空整个缓存，上游 brownout 时
//     每次失败都全量失效 -> 每个请求回源查 model+bindings+vendor -> 缓存被打穿成风暴。
//     现在只有"路由可用性真的变了"（进入冷却）才失效缓存。
func (b *Balancer) MarkVendorError(vendorID uint, errMsg string) {
	res := b.db.Model(&model.Vendor{}).Where("id = ?", vendorID).
		Updates(map[string]interface{}{
			"consecutive_errors": gorm.Expr("consecutive_errors + 1"),
			"last_error":         truncateStr(errMsg, 1024),
			"updated_at":         time.Now(),
		})
	if res.Error != nil {
		logrus.Warnf("累加供应商 %d 错误计数失败: %v", vendorID, res.Error)
		return
	}

	var v model.Vendor
	if err := b.db.Select("id", "consecutive_errors").First(&v, vendorID).Error; err != nil {
		return
	}
	if v.ConsecutiveErrors < cooldownErrorThreshold {
		return // 供应商仍然可路由，不动缓存
	}

	coolUntil := time.Now().Add(cooldownDuration)
	if err := b.db.Model(&model.Vendor{}).Where("id = ?", vendorID).
		Updates(map[string]interface{}{
			"cooldown_until":     coolUntil,
			"consecutive_errors": 0, // 重置计数，冷却到期后重新累计
			"updated_at":         time.Now(),
		}).Error; err != nil {
		logrus.Warnf("供应商 %d 进入熔断冷却失败: %v", vendorID, err)
		return
	}
	logrus.Warnf("供应商 %d 连续失败，进入熔断冷却至 %s: %s",
		vendorID, coolUntil.Format(time.DateTime), truncateStr(errMsg, 200))
	b.invalidateCache()
}

// MarkVendorOK 记录供应商成功：清零连续错误。
func (b *Balancer) MarkVendorOK(vendorID uint) {
	b.db.Model(&model.Vendor{}).Where("id = ? AND consecutive_errors <> 0", vendorID).
		Update("consecutive_errors", 0)
}

// MarkVendorUsed 更新供应商最后使用时间。
func (b *Balancer) MarkVendorUsed(vendorID uint) {
	b.db.Model(&model.Vendor{}).Where("id = ?", vendorID).Update("last_used_at", time.Now())
}

// RecoverVendor 清除熔断冷却与错误计数（健康探测成功后调用）。
func (b *Balancer) RecoverVendor(vendorID uint) {
	b.db.Model(&model.Vendor{}).Where("id = ?", vendorID).Updates(map[string]interface{}{
		"cooldown_until":     nil,
		"consecutive_errors": 0,
		"updated_at":         time.Now(),
	})
	logrus.Infof("供应商 %d 已恢复（清除熔断）", vendorID)
	b.invalidateCache()
}

// ExtendCooldown 延长熔断冷却（健康探测仍失败时调用），避免真实请求继续打到坏供应商。
func (b *Balancer) ExtendCooldown(vendorID uint, errMsg string) {
	coolUntil := time.Now().Add(cooldownDuration)
	b.db.Model(&model.Vendor{}).Where("id = ?", vendorID).Updates(map[string]interface{}{
		"cooldown_until": coolUntil,
		"last_error":     truncateStr(errMsg, 1024),
		"updated_at":     time.Now(),
	})
	b.invalidateCache()
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
