// Package redis provides a Redis-backed cache.Store for rainbowsquirrel/cache.
// It is an optional sub-package; the core framework keeps zero third-party
// runtime dependencies. Package redis 为 rainbowsquirrel/cache 提供 Redis 后端
// Store 实现。这是可选子包，核心框架仍保持零第三方运行时依赖。
package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Store implements cache.Store on top of a go-redis client.
// Store 基于 go-redis 客户端实现 cache.Store。
type Store struct {
	client *goredis.Client
	prefix string
}

// Option configures the Redis store.
// Option 为 Redis store 配置项。
type Option func(*Store)

// WithKeyPrefix sets a store-level key prefix. When set, Flush only deletes
// keys under this prefix instead of flushing the whole DB.
// WithKeyPrefix 设置 store 级 key 前缀。设置后 Flush 只删除该前缀下的 key，
// 而不是清空整个 DB。
func WithKeyPrefix(prefix string) Option {
	return func(s *Store) { s.prefix = prefix }
}

// NewStore creates a Redis-backed Store. It is safe for concurrent use.
// NewStore 创建 Redis 后端 Store，并发安全。
func NewStore(client *goredis.Client, opts ...Option) *Store {
	s := &Store{client: client}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get returns the value and whether the key exists; a Redis nil counts as a
// miss without error. Get 返回值与是否存在；Redis nil 视为未命中且不报错。
func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := s.client.Get(ctx, s.key(key)).Bytes()
	if err == goredis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Set writes the value with the given TTL (ttl <= 0 means no expiration).
// Set 写入值并设置 TTL（ttl <= 0 表示不过期）。
func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, s.key(key), value, ttl).Err()
}

// Delete removes a single key.
// Delete 删除单个 key。
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.key(key)).Err()
}

// DeletePrefix removes all keys starting with the store key prefix + prefix.
// DeletePrefix 删除 store key 前缀 + prefix 开头的全部 key。
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	return s.scanDel(ctx, s.prefix+prefix+"*")
}

// Flush clears the store. With a configured key prefix only matching keys are
// deleted; without a prefix the whole Redis DB is flushed (use a dedicated DB
// for caching). Flush 清空 store。配置了 key 前缀时只删除匹配 key；未配置
// 前缀时清空整个 Redis DB（建议为缓存使用独立 DB）。
func (s *Store) Flush(ctx context.Context) error {
	if s.prefix != "" {
		return s.scanDel(ctx, s.prefix+"*")
	}
	return s.client.FlushDB(ctx).Err()
}

// scanDel iterates keys matching pattern via SCAN and deletes them in batches.
// scanDel 通过 SCAN 迭代匹配 pattern 的 key 并批量删除。
func (s *Store) scanDel(ctx context.Context, pattern string) error {
	var cursor uint64
	for {
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := s.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func (s *Store) key(k string) string { return s.prefix + k }
