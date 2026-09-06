package rainbowsquirrel

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
)

// dbExecutor abstracts the common query capability of *sql.DB and *sql.Tx.
// dbExecutor 抽象 *sql.DB 与 *sql.Tx 的公共查询能力。
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DB is the framework entry, combining *sql.DB, config, and the plugin chain.
// DB 为框架入口，组合 *sql.DB、配置与插件链。
type DB struct {
	db  *sql.DB
	cfg *config
}

// New creates a DB instance. PostgreSQL drivers (pgx / lib/pq) are detected
// via the registered driver's package path, so the default placeholder becomes
// Dollar automatically; an explicit WithPlaceholder always wins.
// New 创建 DB 实例。经注册驱动的包路径识别 PostgreSQL 驱动（pgx / lib/pq），
// 自动将默认占位符设为 Dollar；显式 WithPlaceholder 始终优先。
func New(db *sql.DB, opts ...Option) *DB {
	cfg := defaultConfig()
	if isPostgresDriver(db.Driver()) {
		cfg.placeholder = PlaceholderDollar
	}
	for _, o := range opts {
		o(cfg)
	}
	return &DB{db: db, cfg: cfg}
}

// isPostgresDriver reports whether the registered driver belongs to a
// PostgreSQL driver family (pgx / lib/pq) by inspecting the package path of
// the concrete driver type. It is best-effort: an explicit WithPlaceholder
// always overrides the detected default.
// isPostgresDriver 通过具体驱动类型的包路径判断是否属于 PostgreSQL 驱动家族
// （pgx / lib/pq）。这是尽力而为的检测：显式 WithPlaceholder 始终覆盖检测默认值。
func isPostgresDriver(d driver.Driver) bool {
	if d == nil {
		return false
	}
	var v any = d
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	pkg := t.PkgPath()
	return strings.Contains(pkg, "jackc/pgx") || strings.HasSuffix(pkg, "/pq")
}

// Use appends plugins; registering after first execution returns
// ErrPluginRegisteredTooLate. Use 追加插件；首次执行后注册返回
// ErrPluginRegisteredTooLate。
func (d *DB) Use(p ...Plugin) error {
	return d.cfg.addPlugins(p...)
}

// RawDB returns the underlying *sql.DB (escape hatch).
// RawDB 返回底层 *sql.DB（逃生舱）。
func (d *DB) RawDB() *sql.DB { return d.db }

// BeginTx starts a transaction; Tx shares the same config and plugin chain as DB.
// BeginTx 开启事务，Tx 与 DB 共享同一配置与插件链。
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx, cfg: d.cfg}, nil
}

// Exec executes a write operation.
// Exec 执行写操作。
func (d *DB) Exec(ctx context.Context, query string, arg any, opts ...CallOption) (sql.Result, error) {
	return execOp(ctx, d.db, d.cfg, false, query, arg, opts)
}

// Query returns live rows; After is deferred until Rows.Close().
// Query 返回实时 rows，After 延迟到 Rows.Close()。
func (d *DB) Query(ctx context.Context, query string, arg any, opts ...CallOption) (*Rows, error) {
	return queryOp(ctx, d.db, d.cfg, false, query, arg, opts)
}

// Get queries a single row and maps it.
// Get 单行查询并映射。
func (d *DB) Get[T any](ctx context.Context, query string, arg any, opts ...CallOption) (T, error) {
	return getOp[T](ctx, d.db, d.cfg, false, query, arg, opts)
}

// Select queries multiple rows and maps them.
// Select 多行查询并映射。
func (d *DB) Select[T any](ctx context.Context, query string, arg any, opts ...CallOption) ([]T, error) {
	return selectOp[T](ctx, d.db, d.cfg, false, query, arg, opts)
}

// Tx is the transaction wrapper, sharing config and plugin chain with DB.
// A nested Tx (created via Begin) is backed by a SAVEPOINT on the same
// underlying *sql.Tx. Tx 为事务封装，与 DB 共享配置与插件链。嵌套 Tx
// （经 Begin 创建）基于同一底层 *sql.Tx 上的 SAVEPOINT。
type Tx struct {
	tx        *sql.Tx
	cfg       *config
	savepoint string      // non-empty for a nested savepoint Tx. 非空表示嵌套保存点事务。
	closed    atomic.Bool // finished via Commit/Rollback. 已 Commit/Rollback 结束。
}

// savepointSeq generates unique savepoint names per process.
// savepointSeq 在进程内生成唯一保存点名。
var savepointSeq atomic.Int64

// Begin starts a nested transaction on a SAVEPOINT. The returned Tx shares the
// same underlying *sql.Tx; its Rollback only rolls back to the savepoint and
// its Commit only releases it — neither affects the outer transaction.
// Begin 使用 SAVEPOINT 开启嵌套事务。返回的 Tx 共享同一底层 *sql.Tx；
// 其 Rollback 仅回滚到保存点、Commit 仅释放保存点，均不影响外层事务。
func (tx *Tx) Begin(ctx context.Context) (*Tx, error) {
	if err := tx.ensureOpen(); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("rainbowsquirrel_sp_%d", savepointSeq.Add(1))
	if _, err := tx.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return nil, fmt.Errorf("begin savepoint: %w", err)
	}
	return &Tx{tx: tx.tx, cfg: tx.cfg, savepoint: name}, nil
}

// ensureOpen returns sql.ErrTxDone when the Tx is already finished.
// ensureOpen 当 Tx 已结束时返回 sql.ErrTxDone。
func (tx *Tx) ensureOpen() error {
	if tx.closed.Load() {
		return sql.ErrTxDone
	}
	return nil
}

// Commit commits the transaction; for a nested Tx it releases the savepoint.
// Commit 提交事务；嵌套 Tx 则为释放保存点。
func (tx *Tx) Commit() error {
	if err := tx.ensureOpen(); err != nil {
		return err
	}
	if tx.savepoint == "" {
		err := tx.tx.Commit()
		tx.closed.Store(true)
		return err
	}
	if _, err := tx.tx.Exec("RELEASE SAVEPOINT " + tx.savepoint); err != nil {
		return fmt.Errorf("release savepoint %s: %w", tx.savepoint, err)
	}
	tx.closed.Store(true)
	return nil
}

// Rollback rolls back the transaction; for a nested Tx it rolls back to the
// savepoint and releases it, keeping the outer transaction alive.
// Rollback 回滚事务；嵌套 Tx 则回滚到保存点并释放，外层事务保持有效。
func (tx *Tx) Rollback() error {
	if err := tx.ensureOpen(); err != nil {
		return err
	}
	if tx.savepoint == "" {
		err := tx.tx.Rollback()
		tx.closed.Store(true)
		return err
	}
	if _, err := tx.tx.Exec("ROLLBACK TO SAVEPOINT " + tx.savepoint); err != nil {
		return fmt.Errorf("rollback to savepoint %s: %w", tx.savepoint, err)
	}
	if _, err := tx.tx.Exec("RELEASE SAVEPOINT " + tx.savepoint); err != nil {
		return fmt.Errorf("release savepoint %s: %w", tx.savepoint, err)
	}
	tx.closed.Store(true)
	return nil
}

// RawTx returns the underlying *sql.Tx (escape hatch).
// RawTx 返回底层 *sql.Tx（逃生舱）。
func (tx *Tx) RawTx() *sql.Tx { return tx.tx }

// Exec executes a write operation inside the transaction.
// Exec 在事务内执行写操作。
func (tx *Tx) Exec(ctx context.Context, query string, arg any, opts ...CallOption) (sql.Result, error) {
	if err := tx.ensureOpen(); err != nil {
		return nil, err
	}
	return execOp(ctx, tx.tx, tx.cfg, true, query, arg, opts)
}

// Query queries inside the transaction; After is deferred until Rows.Close().
// Query 在事务内查询，After 延迟到 Rows.Close()。
func (tx *Tx) Query(ctx context.Context, query string, arg any, opts ...CallOption) (*Rows, error) {
	if err := tx.ensureOpen(); err != nil {
		return nil, err
	}
	return queryOp(ctx, tx.tx, tx.cfg, true, query, arg, opts)
}

// Get queries a single row inside the transaction.
// Get 在事务内单行查询。
func (tx *Tx) Get[T any](ctx context.Context, query string, arg any, opts ...CallOption) (T, error) {
	var zero T
	if err := tx.ensureOpen(); err != nil {
		return zero, err
	}
	return getOp[T](ctx, tx.tx, tx.cfg, true, query, arg, opts)
}

// Select queries multiple rows inside the transaction.
// Select 在事务内多行查询。
func (tx *Tx) Select[T any](ctx context.Context, query string, arg any, opts ...CallOption) ([]T, error) {
	if err := tx.ensureOpen(); err != nil {
		return nil, err
	}
	return selectOp[T](ctx, tx.tx, tx.cfg, true, query, arg, opts)
}

// ===== Execution flow. 执行流程。 =====

func newExecInfo(op Op, query string, arg any, opts []CallOption, inTx bool) *ExecInfo {
	return &ExecInfo{
		Op:      op,
		Query:   query,
		Arg:     arg,
		Options: opts,
		InTx:    inTx,
		Start:   time.Now(),
	}
}

func runBefore(ctx context.Context, plugins []Plugin, info *ExecInfo) (context.Context, error) {
	for _, p := range plugins {
		var err error
		ctx, err = p.Before(ctx, info)
		if err != nil {
			return ctx, fmt.Errorf("%s: plugin %s: %w", info.Op, p.Name(), err)
		}
	}
	return ctx, nil
}

// runAfter runs After hooks in reverse order (bypass semantics: errors are
// reported via the handler and do not block later plugins or the main result).
// After panics are recovered and reported the same way, so they neither
// propagate nor block later plugins. runAfter 逆序执行 After（旁路：错误经
// handler 上报，不阻断后续插件与主结果）。After panic 同样被 recover 并经
// handler 上报，不传播、不阻断后续插件。
func runAfter(ctx context.Context, plugins []Plugin, info *ExecInfo, cfg *config) {
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		func() {
			defer func() {
				if r := recover(); r != nil && cfg.pluginErrHandler != nil {
					cfg.pluginErrHandler(p.Name(), fmt.Errorf("panic: %v", r))
				}
			}()
			if err := p.After(ctx, info); err != nil && cfg.pluginErrHandler != nil {
				cfg.pluginErrHandler(p.Name(), err)
			}
		}()
	}
}

func execOp(ctx context.Context, ex dbExecutor, cfg *config, inTx bool, query string, arg any, opts []CallOption) (sql.Result, error) {
	plugins := cfg.begin()
	info := newExecInfo(OpExec, query, arg, opts, inTx)

	boundSQL, bindSQL, bindArgs, args, err := bindForExec(query, arg, cfg)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}
	info.BoundSQL, info.BindSQL, info.BindArgs, info.BoundArgs = boundSQL, bindSQL, bindArgs, args

	ctx, err = runBefore(ctx, plugins, info)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}

	res, err := ex.ExecContext(ctx, boundSQL, args...)
	info.Result = res
	info.Err = err
	info.Duration = time.Since(info.Start)
	runAfter(ctx, plugins, info, cfg)
	return res, wrapOpErr(info.Op, query, args, err)
}

func queryOp(ctx context.Context, ex dbExecutor, cfg *config, inTx bool, query string, arg any, opts []CallOption) (*Rows, error) {
	plugins := cfg.begin()
	info := newExecInfo(OpQuery, query, arg, opts, inTx)

	boundSQL, bindSQL, bindArgs, args, err := bindForExec(query, arg, cfg)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}
	info.BoundSQL, info.BindSQL, info.BindArgs, info.BoundArgs = boundSQL, bindSQL, bindArgs, args

	ctx, err = runBefore(ctx, plugins, info)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}

	raw, err := ex.QueryContext(ctx, boundSQL, args...)
	if err != nil {
		info.Err = err
		info.Duration = time.Since(info.Start)
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}
	return &Rows{Rows: raw, after: func() {
		info.Duration = time.Since(info.Start)
		runAfter(ctx, plugins, info, cfg)
	}}, nil
}

func getOp[T any](ctx context.Context, ex dbExecutor, cfg *config, inTx bool, query string, arg any, opts []CallOption) (T, error) {
	var zero T
	plugins := cfg.begin()
	cc := applyCallOptions(opts)
	info := newExecInfo(OpGet, query, arg, opts, inTx)

	boundSQL, bindSQL, bindArgs, args, err := bindForExec(query, arg, cfg)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return zero, wrapOpErr(info.Op, query, args, err)
	}
	info.BoundSQL, info.BindSQL, info.BindArgs, info.BoundArgs = boundSQL, bindSQL, bindArgs, args

	ctx, err = runBefore(ctx, plugins, info)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return zero, wrapOpErr(info.Op, query, args, err)
	}

	rows := info.Rows
	if rows == nil {
		raw, qerr := ex.QueryContext(ctx, boundSQL, args...)
		if qerr != nil {
			info.Err = qerr
			info.Duration = time.Since(info.Start)
			runAfter(ctx, plugins, info, cfg)
			return zero, wrapOpErr(info.Op, query, args, qerr)
		}
		if w := rowWrapperFrom(ctx); w != nil {
			rows = w(raw)
		} else {
			rows = raw
		}
		info.Rows = rows
	}
	if vs, ok := rows.(ValueRowSource); ok {
		cols, vals := vs.RowValues()
		rows = newValueRows(cols, vals)
		info.Rows = rows
	}

	scanErr := scanRows(rows, &zero, cfg, cc)
	closeErr := rows.Close()
	info.Duration = time.Since(info.Start)
	if scanErr != nil {
		info.Err = scanErr
		runAfter(ctx, plugins, info, cfg)
		return zero, wrapOpErr(info.Op, query, args, scanErr)
	}
	if closeErr != nil {
		info.Err = closeErr
		runAfter(ctx, plugins, info, cfg)
		return zero, wrapOpErr(info.Op, query, args, closeErr)
	}
	runAfter(ctx, plugins, info, cfg)
	return zero, nil
}

func selectOp[T any](ctx context.Context, ex dbExecutor, cfg *config, inTx bool, query string, arg any, opts []CallOption) ([]T, error) {
	var slice []T
	plugins := cfg.begin()
	cc := applyCallOptions(opts)
	info := newExecInfo(OpSelect, query, arg, opts, inTx)

	boundSQL, bindSQL, bindArgs, args, err := bindForExec(query, arg, cfg)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}
	info.BoundSQL, info.BindSQL, info.BindArgs, info.BoundArgs = boundSQL, bindSQL, bindArgs, args

	ctx, err = runBefore(ctx, plugins, info)
	if err != nil {
		info.Err = err
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, err)
	}

	rows := info.Rows
	if rows == nil {
		raw, qerr := ex.QueryContext(ctx, boundSQL, args...)
		if qerr != nil {
			info.Err = qerr
			info.Duration = time.Since(info.Start)
			runAfter(ctx, plugins, info, cfg)
			return nil, wrapOpErr(info.Op, query, args, qerr)
		}
		if w := rowWrapperFrom(ctx); w != nil {
			rows = w(raw)
		} else {
			rows = raw
		}
		info.Rows = rows
	}
	if vs, ok := rows.(ValueRowSource); ok {
		cols, vals := vs.RowValues()
		rows = newValueRows(cols, vals)
		info.Rows = rows
	}

	scanErr := scanRows(rows, &slice, cfg, cc)
	closeErr := rows.Close()
	info.Duration = time.Since(info.Start)
	if scanErr != nil {
		info.Err = scanErr
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, scanErr)
	}
	if closeErr != nil {
		info.Err = closeErr
		runAfter(ctx, plugins, info, cfg)
		return nil, wrapOpErr(info.Op, query, args, closeErr)
	}
	runAfter(ctx, plugins, info, cfg)
	return slice, nil
}
