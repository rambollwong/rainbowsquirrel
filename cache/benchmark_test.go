package cache_test

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
	"github.com/rambollwong/rainbowsquirrel/cache"
	_ "modernc.org/sqlite"
)

type benchUser struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

// newBenchSQLite opens an in-memory sqlite DB with a single row.
// newBenchSQLite 打开内存 sqlite 库并预置一行数据。
func newBenchSQLite(b *testing.B) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, name) VALUES (1, 'alice')`); err != nil {
		b.Fatal(err)
	}
	return db
}

const benchQuery = "SELECT id, name FROM users WHERE id = :id"

var benchArg = map[string]any{"id": 1}

// BenchmarkHandwrittenGetSQLite is the real baseline: hand-written QueryRow +
// Scan against sqlite, for a fair end-to-end comparison with BenchmarkGetSQLite.
// BenchmarkHandwrittenGetSQLite 为真实基线：手写 QueryRow + Scan 查询 sqlite，
// 用于与 BenchmarkGetSQLite 公平端到端对照。
func BenchmarkHandwrittenGetSQLite(b *testing.B) {
	ctx := context.Background()
	db := newBenchSQLite(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var u benchUser
		if err := db.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", 1).Scan(&u.ID, &u.Name); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetSQLite measures a real Get through the framework without cache.
// BenchmarkGetSQLite 测量无缓存时经框架的真实 Get。
func BenchmarkGetSQLite(b *testing.B) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newBenchSQLite(b))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.Get[benchUser](ctx, benchQuery, benchArg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetCacheHit measures a Get served by the cache plugin (zero DB call).
// BenchmarkGetCacheHit 测量缓存插件命中的 Get（零 DB 调用）。
func BenchmarkGetCacheHit(b *testing.B) {
	ctx := context.Background()
	c := cache.New(cache.NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(newBenchSQLite(b), rainbowsquirrel.WithPlugin(c))
	opts := cache.WithTTL(time.Minute)
	// Warm the cache. 预热缓存。
	if _, err := d.Get[benchUser](ctx, benchQuery, benchArg, opts); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.Get[benchUser](ctx, benchQuery, benchArg, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetCacheMiss measures a Get that misses the cache and records rows.
// A varying namespace forces a real query + recording on every iteration.
// BenchmarkGetCacheMiss 测量缓存未命中并录制的 Get。变化的 namespace 强制
// 每次迭代都真实查询并录制。
func BenchmarkGetCacheMiss(b *testing.B) {
	ctx := context.Background()
	c := cache.New(cache.NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(newBenchSQLite(b), rainbowsquirrel.WithPlugin(c))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ns := "ns" + strconv.Itoa(i)
		if _, err := d.Get[benchUser](ctx, benchQuery, benchArg,
			cache.WithTTL(time.Minute), cache.WithNamespace(ns)); err != nil {
			b.Fatal(err)
		}
	}
}
