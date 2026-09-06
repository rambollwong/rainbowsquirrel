# RainbowSquirrel

English | [中文](README.zh_cn.md)

A lightweight Go SQL mapping layer between `database/sql` and business code. It solves exactly two problems: **parameter binding** (Go values → SQL parameters) and **result mapping** (query results → Go values). It never generates or rewrites SQL.

- **Zero third-party runtime dependencies**: only `database/sql`, `reflect`, `sync`, `log/slog`
- **Go 1.27+** (method generics)
- **MIT License**

## Features

- Named placeholders `:name` → `?` / `$n` / `@n` (`Rebind` dialect rewriting, skipping `::cast`, strings, and comments)
- Generic `Get[T]` / `Select[T]`: structs (with embedded flattening), maps, scalars, and pointer type parameters
- `BindNamedMany` batch binding with single-group VALUES repetition
- `IN` expansion (`WithSliceExpansion`; empty slices return `ErrSliceExpansion`)
- `db` tags: `db:"col"`, `db:"-"`, `db:"col,json"`, `db:"col,omitempty"` (zero values bind as NULL)
- Configurable NULL semantics (zero value by default / `ErrNullNotAllowed`), time zone/layout, strict mode
- Plugin system: `Before` (gate — an error aborts) + `After` (bypass — errors/panics do not block)
- Cache plugin (`rainbowsquirrel/cache`): serves `Get`/`Select` only; in-memory LRU, binary codec, singleflight, namespace invalidation; `rainbowsquirrel/cache/redis` provides a Redis backend
- Log plugin (`rainbowsquirrel/log`): slog integration, slow-query Warn, SQL compacted to a single line for display; `rainbowsquirrel/log/rainbowlog` provides a rainbowlog structured-logging backend
- Custom converters via `RegisterConverter[T]`

## Installation

```bash
go get github.com/rambollwong/rainbowsquirrel@latest
```

## Quick Start

```go
package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
	"github.com/rambollwong/rainbowsquirrel/cache"
	"github.com/rambollwong/rainbowsquirrel/log"
	_ "modernc.org/sqlite" // any database/sql driver
)

type User struct {
	ID        int64          `db:"id"`
	Name      string         `db:"name"`
	Age       *int           `db:"age"`
	Profile   map[string]any `db:"profile,json"`
	CreatedAt time.Time      `db:"created_at"`
}

func main() {
	ctx := context.Background()
	raw, _ := sql.Open("sqlite", ":memory:")

	cachePlugin := cache.New(cache.NewMemoryStore(1024, 0))
	logPlugin := log.New(nil, log.WithSlowQueryThreshold(200*time.Millisecond))

	db := rainbowsquirrel.New(raw,
		rainbowsquirrel.WithStrictMode(true),
		rainbowsquirrel.WithSliceExpansion(true),
		rainbowsquirrel.WithPlugin(cachePlugin, logPlugin),
	)

	// Write: named-placeholder binding with struct/map
	_, _ = db.Exec(ctx,
		`INSERT INTO users (name, age, profile) VALUES (:name, :age, :profile)`,
		map[string]any{"name": "alice", "age": 30, "profile": `{"k":1}`},
	)

	// Single-row query: 30s TTL, namespace-scoped cache
	u, err := db.Get[User](ctx,
		`SELECT id, name, age, profile, created_at FROM users WHERE id = :id`,
		map[string]any{"id": 1},
		cache.WithTTL(30*time.Second), cache.WithNamespace("users"),
	)
	_, _ = u, err

	// Multi-row query
	users, _ := db.Select[User](ctx, `SELECT * FROM users ORDER BY id`, nil)
	_ = users

	// Scalar
	count, _ := db.Get[int64](ctx, `SELECT COUNT(*) FROM users`, nil)
	_ = count

	// Batch write: single-group VALUES repetition
	_, _ = db.Exec(ctx,
		`INSERT INTO users (name) VALUES (:name)`,
		[]map[string]any{{"name": "a"}, {"name": "b"}, {"name": "c"}},
	)

	// Precise invalidation, then query again
	_ = cachePlugin.InvalidateQuery(ctx, "users",
		`SELECT id FROM users WHERE id = :id`, map[string]any{"id": 1})

	// Transactions: Tx shares the plugin chain; cache auto-skips inside Tx
	tx, _ := db.BeginTx(ctx, nil)
	_, _ = tx.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "tx"})
	_ = tx.Commit()

	// Nested transaction: SAVEPOINT-based; a sub-rollback does not affect the outer tx
	tx2, _ := db.BeginTx(ctx, nil)
	_, _ = tx2.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "outer"})
	sub, _ := tx2.Begin(ctx)
	_, _ = sub.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "inner"})
	_ = sub.Rollback() // rolls back only "inner"; "outer" stays in the outer tx
	_ = tx2.Commit()

	// Underlying escape hatch
	_ = db.RawDB()
}
```

## Redis Backend (Optional)

`rainbowsquirrel/cache/redis` implements `cache.Store` on top of `go-redis/v9`:

```bash
go get github.com/redis/go-redis/v9
```

```go
import (
	goredis "github.com/redis/go-redis/v9"
	"github.com/rambollwong/rainbowsquirrel/cache"
	rediscache "github.com/rambollwong/rainbowsquirrel/cache/redis"
)

client := goredis.NewClient(&goredis.Options{Addr: "localhost:6379"})
store := rediscache.NewStore(client, rediscache.WithKeyPrefix("rs:"))
c := cache.New(store, cache.WithDefaultTTL(5*time.Minute))
db := rainbowsquirrel.New(rawDB, rainbowsquirrel.WithPlugin(c))
```

Configure `WithKeyPrefix` when possible: `Flush` then only deletes keys under that prefix; without a prefix `Flush` runs `FLUSHDB` (clears the whole DB — use a dedicated DB for caching).

## API Overview

```go
// Entry point
db := rainbowsquirrel.New(rawDB, opts...)

// DB / Tx (same signatures)
db.Exec(ctx, query, arg, opts...)           // (sql.Result, error)
db.Query(ctx, query, arg, opts...)          // (*rainbowsquirrel.Rows, error)
db.Get[T](ctx, query, arg, opts...)         // (T, error)
db.Select[T](ctx, query, arg, opts...)      // ([]T, error)
db.BeginTx(ctx, opts)                       // (*Tx, error)
db.Use(plugin...)                           // register plugins before first execution

// Pure functions
rainbowsquirrel.BindNamed(query, arg)       // (string, []any, error)
rainbowsquirrel.BindNamedMany(query, args)  // (string, []any, error)
rainbowsquirrel.Rebind(query, style)        // string
rainbowsquirrel.Scan(rs, dest)              // error (consumes the whole RowSource)
rainbowsquirrel.ConvertAssign(dest, val)    // error
rainbowsquirrel.RegisterConverter[T](fn)

// Placeholder styles
rainbowsquirrel.PlaceholderQuestion / PlaceholderDollar / PlaceholderAt

// cache plugin
cache.New(store, opts...)                   // implements rainbowsquirrel.Plugin
cache.NewMemoryStore(capacity, defaultTTL)
cache.WithTTL(d) / WithNamespace(ns) / WithNoCache() / WithInvalidateNamespace(ns)
c.InvalidateQuery(ctx, ns, query, arg) / Invalidate(ctx, key) / InvalidateNamespace(ctx, ns) / Flush(ctx)

// log plugin
log.New(logger, opts...)                    // implements rainbowsquirrel.Plugin
log.WithLevel(l) / WithSlowQueryThreshold(d) / WithQuery(b) / WithArgs(b)

// rainbowlog log plugin (optional, backed by github.com/rambollwong/rainbowlog)
rlogplugin "github.com/rambollwong/rainbowsquirrel/log/rainbowlog"
rlogplugin.New(logger, opts...)             // implements rainbowsquirrel.Plugin
```

Arg dispatch (`arg any`): single struct/map → `BindNamed`; `[]struct`/`[]map` → `BindNamedMany`; `[]any`/basic value → positional passthrough.

## Configuration

| Option                                | Default                      | Description                                    |
| ------------------------------------- | ---------------------------- | ---------------------------------------------- |
| `WithTagName`                         | `db`                         | struct tag name                                |
| `WithNameMapper`                      | snake_case                   | field name → column name                       |
| `WithPlaceholder`                     | `Question`; PostgreSQL drivers auto `Dollar` | placeholder dialect, explicit setting wins |
| `WithStrictMode`                      | `false`                      | unknown columns return `ErrColumnNotFound`     |
| `WithSliceExpansion`                  | `false`                      | expand `IN (:ids)`                             |
| `WithNullToZeroValue`                 | `true`                       | NULL → zero value                              |
| `WithTimeLocation` / `WithTimeLayout` | driver default / RFC3339Nano | time parsing and JSON marshaling base          |
| `WithMapKeyFunc`                      | identity                     | map-result key handling                        |
| `WithPlugin` / `Use`                  | none                         | plugin registration (late registration errors) |
| `WithPluginErrorHandler`              | none                         | After error/panic reporting callback           |

Call-level options: `WithStrictModeCall(bool)`; sub-packages extend via `NewCallOption(key, value)` + `CallOptionKeyValue(o)`.

## Testing & Benchmarks

```bash
make check        # build + vet + test (CI entry)
make test-race    # full -race
make bench        # benchmarks
make pg-test      # PostgreSQL integration tests (Docker, one-command loop)
make pg-up / pg-down
```

Benchmark highlights (Apple M3, in-memory sqlite): framework `Get` is about 20% slower than hand-written `QueryRow+Scan` (within the design target of ≤1.5x); the cache-hit path is ~1.2μs, over 2x faster than a real hand-written query.

## Design Document

See [`doc/design.md`](doc/design.md) for the full design, ADR decision records, and error codes.

## License

MIT
