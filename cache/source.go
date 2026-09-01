package cache

import (
	"time"

	"github.com/rambollwong/rainbowsquirrel"
)

// cachedRowSource is the replay data source on a cache hit. It implements
// ValueRowSource so the core can take over replay conversion with DB-level
// config; the RowSource methods below serve as a defensive fallback.
// cachedRowSource 为缓存命中时的重放数据源。实现 ValueRowSource 使核心接管
// 重放转换并使用 DB 级配置；下列 RowSource 方法作为防御性 fallback。
type cachedRowSource struct {
	data *RowData
	idx  int
}

// RowValues exposes the raw cached values for core-managed replay.
// RowValues 暴露原始缓存值，供核心接管重放。
func (c *cachedRowSource) RowValues() ([]string, [][]any) {
	return c.data.Columns, c.data.Rows
}

func (c *cachedRowSource) Columns() ([]string, error) { return c.data.Columns, nil }
func (c *cachedRowSource) Next() bool {
	if c.idx < len(c.data.Rows) {
		c.idx++
		return true
	}
	return false
}
func (c *cachedRowSource) Scan(dest ...any) error {
	row := c.data.Rows[c.idx-1]
	for i, d := range dest {
		if i >= len(row) {
			break
		}
		// Cached values are already raw driver types; write them back into
		// dest via the core conversion function. 缓存值已是驱动原始类型，
		// 经核心转换函数写回 dest。
		if err := rainbowsquirrel.ConvertAssign(d, row[i]); err != nil {
			return err
		}
	}
	return nil
}
func (c *cachedRowSource) Err() error   { return nil }
func (c *cachedRowSource) Close() error { return nil }

// recorder collects raw cell values of a cache-miss query for the After phase
// to commit. recorder 收集未命中查询的原始单元格值，供 After 阶段提交缓存。
type recorder struct {
	key  string
	ttl  time.Duration
	cols []string
	rows [][]any
}

func (r *recorder) RecordRow(values []any) {
	cp := append([]any(nil), values...)
	r.rows = append(r.rows, cp)
}

// recordingRowSource wraps the real rows and implements RowRecorder to receive
// raw-value callbacks from the core scan layer.
// recordingRowSource 包装真实 rows，实现 RowRecorder 以接收核心扫描层的原始值回调。
type recordingRowSource struct {
	rows rainbowsquirrel.RowSource
	rec  *recorder
}

func (r *recordingRowSource) Columns() ([]string, error) {
	cols, err := r.rows.Columns()
	if err == nil {
		r.rec.cols = cols
	}
	return cols, err
}
func (r *recordingRowSource) Next() bool             { return r.rows.Next() }
func (r *recordingRowSource) Scan(dest ...any) error { return r.rows.Scan(dest...) }
func (r *recordingRowSource) Err() error             { return r.rows.Err() }
func (r *recordingRowSource) Close() error           { return r.rows.Close() }
func (r *recordingRowSource) RecordRow(values []any) { r.rec.RecordRow(values) }
