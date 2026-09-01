package cache

import "sync"

// flight is a lightweight singleflight implementation for cache-penetration
// protection. flight 为防缓存击穿的轻量 singleflight 实现。
type flight struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
}

func (f *flight) init() {
	if f.calls == nil {
		f.calls = make(map[string]*flightCall)
	}
}

// acquire tries to become the leader for key; non-leaders get a wait function.
// acquire 尝试成为 key 的 leader；非 leader 返回等待函数。
func (f *flight) acquire(key string) (wait func(), leader bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if c, ok := f.calls[key]; ok {
		done := c.done
		return func() { <-done }, false
	}
	f.calls[key] = &flightCall{done: make(chan struct{})}
	return func() {}, true
}

// done ends the flight for key and wakes waiters.
// done 结束 key 的飞行，唤醒等待者。
func (f *flight) done(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.calls[key]; ok {
		close(c.done)
		delete(f.calls, key)
	}
}
