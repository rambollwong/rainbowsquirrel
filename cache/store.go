// Package cache is the RainbowSquirrel cache plugin, serving Get/Select only.
// Package cache 为 RainbowSquirrel 的缓存插件，仅服务 Get/Select。
package cache

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"
)

// Store is the pluggable cache backend.
// Store 为可插拔缓存后端。
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	// DeletePrefix deletes all keys starting with prefix (used for namespace
	// invalidation). DeletePrefix 删除所有以 prefix 开头的 key（用于域删除）。
	DeletePrefix(ctx context.Context, prefix string) error
	Flush(ctx context.Context) error
}

// MemoryStore is the built-in in-memory implementation: lazy expiration + LRU
// eviction. MemoryStore 为内置内存实现：惰性过期 + LRU 淘汰。
type MemoryStore struct {
	mu    sync.Mutex
	cap   int
	ttl   time.Duration
	items map[string]*list.Element
	lru   *list.List
}

type memEntry struct {
	key     string
	val     []byte
	expires time.Time // zero value means no expiration. 零值表示不过期。
}

// NewMemoryStore creates a memory store; capacity <= 0 means unlimited and
// defaultTTL <= 0 means no default expiration. NewMemoryStore 创建内存 Store；
// capacity <= 0 表示不限制，defaultTTL <= 0 表示默认不过期。
func NewMemoryStore(capacity int, defaultTTL time.Duration) *MemoryStore {
	return &MemoryStore{
		cap:   capacity,
		ttl:   defaultTTL,
		items: make(map[string]*list.Element),
		lru:   list.New(),
	}
}

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*memEntry)
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		s.remove(el)
		return nil, false, nil
	}
	s.lru.MoveToFront(el)
	return e.val, true, nil
}

func (s *MemoryStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	expires := time.Time{}
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	} else if s.ttl > 0 {
		expires = time.Now().Add(s.ttl)
	}
	if el, ok := s.items[key]; ok {
		e := el.Value.(*memEntry)
		e.val = value
		e.expires = expires
		s.lru.MoveToFront(el)
		return nil
	}
	if s.cap > 0 && s.lru.Len() >= s.cap {
		s.evict()
	}
	e := &memEntry{key: key, val: value, expires: expires}
	s.items[key] = s.lru.PushFront(e)
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		s.remove(el)
	}
	return nil
}

func (s *MemoryStore) DeletePrefix(ctx context.Context, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, el := range s.items {
		if strings.HasPrefix(key, prefix) {
			s.remove(el)
		}
	}
	return nil
}

func (s *MemoryStore) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*list.Element)
	s.lru.Init()
	return nil
}

func (s *MemoryStore) remove(el *list.Element) {
	s.lru.Remove(el)
	delete(s.items, el.Value.(*memEntry).key)
}

func (s *MemoryStore) evict() {
	back := s.lru.Back()
	if back != nil {
		s.remove(back)
	}
}
