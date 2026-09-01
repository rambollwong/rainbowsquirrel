package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
)

// Cache is the cache plugin implementing rainbowsquirrel.Plugin; it serves
// Get/Select only. Cache 为缓存插件，实现 rainbowsquirrel.Plugin，仅服务 Get/Select。
type Cache struct {
	store  Store
	cfg    cacheConfig
	flight flight
}

type cacheConfig struct {
	defaultTTL        time.Duration
	invalidateOnWrite bool
	singleflight      bool
	keyPrefix         string
	codec             Codec
}

// Option is a cache plugin configuration item.
// Option 为 cache 插件配置项。
type Option func(*cacheConfig)

// WithDefaultTTL sets the default TTL; caching is disabled when both
// defaultTTL and call-level WithTTL are 0. WithDefaultTTL 设置默认 TTL；
// 未启用时（defaultTTL 与调用级 WithTTL 均为 0）缓存关闭。
func WithDefaultTTL(d time.Duration) Option {
	return func(c *cacheConfig) { c.defaultTTL = d }
}

// WithInvalidateOnWrite flushes after every successful Exec (fallback, safe and
// conservative). WithInvalidateOnWrite 设置任何 Exec 成功后 Flush（兜底，安全保守）。
func WithInvalidateOnWrite() Option {
	return func(c *cacheConfig) { c.invalidateOnWrite = true }
}

// WithSingleflight toggles penetration protection, default true.
// WithSingleflight 开关防击穿，默认 true。
func WithSingleflight(enabled bool) Option {
	return func(c *cacheConfig) { c.singleflight = enabled }
}

// WithKeyPrefix sets the global store-key prefix.
// WithKeyPrefix 设置 store key 全局前缀。
func WithKeyPrefix(prefix string) Option {
	return func(c *cacheConfig) { c.keyPrefix = prefix }
}

// WithCodec replaces the serialization codec.
// WithCodec 替换序列化 codec。
func WithCodec(c Codec) Option {
	return func(cfg *cacheConfig) { cfg.codec = c }
}

// New creates the cache plugin.
// New 创建缓存插件。
func New(store Store, opts ...Option) *Cache {
	cfg := cacheConfig{
		singleflight: true,
		codec:        binaryCodec{},
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &Cache{store: store, cfg: cfg}
}

func (c *Cache) Name() string { return "cache" }

// ===== Call-level options. 调用级选项。 =====

type ttlKey struct{}
type namespaceKey struct{}
type noCacheKey struct{}
type invalidateNSKey struct{}

// WithTTL sets the cache TTL for this call (also acts as the enable switch).
// WithTTL 为本次调用设置缓存 TTL（同时作为启用开关）。
func WithTTL(d time.Duration) rainbowsquirrel.CallOption {
	return rainbowsquirrel.NewCallOption(ttlKey{}, d)
}

// WithNamespace sets the cache namespace for this call.
// WithNamespace 设置本次调用的缓存域。
func WithNamespace(ns string) rainbowsquirrel.CallOption {
	return rainbowsquirrel.NewCallOption(namespaceKey{}, ns)
}

// WithNoCache skips the cache for this call (no hit, no write).
// WithNoCache 本次调用跳过缓存（不命中、不写入）。
func WithNoCache() rainbowsquirrel.CallOption {
	return rainbowsquirrel.NewCallOption(noCacheKey{}, true)
}

// WithInvalidateNamespace invalidates only that namespace after a successful Exec.
// WithInvalidateNamespace 在 Exec 成功后仅失效该域。
func WithInvalidateNamespace(ns string) rainbowsquirrel.CallOption {
	return rainbowsquirrel.NewCallOption(invalidateNSKey{}, ns)
}

type callOpts struct {
	ttl        time.Duration
	namespace  string
	noCache    bool
	invalidate string
}

func cacheOpts(info *rainbowsquirrel.ExecInfo) callOpts {
	var o callOpts
	for _, opt := range info.Options {
		k, v := rainbowsquirrel.CallOptionKeyValue(opt)
		switch k.(type) {
		case ttlKey:
			if d, ok := v.(time.Duration); ok {
				o.ttl = d
			}
		case namespaceKey:
			if s, ok := v.(string); ok {
				o.namespace = s
			}
		case noCacheKey:
			o.noCache = true
		case invalidateNSKey:
			if s, ok := v.(string); ok {
				o.invalidate = s
			}
		}
	}
	return o
}

// ===== Invalidation API. 失效 API。 =====

// CacheKey computes the cache logical key (without keyPrefix):
// namespace + "\x00" + sha256(...). It is based on the BindNamed output (before
// Rebind, dialect-independent) for debugging and precise invalidation.
// CacheKey 计算缓存逻辑 key（不含 keyPrefix）：namespace + "\x00" + sha256(...)。
// 基于 BindNamed 输出（Rebind 前，方言无关），供调试与精确失效。
func CacheKey(namespace, query string, arg any) (string, error) {
	bindSQL, args, err := rainbowsquirrel.BindNamed(query, arg)
	if err != nil {
		return "", err
	}
	return computeKey(namespace, bindSQL, args), nil
}

// InvalidateQuery performs precise deletion: it binds and computes the key
// internally, then deletes the single entry (recommended usage).
// InvalidateQuery 精确删除：内部 BindNamed + 算 key 后删单条（推荐用法）。
func (c *Cache) InvalidateQuery(ctx context.Context, namespace, query string, arg any) error {
	key, err := CacheKey(namespace, query, arg)
	if err != nil {
		return err
	}
	return c.store.Delete(ctx, c.storeKey(key))
}

// Invalidate is the low-level precise deletion: key is the logical key returned
// by CacheKey. Invalidate 底层精确删除：key 为 CacheKey 返回的逻辑 key。
func (c *Cache) Invalidate(ctx context.Context, key string) error {
	return c.store.Delete(ctx, c.storeKey(key))
}

// InvalidateNamespace deletes all cached entries of the namespace.
// InvalidateNamespace 删除该 namespace 全部缓存。
func (c *Cache) InvalidateNamespace(ctx context.Context, ns string) error {
	return c.store.DeletePrefix(ctx, c.cfg.keyPrefix+ns+"\x00")
}

// Flush clears all cached entries.
// Flush 清空全部缓存。
func (c *Cache) Flush(ctx context.Context) error {
	return c.store.Flush(ctx)
}

// ===== Plugin hooks. Plugin 钩子。 =====

type recCtxKey struct{}

func (c *Cache) Before(ctx context.Context, info *rainbowsquirrel.ExecInfo) (context.Context, error) {
	if info.InTx {
		return ctx, nil // Skip inside transactions: cache breaks isolation. 事务内自动跳过：缓存破坏隔离性。
	}
	if info.Op != rainbowsquirrel.OpGet && info.Op != rainbowsquirrel.OpSelect {
		return ctx, nil
	}
	o := cacheOpts(info)
	if o.noCache {
		return ctx, nil
	}
	ttl := o.ttl
	if ttl <= 0 {
		ttl = c.cfg.defaultTTL
	}
	if ttl <= 0 {
		return ctx, nil // Not enabled. 未启用。
	}
	key := computeKey(o.namespace, info.BindSQL, info.BindArgs)

	if c.cfg.singleflight {
		for {
			wait, leader := c.flight.acquire(key)
			if leader {
				if c.tryHit(ctx, key, info) {
					c.flight.done(key)
					return ctx, nil
				}
				break
			}
			wait()
			if c.tryHit(ctx, key, info) {
				return ctx, nil
			}
			// The leader failed without writing the cache; compete to become
			// the new leader. leader 失败未写缓存，重新竞争成为新的 leader。
		}
	} else if c.tryHit(ctx, key, info) {
		return ctx, nil
	}

	rec := &recorder{key: key, ttl: ttl}
	ctx = context.WithValue(ctx, recCtxKey{}, rec)
	ctx = rainbowsquirrel.WithRowWrapper(ctx, func(rs rainbowsquirrel.RowSource) rainbowsquirrel.RowSource {
		return &recordingRowSource{rows: rs, rec: rec}
	})
	return ctx, nil
}

func (c *Cache) After(ctx context.Context, info *rainbowsquirrel.ExecInfo) error {
	if info.Op == rainbowsquirrel.OpExec {
		if c.cfg.invalidateOnWrite {
			_ = c.store.Flush(ctx)
			return nil
		}
		if ns := cacheOpts(info).invalidate; ns != "" {
			_ = c.store.DeletePrefix(ctx, c.cfg.keyPrefix+ns+"\x00")
		}
		return nil
	}
	if info.InTx {
		return nil
	}
	if info.Op != rainbowsquirrel.OpGet && info.Op != rainbowsquirrel.OpSelect {
		return nil
	}
	rec, ok := ctx.Value(recCtxKey{}).(*recorder)
	if !ok {
		return nil // Hit or not this plugin's path. 命中或非本插件路径。
	}
	defer c.flight.done(rec.key)
	if info.Err != nil {
		return nil // Scan was not clean; drop the recording. 扫描不干净，丢弃录制。
	}
	data, err := c.cfg.codec.Encode(&RowData{Columns: rec.cols, Rows: rec.rows})
	if err != nil {
		return nil
	}
	_ = c.store.Set(ctx, c.storeKey(rec.key), data, rec.ttl)
	return nil
}

// ===== Internal. 内部。 =====

func computeKey(namespace, bindSQL string, bindArgs []any) string {
	payload, _ := json.Marshal(bindArgs)
	sum := sha256.Sum256([]byte(namespace + "\x00" + bindSQL + "\x00" + string(payload)))
	return namespace + "\x00" + hex.EncodeToString(sum[:])
}

func (c *Cache) storeKey(key string) string {
	return c.cfg.keyPrefix + key
}

func (c *Cache) tryHit(ctx context.Context, key string, info *rainbowsquirrel.ExecInfo) bool {
	data, ok, err := c.store.Get(ctx, c.storeKey(key))
	if err != nil || !ok {
		return false
	}
	rd, err := c.cfg.codec.Decode(data)
	if err != nil {
		return false
	}
	info.Rows = &cachedRowSource{data: rd}
	return true
}
