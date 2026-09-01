# RainbowSquirrel

[English](README.md) | 中文

轻量 Go SQL 映射层，位于 `database/sql` 与业务代码之间。只解决两类问题：**参数绑定**（Go 值 → SQL 参数）与**结果映射**（查询结果 → Go 值）。不生成、不修改 SQL。

- **零第三方运行时依赖**：仅 `database/sql`、`reflect`、`sync`、`log/slog`
- **Go 1.27+**（方法泛型）
- **MIT License**

## 特性

- 命名占位符 `:name` → `?` / `$n` / `@n`（`Rebind` 方言重绑定，跳过 `::cast`、字符串与注释）
- 泛型 `Get[T]` / `Select[T]`：struct（含内嵌展平）、map、标量、指针类型参数
- `BindNamedMany` 单组 VALUES 批量绑定
- `IN` 展开（`WithSliceExpansion`，空切片报 `ErrSliceExpansion`）
- `db` tag：`db:"col"`、`db:"-"`、`db:"col,json"`、`db:"col,omitempty"`（空值绑定为 NULL）
- NULL 语义可配（默认零值 / `ErrNullNotAllowed`）、时区/布局可配、strict 模式
- 插件系统：`Before`（门，出错中止）+ `After`（旁路，错误/panic 不阻断）
- 缓存插件（`rainbowsquirrel/cache`）：仅服务 `Get`/`Select`，内存 LRU、二进制 codec、singleflight 防击穿、域失效；`rainbowsquirrel/cache/redis` 提供 Redis 后端
- 日志插件（`rainbowsquirrel/log`）：slog 对接，慢查询 Warn；`rainbowsquirrel/log/rainbowlog` 提供 rainbowlog 结构化日志后端
- 自定义转换器 `RegisterConverter[T]`

## 安装

```bash
go get github.com/rambollwong/rainbowsquirrel@latest
```

## 快速开始

```go
package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
	"github.com/rambollwong/rainbowsquirrel/cache"
	"github.com/rambollwong/rainbowsquirrel/log"
	_ "modernc.org/sqlite" // 任意 database/sql 驱动
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

	// 写入：命名占位符绑定 struct/map
	_, _ = db.Exec(ctx,
		`INSERT INTO users (name, age, profile) VALUES (:name, :age, :profile)`,
		map[string]any{"name": "alice", "age": 30, "profile": `{"k":1}`},
	)

	// 单行查询：缓存 TTL 30s，namespace 域
	u, err := db.Get[User](ctx,
		`SELECT id, name, age, profile, created_at FROM users WHERE id = :id`,
		map[string]any{"id": 1},
		cache.WithTTL(30*time.Second), cache.WithNamespace("users"),
	)
	_, _ = u, err

	// 多行查询
	users, _ := db.Select[User](ctx, `SELECT * FROM users ORDER BY id`, nil)
	_ = users

	// 标量
	count, _ := db.Get[int64](ctx, `SELECT COUNT(*) FROM users`, nil)
	_ = count

	// 批量写入：单组 VALUES 重复
	_, _ = db.Exec(ctx,
		`INSERT INTO users (name) VALUES (:name)`,
		[]map[string]any{{"name": "a"}, {"name": "b"}, {"name": "c"}},
	)

	// 精确失效后重新查询
	_ = cachePlugin.InvalidateQuery(ctx, "users",
		`SELECT id FROM users WHERE id = :id`, map[string]any{"id": 1})

	// 事务：Tx 与 DB 共享插件链；事务内缓存自动跳过
	tx, _ := db.BeginTx(ctx, nil)
	_, _ = tx.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "tx"})
	_ = tx.Commit()

	// 嵌套事务：基于 SAVEPOINT，子回滚不影响外层
	tx2, _ := db.BeginTx(ctx, nil)
	_, _ = tx2.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "outer"})
	sub, _ := tx2.Begin(ctx)
	_, _ = sub.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "inner"})
	_ = sub.Rollback() // 仅回滚 inner；outer 仍在外层事务中
	_ = tx2.Commit()

	// 底层逃生舱
	_ = db.RawDB()
}
```

## Redis 后端（可选）

`rainbowsquirrel/cache/redis` 基于 `go-redis/v9` 实现 `cache.Store`：

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

建议配置 `WithKeyPrefix`：`Flush` 只删除该前缀下的 key；未配置时 `Flush` 执行 `FLUSHDB`（清空整个 DB，缓存请使用专用 DB）。

## API 速查

```go
// 入口
db := rainbowsquirrel.New(rawDB, opts...)

// DB / Tx（同签名）
db.Exec(ctx, query, arg, opts...)           // (sql.Result, error)
db.Query(ctx, query, arg, opts...)          // (*rainbowsquirrel.Rows, error)
db.Get[T](ctx, query, arg, opts...)         // (T, error)
db.Select[T](ctx, query, arg, opts...)      // ([]T, error)
db.BeginTx(ctx, opts)                       // (*Tx, error)
db.Use(plugin...)                           // 首次执行前注册插件

// 纯函数
rainbowsquirrel.BindNamed(query, arg)       // (string, []any, error)
rainbowsquirrel.BindNamedMany(query, args)  // (string, []any, error)
rainbowsquirrel.Rebind(query, style)        // string
rainbowsquirrel.Scan(rs, dest)              // error（消费整个 RowSource）
rainbowsquirrel.ConvertAssign(dest, val)    // error
rainbowsquirrel.RegisterConverter[T](fn)

// 占位符风格
rainbowsquirrel.PlaceholderQuestion / PlaceholderDollar / PlaceholderAt

// cache 插件
cache.New(store, opts...)                   // 实现 rainbowsquirrel.Plugin
cache.NewMemoryStore(capacity, defaultTTL)
cache.WithTTL(d) / WithNamespace(ns) / WithNoCache() / WithInvalidateNamespace(ns)
c.InvalidateQuery(ctx, ns, query, arg) / Invalidate(ctx, key) / InvalidateNamespace(ctx, ns) / Flush(ctx)

// log 插件
log.New(logger, opts...)                    // 实现 rainbowsquirrel.Plugin
log.WithLevel(l) / WithSlowQueryThreshold(d) / WithQuery(b) / WithArgs(b)

// rainbowlog 日志插件（可选，基于 github.com/rambollwong/rainbowlog）
rlogplugin "github.com/rambollwong/rainbowsquirrel/log/rainbowlog"
rlogplugin.New(logger, opts...)             // 实现 rainbowsquirrel.Plugin
```

参数分派（`arg any`）：单个 struct/map → `BindNamed`；`[]struct`/`[]map` → `BindNamedMany`；`[]any`/基础值 → 位置参数透传。

## 配置

| Option | 默认 | 说明 |
|---|---|---|
| `WithTagName` | `db` | struct tag 名 |
| `WithNameMapper` | snake_case | 字段名 → 列名 |
| `WithPlaceholder` | `Question` | 占位符方言 |
| `WithStrictMode` | `false` | 未知列报 `ErrColumnNotFound` |
| `WithSliceExpansion` | `false` | `IN (:ids)` 展开 |
| `WithNullToZeroValue` | `true` | NULL → 零值 |
| `WithTimeLocation` / `WithTimeLayout` | 驱动默认 / RFC3339Nano | 时间解析与 JSON 序列化基准 |
| `WithMapKeyFunc` | identity | map 结果 key 处理 |
| `WithPlugin` / `Use` | 无 | 插件注册（首次执行后注册报错） |
| `WithPluginErrorHandler` | 无 | After 错误/panic 上报回调 |

调用级选项：`WithStrictModeCall(bool)`；子包扩展用 `NewCallOption(key, value)` + `CallOptionKeyValue(o)`。

## 测试与基准

```bash
make check        # build + vet + test（CI 入口）
make test-race    # 全量 -race
make bench        # 基准测试
make pg-test      # PostgreSQL 集成测试（Docker，一条命令闭环）
make pg-up / pg-down
```

基准报告要点（Apple M3，sqlite 内存库）：框架 `Get` 比手写 `QueryRow+Scan` 慢约 20%（满足设计目标 ≤1.5x）；缓存命中路径约 1.2μs，比手写真实查询快 2 倍以上。

## 设计文档

完整设计、决策记录（ADR）与错误码表见 [`doc/design.md`](doc/design.md)。

## License

MIT
