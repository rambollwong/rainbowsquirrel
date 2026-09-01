package rainbowsquirrel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
)

// Scan is the public pure function that consumes the whole RowSource.
// dest is *T (single row; no rows return sql.ErrNoRows) or *[]T (multiple rows;
// no rows return an empty non-nil slice). Scan 为公开纯函数：消费整个 RowSource。
// dest 为 *T（单行，无行返回 sql.ErrNoRows）或 *[]T（多行，无行返回空非 nil 切片）。
func Scan(rs RowSource, dest any) error {
	return scanRows(rs, dest, defaultConfig(), callConfig{})
}

// scanRows is the internal scan entry; cfg/cc carry instance config and
// call-level overrides. scanRows 为内部扫描入口，cfg/cc 携带实例配置与调用级覆盖。
func scanRows(rs RowSource, dest any, cfg *config, cc callConfig) error {
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("scan: dest must be a non-nil pointer, got %T: %w", dest, ErrUnsupportedType)
	}
	elem := rv.Elem()
	if elem.Kind() == reflect.Slice {
		return scanSlice(rs, elem, cfg, cc)
	}
	return scanOne(rs, elem, cfg, cc)
}

// scanOne scans a single row: only the first row is read; no rows return
// sql.ErrNoRows. scanOne 单行扫描：仅读第一行，无行返回 sql.ErrNoRows。
func scanOne(rs RowSource, elem reflect.Value, cfg *config, cc callConfig) error {
	cols, err := rs.Columns()
	if err != nil {
		return err
	}
	if !rs.Next() {
		if err := rs.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := scanOneValue(rs, elem, cols, cfg, cc); err != nil {
		return err
	}
	return rs.Err()
}

// scanSlice scans multiple rows: no rows return an empty non-nil slice.
// scanSlice 多行扫描：无行返回空非 nil 切片。
func scanSlice(rs RowSource, slice reflect.Value, cfg *config, cc callConfig) error {
	cols, err := rs.Columns()
	if err != nil {
		return err
	}
	elemType := slice.Type().Elem()
	out := reflect.MakeSlice(slice.Type(), 0, 0)
	for rs.Next() {
		ev := reflect.New(elemType).Elem()
		if err := scanOneValue(rs, ev, cols, cfg, cc); err != nil {
			return err
		}
		out = reflect.Append(out, ev)
	}
	if err := rs.Err(); err != nil {
		return err
	}
	slice.Set(out)
	return nil
}

// scanOneValue fills the current row into elem (a single value or slice element).
// scanOneValue 将当前行填充到 elem（单行值或切片元素）。
func scanOneValue(rs RowSource, elem reflect.Value, cols []string, cfg *config, cc callConfig) error {
	// Get[*T] case: elem is a nil pointer; allocate first, then dereference.
	// Get[*T] 场景：elem 为 nil 指针，先分配再解引用。
	if elem.Kind() == reflect.Ptr {
		elem.Set(reflect.New(elem.Type().Elem()))
		elem = elem.Elem()
	}
	switch elem.Kind() {
	case reflect.Struct:
		return scanStructRow(rs, elem, cols, cfg, cc)
	case reflect.Map:
		if elem.IsNil() {
			elem.Set(reflect.MakeMap(elem.Type()))
		}
		return scanMapRow(rs, elem, cols, cfg)
	default:
		return scanScalarRow(rs, elem, cols, cfg)
	}
}

func effectiveStrict(cfg *config, cc callConfig) bool {
	if cc.strictMode != nil {
		return *cc.strictMode
	}
	return cfg.strictMode
}

func scanStructRow(rs RowSource, rv reflect.Value, cols []string, cfg *config, cc callConfig) error {
	ti, err := getTypeInfo(rv.Type(), cfg)
	if err != nil {
		return err
	}
	strict := effectiveStrict(cfg, cc)

	// Recording mode: fetch raw cell values first, then convert them back into
	// the fields. 录制模式：先取原始单元格值，再转换写回字段。
	if rec, ok := rs.(RowRecorder); ok {
		raw, err := scanRawRow(rs, len(cols))
		if err != nil {
			return err
		}
		rec.RecordRow(raw)
		for i, col := range cols {
			idx, ok := ti.lookupScan(col)
			if !ok {
				if strict {
					return fmt.Errorf("scan: column %q has no matching field: %w", col, ErrColumnNotFound)
				}
				continue
			}
			f := &ti.fields[idx]
			fv := fieldValue(rv, f)
			if err := assignFieldValue(f, fv, raw[i], cfg); err != nil {
				return err
			}
		}
		return nil
	}

	// Normal path: fast path scans directly into the field; slow path scans
	// into any first, then converts. Column lookup happens inline so no
	// intermediate fields slice is allocated; slowCols/slowIdx are allocated
	// only when a slow column exists. 常规路径：快路径直接 Scan 进字段，慢路径
	// 先 Scan 进 any 再转换。列查找内联完成，不分配中间 fields 切片；
	// 仅存在慢列时才分配 slowCols/slowIdx。
	dests := make([]any, len(cols))
	var skip any
	var slowCols []int
	var slowIdx []int
	for i, col := range cols {
		idx, ok := ti.lookupScan(col)
		if !ok {
			if strict {
				return fmt.Errorf("scan: column %q has no matching field: %w", col, ErrColumnNotFound)
			}
			dests[i] = &skip
			continue
		}
		f := &ti.fields[idx]
		fv := fieldValue(rv, f)
		if f.fast && !f.json {
			dests[i] = fv.Addr().Interface()
		} else {
			slot := new(any)
			dests[i] = slot
			slowCols = append(slowCols, i)
			slowIdx = append(slowIdx, idx)
		}
	}
	if err := rs.Scan(dests...); err != nil {
		return err
	}
	for k, i := range slowCols {
		f := &ti.fields[slowIdx[k]]
		fv := fieldValue(rv, f)
		if err := assignFieldValue(f, fv, *(dests[i].(*any)), cfg); err != nil {
			return err
		}
	}
	return nil
}

// assignFieldValue writes a raw driver value into a field (json-tag fields go
// through Unmarshal, the rest through ConvertAssign). assignFieldValue 将驱动
// 原始值写入字段（json tag 走 Unmarshal，其余走 ConvertAssign）。
func assignFieldValue(f *fieldInfo, fv reflect.Value, v any, cfg *config) error {
	dest := fv.Addr().Interface()
	if f.json {
		return unmarshalJSONField(v, dest, cfg)
	}
	return convertAssign(reflect.ValueOf(dest), v, cfg)
}

func unmarshalJSONField(v any, dest any, cfg *config) error {
	if v == nil {
		return convertAssign(reflect.ValueOf(dest), nil, cfg)
	}
	var data []byte
	switch x := v.(type) {
	case []byte:
		data = x
	case string:
		data = []byte(x)
	default:
		return fmt.Errorf("scan: json field expects []byte/string, got %T: %w", v, ErrUnsupportedType)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("scan: json unmarshal: %w", err)
	}
	return nil
}

func scanMapRow(rs RowSource, rv reflect.Value, cols []string, cfg *config) error {
	if rv.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("scan: map key must be string, got %s: %w", rv.Type().Key(), ErrUnsupportedType)
	}
	raw, err := scanRawRow(rs, len(cols))
	if err != nil {
		return err
	}
	if rec, ok := rs.(RowRecorder); ok {
		rec.RecordRow(raw)
	}
	setMapRow(rv, cols, raw, cfg)
	return nil
}

func setMapRow(rv reflect.Value, cols []string, raw []any, cfg *config) {
	elemType := rv.Type().Elem()
	for i, col := range cols {
		key := cfg.mapKeyFunc(col)
		kv := reflect.ValueOf(key)
		var vv reflect.Value
		if raw[i] == nil {
			vv = reflect.Zero(elemType)
		} else if elemType == reflect.TypeOf((*any)(nil)).Elem() {
			vv = reflect.ValueOf(raw[i])
		} else {
			// Non-any value type: convert via ConvertAssign.
			// 值类型非 any：经 ConvertAssign 转换。
			tmp := reflect.New(elemType)
			if err := convertAssign(tmp, raw[i], cfg); err != nil {
				vv = reflect.Zero(elemType)
			} else {
				vv = tmp.Elem()
			}
		}
		rv.SetMapIndex(kv, vv)
	}
}

func scanScalarRow(rs RowSource, elem reflect.Value, cols []string, cfg *config) error {
	if len(cols) != 1 {
		return fmt.Errorf("scan: scalar target requires exactly one column, got %d: %w", len(cols), ErrUnsupportedType)
	}
	raw, err := scanRawRow(rs, 1)
	if err != nil {
		return err
	}
	if rec, ok := rs.(RowRecorder); ok {
		rec.RecordRow(raw)
	}
	return convertAssign(elem.Addr(), raw[0], cfg)
}

// scanRawRow scans the current row into []any and returns the raw driver cell
// values. scanRawRow 将当前行 Scan 进 []any，返回驱动原始单元格值。
func scanRawRow(rs RowSource, n int) ([]any, error) {
	ptrs := make([]any, n)
	for i := range ptrs {
		ptrs[i] = new(any)
	}
	if err := rs.Scan(ptrs...); err != nil {
		return nil, err
	}
	vals := make([]any, n)
	for i, p := range ptrs {
		vals[i] = *(p.(*any))
	}
	return vals, nil
}

// valueRows replays raw row values as a RowSource. It implements RowRecorder
// (no-op) so the core scan layer fetches raw values and converts them with the
// DB-level config instead of package-level defaults.
// valueRows 将原始行值重放为 RowSource。实现 RowRecorder（空操作）使核心扫描层
// 取原始值并用 DB 级配置转换（而非包级默认）。
type valueRows struct {
	cols []string
	rows [][]any
	idx  int
}

// newValueRows builds a replay RowSource from raw values.
// newValueRows 从原始值构建重放 RowSource。
func newValueRows(cols []string, rows [][]any) *valueRows {
	return &valueRows{cols: cols, rows: rows}
}

func (v *valueRows) Columns() ([]string, error) { return v.cols, nil }
func (v *valueRows) Next() bool {
	if v.idx < len(v.rows) {
		v.idx++
		return true
	}
	return false
}
func (v *valueRows) Scan(dest ...any) error {
	row := v.rows[v.idx-1]
	for i, d := range dest {
		if i >= len(row) {
			break
		}
		rv := reflect.ValueOf(d)
		if rv.Kind() != reflect.Ptr || rv.IsNil() {
			return fmt.Errorf("valueRows: dest %d is not a non-nil pointer", i)
		}
		val := row[i]
		if val == nil {
			rv.Elem().SetZero()
			continue
		}
		sv := reflect.ValueOf(val)
		if sv.Type().AssignableTo(rv.Elem().Type()) {
			rv.Elem().Set(sv)
		} else {
			return fmt.Errorf("valueRows: cannot assign %T to %s", val, rv.Elem().Type())
		}
	}
	return nil
}
func (v *valueRows) Err() error             { return nil }
func (v *valueRows) Close() error           { return nil }
func (v *valueRows) RecordRow(values []any) { /* no-op replay hook. 重放钩子空操作。 */ }
