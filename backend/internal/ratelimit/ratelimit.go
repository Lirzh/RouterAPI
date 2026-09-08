package ratelimit

import (
	"sync"
	"time"
)

// Limiter 内存固定窗口限流器（每分钟计数）。
// 单实例部署足够；多副本场景需换成 Redis（本项目范围外）。
// 设计原则 fail-open：limit<=0 视为不限流，直接放行。
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	nowFunc func() time.Time
}

type window struct {
	count   int
	resetAt time.Time
}

func New() *Limiter {
	return &Limiter{windows: make(map[string]*window), nowFunc: time.Now}
}

// Allow 判定 key 在当前分钟窗口内是否放行；limitPerMin<=0 表示不限流。
func (l *Limiter) Allow(key string, limitPerMin int) bool {
	if limitPerMin <= 0 {
		return true // fail-open：未配置即不限流
	}
	now := l.nowFunc()

	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.windows[key]
	if !ok || now.After(w.resetAt) {
		l.windows[key] = &window{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	if w.count >= limitPerMin {
		return false
	}
	w.count++
	return true
}

// RetryAfter 返回当前窗口剩余秒数（用于 429 的 Retry-After 头），至少 1 秒。
func (l *Limiter) RetryAfter(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.windows[key]; ok {
		sec := int(time.Until(w.resetAt).Seconds())
		if sec < 1 {
			sec = 1
		}
		return sec
	}
	return 1
}

// cleanup 定期清理过期窗口，防止 map 无限增长（由 Start 后台调用）。
func (l *Limiter) cleanup() {
	now := l.nowFunc()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, w := range l.windows {
		if now.After(w.resetAt) {
			delete(l.windows, k)
		}
	}
}

// StartCleanup 启动后台清理协程；stop 关闭时退出。
func (l *Limiter) StartCleanup(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				l.cleanup()
			case <-stop:
				return
			}
		}
	}()
}
