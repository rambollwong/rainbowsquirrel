package rainbowsquirrel

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Op identifies the operation type of an execution.
// Op 标识本次操作类型。
type Op int

const (
	OpExec Op = iota
	OpQuery
	OpGet
	OpSelect
)

func (o Op) String() string {
	switch o {
	case OpExec:
		return "Exec"
	case OpQuery:
		return "Query"
	case OpGet:
		return "Get"
	case OpSelect:
		return "Select"
	default:
		return fmt.Sprintf("Op(%d)", int(o))
	}
}

// RowSource is the data-source abstraction of the scan layer; *sql.Rows
// satisfies it natively. RowSource 为扫描层数据源抽象；*sql.Rows 原生满足。
type RowSource interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// RowRecorder is implemented by RowSource types that need to record raw cell
// values (cache-miss path). When the core scan layer detects this interface, it
// fetches raw driver values per row, calls RecordRow, and then converts them
// back into dest. RowRecorder 由需要录制原始单元格值的 RowSource 实现
// （cache 未命中路径）。核心扫描层检测到该接口后，每行先取驱动原始值并回调
// RecordRow，再转换写回 dest。
type RowRecorder interface {
	RecordRow(values []any)
}

// ValueRowSource is implemented by RowSource types that expose raw row values
// directly (cache-hit replay). The core wraps it into an internal RowSource so
// replay conversion uses the DB-level config. ValueRowSource 由直接暴露原始
// 行值的 RowSource（缓存命中重放）实现。核心将其包装为内部 RowSource，
// 使重放转换使用 DB 级配置。
type ValueRowSource interface {
	RowValues() (columns []string, rows [][]any)
}

// RowWrapper wraps a RowSource after the real query (cache injects a recording
// wrapper on cache miss). RowWrapper 在真实查询之后包装 RowSource
// （cache 未命中时注入录制包装）。
type RowWrapper func(rs RowSource) RowSource

type ctxKey int

const rowWrapperKey ctxKey = iota

// WithRowWrapper attaches a rows wrapper to ctx: plugins inject it during
// Before and the core consumes it after the query (real rows only exist after
// Before). WithRowWrapper 将 rows 包装器挂到 ctx 上，供插件在 Before 阶段注入、
// 核心在查询后取用（真实 rows 在 Before 之后才存在）。
func WithRowWrapper(ctx context.Context, w RowWrapper) context.Context {
	return context.WithValue(ctx, rowWrapperKey, w)
}

func rowWrapperFrom(ctx context.Context) RowWrapper {
	w, _ := ctx.Value(rowWrapperKey).(RowWrapper)
	return w
}

// Plugin is the cross-cutting concern interface.
// Plugin 为横切关注点接口。
type Plugin interface {
	Name() string
	Before(ctx context.Context, info *ExecInfo) (context.Context, error)
	After(ctx context.Context, info *ExecInfo) error
}

// ExecInfo is the per-execution context shared between Before/After.
// ExecInfo 为一次执行的上下文信息，Before/After 共享。
type ExecInfo struct {
	Op        Op
	Query     string
	Arg       any
	BoundSQL  string // final SQL after Rebind. Rebind 后的最终 SQL。
	BindSQL   string // BindNamed output (before Rebind, CacheKey scope). BindNamed 输出（Rebind 前，CacheKey 口径）。
	BindArgs  []any  // BindNamed output args (before slice expansion, CacheKey scope). BindNamed 输出参数（slice 展开前，CacheKey 口径）。
	BoundArgs []any  // final args. 最终参数。
	Options   []CallOption
	InTx      bool
	Start     time.Time
	Duration  time.Duration
	Rows      RowSource // injectable in Before (short-circuit). Before 可注入（短路）。
	Result    sql.Result
	Err       error
}

// Rows wraps *sql.Rows: Close() triggers the deferred After, and Duration is
// measured until Close. Rows 包装 *sql.Rows：Close() 时触发延迟 After，
// Duration 统计到 Close。
type Rows struct {
	*sql.Rows
	once  sync.Once
	after func()
}

// Close closes the underlying rows and fires the deferred After exactly once.
// Close 关闭底层 rows 并恰好触发一次延迟 After。
func (r *Rows) Close() error {
	err := r.Rows.Close()
	r.once.Do(r.after)
	return err
}
