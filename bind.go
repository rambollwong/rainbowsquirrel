package rainbowsquirrel

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// placeholder represents one placeholder in a query. An empty name means a
// positional `?`; otherwise it is the name part of a named `:name`.
// placeholder 表示 query 中的一个占位符。name 为空表示位置占位符 `?`，
// 否则为命名占位符 `:name` 的 name 部分。
type placeholder struct {
	name string
	pos  int // start byte offset (`?` or `:`). 起始字节偏移（`?` 或 `:`）。
	end  int // end byte offset (exclusive). 结束字节偏移（不含）。
}

// parsePlaceholders parses placeholders in query, skipping `::cast`,
// string literals, and comments. parsePlaceholders 解析 query 中的占位符，
// 跳过 `::cast`、字符串字面量与注释。
func parsePlaceholders(query string) []placeholder {
	var places []placeholder
	i := 0
	for i < len(query) {
		c := query[i]
		switch c {
		case '\'', '"', '`':
			quote := c
			i++
			for i < len(query) {
				if query[i] == quote {
					// Handle ''-style escaping. 处理 '' 形式的转义。
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case '-':
			if i+1 < len(query) && query[i+1] == '-' {
				i += 2
				for i < len(query) && query[i] != '\n' {
					i++
				}
			} else {
				i++
			}
		case '/':
			if i+1 < len(query) && query[i+1] == '*' {
				i += 2
				for i+1 < len(query) && !(query[i] == '*' && query[i+1] == '/') {
					i++
				}
				i += 2
			} else {
				i++
			}
		case ':':
			if i+1 < len(query) && query[i+1] == ':' {
				i += 2 // ::cast
				continue
			}
			start := i
			i++
			nameStart := i
			for i < len(query) && isIdentChar(query[i]) {
				i++
			}
			if i == nameStart {
				continue // Lone colon, ignore. 孤立冒号，忽略。
			}
			places = append(places, placeholder{name: query[nameStart:i], pos: start, end: i})
		case '?':
			places = append(places, placeholder{pos: i, end: i + 1})
			i++
		default:
			i++
		}
	}
	return places
}

func isIdentChar(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// BindNamed is the public pure function for named placeholder binding
// (default config, no slice expansion).
// BindNamed 为公开纯函数：命名占位符绑定（默认配置，不展开切片）。
func BindNamed(query string, arg any) (string, []any, error) {
	q, args, _, err := bindNamed(query, arg, nil)
	return q, args, err
}

// bindNamed implements named binding; a nil cfg uses default behavior
// (sliceExpansion=false). It returns bindSQL, final args (possibly with slice
// expansion), and CacheKey-scoped bindArgs (before expansion).
// bindNamed 实现命名绑定；cfg 为 nil 时使用默认行为（sliceExpansion=false）。
// 返回 bindSQL、最终 args（可能含切片展开）、CacheKey 口径的 bindArgs（展开前）。
func bindNamed(query string, arg any, cfg *config) (string, []any, []any, error) {
	places := parsePlaceholders(query)
	named := namedPlaces(places)
	if len(named) == 0 {
		// Positional-args path: pass `?` through unchanged. 位置参数路径：? 原样透传。
		args := positionalArgs(arg)
		return query, args, args, nil
	}

	rv, err := derefValue(reflect.ValueOf(arg))
	if err != nil {
		return "", nil, nil, err
	}
	if isScalarValue(rv) {
		if len(named) != 1 {
			return "", nil, nil, fmt.Errorf("bindNamed: named placeholders require struct or map, got %s: %w",
				typeName(rv), ErrUnsupportedType)
		}
		// Exactly one named placeholder: bind the scalar value to it.
		// 恰好一个命名占位符：将标量单值绑定到该占位符。
		nv, nerr := normalizeBindValue(rv.Interface(), false, locOf(cfg))
		if nerr != nil {
			return "", nil, nil, nerr
		}
		p := named[0]
		bindSQL := query[:p.pos] + "?" + query[p.end:]
		return bindSQL, []any{nv}, []any{nv}, nil
	}
	if rv.Kind() != reflect.Struct && rv.Kind() != reflect.Map {
		return "", nil, nil, fmt.Errorf("bindNamed: named placeholders require struct or map, got %s: %w",
			typeName(rv), ErrUnsupportedType)
	}

	var ti *typeInfo
	if rv.Kind() == reflect.Struct {
		ti, err = getTypeInfo(rv.Type(), cfg)
		if err != nil {
			return "", nil, nil, err
		}
	}

	var b strings.Builder
	b.Grow(len(query) + 8)
	args := make([]any, 0, len(named))
	bindArgs := make([]any, 0, len(named))
	last := 0
	for _, p := range named {
		v, jsonTag, omitEmpty, err := lookupNamedValue(rv, ti, p.name)
		if err != nil {
			return "", nil, nil, err
		}
		b.WriteString(query[last:p.pos])
		last = p.end
		if omitEmpty && isZeroAny(v) {
			// omitempty zero value binds as NULL: keep the placeholder and pass nil.
			// omitempty 空值绑定为 NULL：占位符保留、参数传 nil。
			bindArgs = append(bindArgs, nil)
			args = append(args, nil)
			b.WriteByte('?')
			continue
		}
		if cfg != nil && cfg.sliceExpansion && isExpandableSlice(v) {
			items, err := expandSlice(v, p.name, locOf(cfg))
			if err != nil {
				return "", nil, nil, err
			}
			for i := range items {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteByte('?')
			}
			bindArgs = append(bindArgs, v)
			args = append(args, items...)
			continue
		}
		nv, err := normalizeBindValue(v, jsonTag, locOf(cfg))
		if err != nil {
			return "", nil, nil, err
		}
		bindArgs = append(bindArgs, nv)
		args = append(args, nv)
		b.WriteByte('?')
	}
	b.WriteString(query[last:])
	return b.String(), args, bindArgs, nil
}

// positionalArgs implements positional-arg dispatch: nil → nil; []any → as-is;
// other → single element. positionalArgs 实现位置参数分派：nil → nil；
// []any → 原样；其他 → 单元素。
func positionalArgs(arg any) []any {
	switch v := arg.(type) {
	case nil:
		return nil
	case []any:
		return v
	default:
		return []any{arg}
	}
}

// BindNamedMany is the public pure function with single-group VALUES repetition
// semantics (default config). BindNamedMany 为公开纯函数：单组 VALUES 重复语义（默认配置）。
func BindNamedMany(query string, args []any) (string, []any, error) {
	return bindNamedMany(query, args, nil)
}

// bindNamedMany implements batch named binding: the query must contain exactly
// one contiguous named placeholder group, which is repeated N times according to
// len(args); placeholders outside the group bind once; multiple/no groups error.
// bindNamedMany 实现批量命名绑定：query 中须恰好含一个连续命名占位符组，
// 该组按 args 数量重复 N 组；组外占位符单次绑定；多组或无组报错。
func bindNamedMany(query string, args []any, cfg *config) (string, []any, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("bindNamedMany: empty args: %w", ErrUnsupportedType)
	}
	places := parsePlaceholders(query)
	named := namedPlaces(places)
	if len(named) == 0 {
		return "", nil, fmt.Errorf("bindNamedMany: no named placeholder group: %w", ErrUnsupportedType)
	}

	groups := groupNamedPlaces(named, query)
	if len(groups) > 1 {
		return "", nil, fmt.Errorf("bindNamedMany: expected exactly one contiguous placeholder group, got %d: %w",
			len(groups), ErrUnsupportedType)
	}
	group := groups[0]

	// Pre-resolve each arg object. 预解析每个 arg 对象。
	objs := make([]reflect.Value, len(args))
	var ti *typeInfo
	for i, a := range args {
		rv, err := derefValue(reflect.ValueOf(a))
		if err != nil {
			return "", nil, err
		}
		if rv.Kind() != reflect.Struct && rv.Kind() != reflect.Map {
			return "", nil, fmt.Errorf("bindNamedMany: arg %d must be struct or map, got %s: %w",
				i, typeName(rv), ErrUnsupportedType)
		}
		if rv.Kind() == reflect.Struct && ti == nil {
			ti, err = getTypeInfo(rv.Type(), cfg)
			if err != nil {
				return "", nil, err
			}
		}
		objs[i] = rv
	}

	inGroup := make(map[int]bool, len(group))
	for _, p := range group {
		inGroup[p.pos] = true
	}

	// Extend the group fragment to include the outer parentheses (if any):
	// VALUES (:a, :b) → repeat (:a, :b).
	// 组片段扩展为含外层括号（如有）：VALUES (:a, :b) → 重复 (:a, :b)。
	groupStart := group[0].pos
	for groupStart > 0 && query[groupStart-1] != '(' {
		groupStart--
	}
	if groupStart > 0 && query[groupStart-1] == '(' {
		groupStart--
	}
	groupEnd := group[len(group)-1].end
	for groupEnd < len(query) && query[groupEnd] != ')' {
		groupEnd++
	}
	if groupEnd < len(query) && query[groupEnd] == ')' {
		groupEnd++
	}
	groupSQL := query[groupStart:groupEnd]
	// Build N copies of the group fragment joined by commas; placeholders inside
	// the group are replaced with ?. 生成 N 份组片段，份间用逗号连接；组内占位符替换为 ?。
	repeated := make([]string, len(args))
	for i := range args {
		repeated[i] = replaceNamedInFragment(groupSQL)
	}
	groupReplacement := strings.Join(repeated, ",")

	var b strings.Builder
	b.Grow(len(query) + len(args)*8)
	out := make([]any, 0, len(named)*len(args))
	last := 0
	for _, p := range named {
		if inGroup[p.pos] {
			continue // Group is replaced at the end. 组在最后统一替换。
		}
		v, err := bindValue(objs[0], ti, p.name, cfg)
		if err != nil {
			return "", nil, err
		}
		b.WriteString(query[last:p.pos])
		b.WriteByte('?')
		last = p.end
		out = append(out, v)
	}
	// Write the group. 写入组。
	b.WriteString(query[last:groupStart])
	b.WriteString(groupReplacement)
	last = groupEnd

	// In-group args: take values per object in group placeholder order.
	// 组内参数：每个对象按组内占位符顺序取值。
	for _, obj := range objs {
		for _, p := range group {
			v, err := bindValue(obj, ti, p.name, cfg)
			if err != nil {
				return "", nil, err
			}
			out = append(out, v)
		}
	}
	// Remaining out-of-group placeholders after the group. 组后剩余的组外占位符。
	for _, p := range named {
		if p.pos < last || inGroup[p.pos] {
			continue
		}
		v, err := bindValue(objs[0], ti, p.name, cfg)
		if err != nil {
			return "", nil, err
		}
		b.WriteString(query[last:p.pos])
		b.WriteByte('?')
		last = p.end
		out = append(out, v)
	}
	b.WriteString(query[last:])
	return b.String(), out, nil
}

// groupNamedPlaces merges named placeholders into contiguous groups where the
// text between adjacent placeholders contains only whitespace/commas.
// groupNamedPlaces 将命名占位符按"相邻占位符之间仅含空白/逗号"合并为连续组。
func groupNamedPlaces(named []placeholder, query string) [][]placeholder {
	var groups [][]placeholder
	cur := []placeholder{named[0]}
	for i := 1; i < len(named); i++ {
		between := query[named[i-1].end:named[i].pos]
		if onlySeparators(between) {
			cur = append(cur, named[i])
			continue
		}
		groups = append(groups, cur)
		cur = []placeholder{named[i]}
	}
	groups = append(groups, cur)
	return groups
}

func onlySeparators(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || r == ',' {
			continue
		}
		return false
	}
	return true
}

// replaceNamedInFragment replaces named placeholders inside fragment with ?
// (keeping separators as-is). replaceNamedInFragment 将 fragment 内的命名
// 占位符替换为 ?（保持分隔符原样）。
func replaceNamedInFragment(fragment string) string {
	places := namedPlaces(parsePlaceholders(fragment))
	if len(places) == 0 {
		return fragment
	}
	var b strings.Builder
	last := 0
	for _, p := range places {
		b.WriteString(fragment[last:p.pos])
		b.WriteByte('?')
		last = p.end
	}
	b.WriteString(fragment[last:])
	return b.String()
}

// Rebind rewrites real `?` placeholders in query to $n/@n; it reuses the parser
// and skips literals/comments/::. Rebind 将 query 中真正的 ? 占位符转为 $n/@n；
// 复用解析器跳过字面量/注释/::。
func Rebind(query string, style PlaceholderStyle) string {
	if style == PlaceholderQuestion {
		return query
	}
	places := parsePlaceholders(query)
	var b strings.Builder
	b.Grow(len(query) + len(places)*2)
	last, n := 0, 0
	for _, p := range places {
		if p.name != "" {
			continue
		}
		n++
		b.WriteString(query[last:p.pos])
		switch style {
		case PlaceholderDollar:
			fmt.Fprintf(&b, "$%d", n)
		case PlaceholderAt:
			fmt.Fprintf(&b, "@%d", n)
		}
		last = p.end
	}
	b.WriteString(query[last:])
	return b.String()
}

func namedPlaces(places []placeholder) []placeholder {
	out := make([]placeholder, 0, len(places))
	for _, p := range places {
		if p.name != "" {
			out = append(out, p)
		}
	}
	return out
}

// isScalarValue reports whether a dereferenced value can bind as a single
// scalar placeholder value (basic types, time.Time, []byte, driver.Valuer).
// isScalarValue 判断解引用后的值是否可作为单个标量占位符值绑定（基础类型、
// time.Time、[]byte、driver.Valuer）。
func isScalarValue(rv reflect.Value) bool {
	if !rv.IsValid() {
		return false
	}
	if rv.Type() == timeType || rv.Type() == byteSliceType {
		return true
	}
	if rv.CanInterface() {
		if _, ok := rv.Interface().(driver.Valuer); ok {
			return true
		}
	}
	switch rv.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
}

// derefValue dereferences pointers; a nil pointer is treated as an invalid arg.
// derefValue 解引用指针，nil 指针视为无效参数。
func derefValue(rv reflect.Value) (reflect.Value, error) {
	if !rv.IsValid() {
		return rv, fmt.Errorf("bind: invalid nil argument: %w", ErrUnsupportedType)
	}
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return rv, fmt.Errorf("bind: nil pointer argument: %w", ErrUnsupportedType)
		}
		rv = rv.Elem()
	}
	return rv, nil
}

// lookupNamedValue fetches the raw value by placeholder name from a struct/map
// and returns the json/omitempty tag flags for struct fields.
// lookupNamedValue 从 struct/map 中按占位符名取原始值，并返回 struct 字段的
// json/omitempty tag 标记。
func lookupNamedValue(rv reflect.Value, ti *typeInfo, name string) (any, bool, bool, error) {
	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() != reflect.String {
			return nil, false, false, fmt.Errorf("bind: map key must be string, got %s: %w", rv.Type().Key(), ErrUnsupportedType)
		}
		mv := rv.MapIndex(reflect.ValueOf(name))
		if !mv.IsValid() {
			return nil, false, false, fmt.Errorf("bind: placeholder %q: %w", name, ErrPlaceholderNotFound)
		}
		return mv.Interface(), false, false, nil
	}
	if ti == nil {
		return nil, false, false, fmt.Errorf("bind: missing type info for %s: %w", rv.Type(), ErrUnsupportedType)
	}
	idx, ok := ti.lookupBind(name)
	if !ok {
		return nil, false, false, fmt.Errorf("bind: placeholder %q: %w", name, ErrPlaceholderNotFound)
	}
	f := ti.fields[idx]
	fv := fieldByIndex(rv, f.index)
	if !fv.IsValid() {
		return nil, false, false, fmt.Errorf("bind: placeholder %q: %w", name, ErrPlaceholderNotFound)
	}
	return fv.Interface(), f.json, f.omitEmpty, nil
}

// bindValue fetches the raw value and normalizes it per the bind-direction
// rules; omitempty zero values bind as NULL.
// bindValue 取原始值并按绑定方向规则 normalize；omitempty 零值绑定为 NULL。
func bindValue(rv reflect.Value, ti *typeInfo, name string, cfg *config) (any, error) {
	v, jsonTag, omitEmpty, err := lookupNamedValue(rv, ti, name)
	if err != nil {
		return nil, err
	}
	if omitEmpty && isZeroAny(v) {
		return nil, nil
	}
	return normalizeBindValue(v, jsonTag, locOf(cfg))
}

// normalizeBindValue normalizes a Go value per the §5.4 bind-direction rules;
// loc (if non-nil) is applied to time.Time values before JSON marshaling.
// normalizeBindValue 按 §5.4 绑定方向规则规范化 Go 值；loc 非 nil 时在 JSON
// 序列化前将 time.Time 值转换到该时区。
func normalizeBindValue(v any, jsonEncode bool, loc *time.Location) (any, error) {
	if v == nil {
		return nil, nil
	}
	if _, ok := v.(driver.Valuer); ok {
		return v, nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return rv.Interface(), nil
	case reflect.Slice:
		if rv.Type() == byteSliceType {
			return rv.Interface(), nil
		}
		if jsonEncode {
			return marshalJSON(rv.Interface(), loc)
		}
		return nil, fmt.Errorf("bind: slice value without json tag or slice expansion: %w", ErrUnsupportedType)
	case reflect.Struct:
		if rv.Type() == timeType {
			return rv.Interface(), nil
		}
		if jsonEncode {
			return marshalJSON(rv.Interface(), loc)
		}
		return nil, fmt.Errorf("bind: struct value without json tag: %w", ErrUnsupportedType)
	case reflect.Map:
		if jsonEncode {
			return marshalJSON(rv.Interface(), loc)
		}
		return nil, fmt.Errorf("bind: map value without json tag: %w", ErrUnsupportedType)
	default:
		return nil, fmt.Errorf("bind: unsupported value kind %s: %w", rv.Kind(), ErrUnsupportedType)
	}
}

func marshalJSON(v any, loc *time.Location) (any, error) {
	if loc != nil {
		v = withLocation(v, loc)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("bind: json marshal: %w", err)
	}
	return b, nil
}

// locOf returns the config time location, or nil for a nil config.
// locOf 返回配置的时区；cfg 为 nil 时返回 nil。
func locOf(cfg *config) *time.Location {
	if cfg == nil {
		return nil
	}
	return cfg.timeLocation
}

// withLocation deep-converts all time.Time values in v to loc.
// withLocation 将 v 中所有 time.Time 值深度转换到 loc。
func withLocation(v any, loc *time.Location) any {
	if loc == nil {
		return v
	}
	return convertTimes(reflect.ValueOf(v), loc).Interface()
}

// convertTimes recursively converts time.Time values to loc (deep copy).
// convertTimes 递归将 time.Time 值转换到 loc（深拷贝）。
func convertTimes(rv reflect.Value, loc *time.Location) reflect.Value {
	switch rv.Kind() {
	case reflect.Ptr:
		if rv.IsNil() {
			return rv
		}
		out := reflect.New(rv.Type().Elem())
		out.Elem().Set(convertTimes(rv.Elem(), loc))
		return out
	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		out := reflect.New(rv.Elem().Type()).Elem()
		out.Set(convertTimes(rv.Elem(), loc))
		return out
	case reflect.Struct:
		if rv.Type() == timeType {
			t := rv.Interface().(time.Time)
			return reflect.ValueOf(t.In(loc))
		}
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Field(i)
			if out.Field(i).CanSet() {
				out.Field(i).Set(convertTimes(f, loc))
			}
		}
		return out
	case reflect.Slice:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(convertTimes(rv.Index(i), loc))
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), convertTimes(iter.Value(), loc))
		}
		return out
	default:
		return rv
	}
}

// isExpandableSlice reports whether the value can be expanded into IN params
// (a slice that is not []byte and not a Valuer). isExpandableSlice 判断值是否
// 可展开为 IN 参数（slice 且非 []byte、非 Valuer）。
func isExpandableSlice(v any) bool {
	if v == nil {
		return false
	}
	if _, ok := v.(driver.Valuer); ok {
		return false
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	return rv.Kind() == reflect.Slice && rv.Type() != byteSliceType
}

func expandSlice(v any, name string, loc *time.Location) ([]any, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, fmt.Errorf("bind: placeholder %q: %w", name, ErrSliceExpansion)
		}
		rv = rv.Elem()
	}
	if rv.Len() == 0 {
		return nil, fmt.Errorf("bind: placeholder %q: %w", name, ErrSliceExpansion)
	}
	out := make([]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item, err := normalizeBindValue(rv.Index(i).Interface(), false, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func typeName(rv reflect.Value) string {
	if !rv.IsValid() {
		return "nil"
	}
	return rv.Type().String()
}

// bindForExec is the DB/Tx-layer arg dispatch (§5.1). It returns:
//   - boundSQL: final SQL after Rebind
//   - bindSQL: BindNamed output (before Rebind, CacheKey scope)
//   - bindArgs: BindNamed output args (before expansion, CacheKey scope)
//   - args: final args
//
// bindForExec 为 DB/Tx 层参数分派（§5.1），返回：
//   - boundSQL：Rebind 后的最终 SQL
//   - bindSQL：BindNamed 输出（Rebind 前，CacheKey 口径）
//   - bindArgs：BindNamed 输出参数（展开前，CacheKey 口径）
//   - args：最终参数
func bindForExec(query string, arg any, cfg *config) (boundSQL, bindSQL string, bindArgs, args []any, err error) {
	places := parsePlaceholders(query)
	hasNamed := false
	for _, p := range places {
		if p.name != "" {
			hasNamed = true
			break
		}
	}
	if hasNamed {
		rv, derr := derefValue(reflect.ValueOf(arg))
		if derr != nil {
			return "", "", nil, nil, derr
		}
		switch rv.Kind() {
		case reflect.Struct, reflect.Map:
			bindSQL, args, bindArgs, err = bindNamed(query, arg, cfg)
		case reflect.Slice:
			if rv.Type() == byteSliceType {
				// []byte is a single scalar value. []byte 为单个标量值。
				bindSQL, args, bindArgs, err = bindNamed(query, arg, cfg)
			} else if elem := rv.Type().Elem(); elem.Kind() == reflect.Struct || elem.Kind() == reflect.Map {
				sliceArgs := make([]any, rv.Len())
				for i := 0; i < rv.Len(); i++ {
					sliceArgs[i] = rv.Index(i).Interface()
				}
				bindSQL, args, err = bindNamedMany(query, sliceArgs, cfg)
				bindArgs = args
			} else {
				return "", "", nil, nil, fmt.Errorf("bind: named placeholders require struct/map/slice of struct/map: %w", ErrUnsupportedType)
			}
		default:
			// Scalar value: bindNamed allows it only when exactly one named
			// placeholder exists. 标量单值：bindNamed 仅在恰好一个命名占位符时允许。
			bindSQL, args, bindArgs, err = bindNamed(query, arg, cfg)
		}
		if err != nil {
			return "", "", nil, nil, err
		}
		return Rebind(bindSQL, cfg.placeholder), bindSQL, bindArgs, args, nil
	}

	// Positional-args path. 位置参数路径。
	bindSQL = query
	args = positionalArgs(arg)
	return Rebind(bindSQL, cfg.placeholder), bindSQL, args, args, nil
}
