package rainbowsquirrel

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var (
	timeType      = reflect.TypeOf(time.Time{})
	byteSliceType = reflect.TypeOf([]byte{})
	scannerType   = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	valuerType    = reflect.TypeOf((*driver.Valuer)(nil)).Elem()
)

// fieldInfo describes a flattened field that can be bound/scanned.
// fieldInfo 描述一个展平后可绑定/扫描的字段。
type fieldInfo struct {
	index     []int        // field index path relative to the top struct. 相对顶层结构体的字段索引路径。
	name      string       // column name (tag name or NameMapper result). 列名（tag 名或 NameMapper 结果）。
	fieldName string       // original field name. 原始字段名。
	typ       reflect.Type // field type. 字段类型。
	json      bool         // json tag option. json tag 选项。
	omitEmpty bool         // omitempty tag option. omitempty tag 选项。
	fast      bool         // whether rows.Scan can be used directly (fast path). 是否可快路径直接 rows.Scan。
	offset    uintptr      // absolute field offset in the top struct (unsafe fast path). 相对顶层结构体的绝对字段偏移（unsafe 快路径）。
	addr      bool         // whether offset is valid. offset 是否有效。
}

// typeInfo is immutable field metadata built once per type.
// typeInfo 为构建后不可变的字段元数据。
type typeInfo struct {
	fields      []fieldInfo
	byColumn    map[string]int // exact tag column name match. tag 列名精确匹配。
	byMapped    map[string]int // NameMapper result match (untagged fields only). NameMapper 结果匹配（仅无 tag 字段）。
	byFieldName map[string]int // exact field name match (bind direction). 字段名精确匹配（绑定方向）。
	byName      map[string]int // lowercase field name match (scan direction, case-insensitive). 字段名小写匹配（扫描方向，大小写不敏感）。
}

type metaKey struct {
	typ     reflect.Type
	tagName string
	mapper  uintptr
}

var typeCache sync.Map // metaKey → *typeInfo

func getTypeInfo(t reflect.Type, cfg *config) (*typeInfo, error) {
	tagName := "db"
	var mapper NameMapper = snakeCase
	if cfg != nil {
		tagName = cfg.tagName
		if cfg.nameMapper != nil {
			mapper = cfg.nameMapper
		}
	}
	key := metaKey{typ: t, tagName: tagName, mapper: reflect.ValueOf(mapper).Pointer()}
	if v, ok := typeCache.Load(key); ok {
		return v.(*typeInfo), nil
	}
	ti, err := buildTypeInfo(t, tagName, mapper)
	if err != nil {
		return nil, err
	}
	actual, _ := typeCache.LoadOrStore(key, ti)
	return actual.(*typeInfo), nil
}

func buildTypeInfo(t reflect.Type, tagName string, mapper NameMapper) (*typeInfo, error) {
	ti := &typeInfo{
		byColumn:    make(map[string]int),
		byMapped:    make(map[string]int),
		byFieldName: make(map[string]int),
		byName:      make(map[string]int),
	}
	if err := collectFields(t, nil, tagName, mapper, ti); err != nil {
		return nil, err
	}
	// Resolve absolute field offsets for the unsafe addressing fast path.
	// Pointer-embedded paths fall back to reflect traversal.
	// 解析绝对字段偏移，供 unsafe 寻址快路径使用；指针嵌入路径回退反射遍历。
	for i := range ti.fields {
		f := &ti.fields[i]
		if off, ok := resolveFieldOffset(t, f.index); ok {
			f.offset = off
			f.addr = true
		}
	}
	return ti, nil
}

// resolveFieldOffset accumulates per-layer offsets along the index path.
// It returns false when the path crosses a pointer field (unsafe addressing
// would need pointer dereference). resolveFieldOffset 沿索引路径逐层累加偏移；
// 路径穿过指针字段时返回 false（unsafe 寻址需解引用指针）。
func resolveFieldOffset(t reflect.Type, index []int) (uintptr, bool) {
	var off uintptr
	for _, idx := range index {
		sf := t.Field(idx)
		off += sf.Offset
		if sf.Type.Kind() == reflect.Ptr {
			return 0, false
		}
		t = sf.Type
	}
	return off, true
}

// collectFields recursively flattens struct fields.
// collectFields 递归展平结构体字段。
func collectFields(t reflect.Type, prefix []int, tagName string, mapper NameMapper, ti *typeInfo) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" { // Skip unexported fields. 未导出字段跳过。
			continue
		}
		index := append(append([]int(nil), prefix...), i)
		tag := sf.Tag.Get(tagName)

		if sf.Anonymous {
			// Anonymous fields with an explicit (non-empty) tag are treated as
			// normal fields; "-" ignores. 有显式 tag（非空）的匿名字段作为
			// 普通字段处理；"-" 忽略。
			if tag != "" {
				if err := addField(sf, index, tag, mapper, ti); err != nil {
					return err
				}
				continue
			}
			ft := sf.Type
			// Special embedded types (time.Time, sql.Null* etc., i.e. Scanner/
			// Valuer) are not flattened but treated as normal fields.
			// 特殊内嵌类型不展平（time.Time、sql.Null* 等 Scanner/Valuer），作为普通字段。
			if ft.Kind() == reflect.Struct && ft != timeType &&
				!reflect.PtrTo(ft).Implements(scannerType) && !reflect.PtrTo(ft).Implements(valuerType) {
				if err := collectFields(ft, index, tagName, mapper, ti); err != nil {
					return err
				}
				continue
			}
			// Embedded pointer-to-struct (e.g. *Base) is flattened recursively.
			// 内嵌结构体指针（如 *Base）递归展平。
			if ft.Kind() == reflect.Ptr && ft.Elem().Kind() == reflect.Struct &&
				ft.Elem() != timeType &&
				!reflect.PtrTo(ft.Elem()).Implements(scannerType) && !reflect.PtrTo(ft.Elem()).Implements(valuerType) {
				if err := collectFields(ft.Elem(), index, tagName, mapper, ti); err != nil {
					return err
				}
				continue
			}
		}
		if err := addField(sf, index, tag, mapper, ti); err != nil {
			return err
		}
	}
	return nil
}

func addField(sf reflect.StructField, index []int, tag string, mapper NameMapper, ti *typeInfo) error {
	f := fieldInfo{
		index:     index,
		fieldName: sf.Name,
		typ:       sf.Type,
	}
	if tag == "-" {
		return nil // Ignore the field. 忽略字段。
	}
	if tag != "" {
		name, opts, err := parseTag(tag)
		if err != nil {
			return err
		}
		f.name = name
		f.json = opts["json"]
		f.omitEmpty = opts["omitempty"]
	} else {
		f.name = mapper(sf.Name)
	}
	if f.name == "" {
		return ErrTagInvalid
	}
	f.fast = canFastScan(f.typ)
	idx := len(ti.fields)
	ti.fields = append(ti.fields, f)
	if tag != "" {
		ti.byColumn[f.name] = idx
	} else {
		ti.byMapped[f.name] = idx
	}
	ti.byFieldName[sf.Name] = idx
	ti.byName[strings.ToLower(sf.Name)] = idx
	return nil
}

// parseTag parses `db:"name,opt1,opt2"`; the column name is required.
// parseTag 解析 `db:"name,opt1,opt2"`，列名必填。
func parseTag(tag string) (name string, opts map[string]bool, err error) {
	parts := strings.Split(tag, ",")
	name = strings.TrimSpace(parts[0])
	if name == "" {
		return "", nil, ErrTagInvalid
	}
	opts = make(map[string]bool, len(parts)-1)
	for _, o := range parts[1:] {
		o = strings.TrimSpace(o)
		if o != "" {
			opts[o] = true
		}
	}
	return name, opts, nil
}

// lookupBind matches in the bind direction: db tag column → NameMapper(field
// name) → exact field name. lookupBind 绑定方向匹配：db tag 列名 →
// NameMapper(字段名) → 字段名精确匹配。
func (ti *typeInfo) lookupBind(name string) (int, bool) {
	if i, ok := ti.byColumn[name]; ok {
		return i, true
	}
	if i, ok := ti.byMapped[name]; ok {
		return i, true
	}
	if i, ok := ti.byFieldName[name]; ok {
		return i, true
	}
	return 0, false
}

// lookupScan matches in the scan direction: db tag → NameMapper(field name) →
// case-insensitive exact match. lookupScan 扫描方向匹配：db tag →
// NameMapper(字段名) → 大小写不敏感精确匹配。
func (ti *typeInfo) lookupScan(col string) (int, bool) {
	if i, ok := ti.byColumn[col]; ok {
		return i, true
	}
	if i, ok := ti.byMapped[col]; ok {
		return i, true
	}
	if i, ok := ti.byName[strings.ToLower(col)]; ok {
		return i, true
	}
	return 0, false
}

// canFastScan reports whether a field can use the fast path (direct rows.Scan).
// After dereferencing pointers, basic types/string/[]byte or sql.Scanner
// implementers qualify; time.Time uses the slow path to guarantee
// []byte/string → time.Time conversion.
// canFastScan 判断字段是否可快路径直接 rows.Scan。解引用指针后为基础类型/
// string/[]byte 或实现 sql.Scanner 者可快路径；time.Time 走慢路径以保证
// []byte/string → time.Time 转换。
func canFastScan(ft reflect.Type) bool {
	for ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}
	if ft == timeType {
		return false
	}
	if reflect.PtrTo(ft).Implements(scannerType) {
		return true
	}
	switch ft.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	case reflect.Slice:
		return ft.Elem().Kind() == reflect.Uint8 // []byte
	default:
		return false
	}
}

// fieldByIndex follows the index path to get a field value (handles embedded
// pointers). fieldByIndex 沿索引路径取值（处理内嵌指针）。
func fieldByIndex(rv reflect.Value, index []int) reflect.Value {
	for i, idx := range index {
		if rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				return reflect.Value{}
			}
			rv = rv.Elem()
		}
		rv = rv.Field(idx)
		if i < len(index)-1 && rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				return reflect.Value{}
			}
			rv = rv.Elem()
		}
	}
	return rv
}

// fieldValue returns the addressable field value, using the cached unsafe
// offset when available and falling back to reflect index traversal.
// fieldValue 返回可寻址的字段值；优先用缓存的 unsafe 偏移，失败回退反射索引遍历。
func fieldValue(rv reflect.Value, f *fieldInfo) reflect.Value {
	if f.addr {
		p := unsafe.Add(unsafe.Pointer(rv.UnsafeAddr()), f.offset)
		return reflect.NewAt(f.typ, p).Elem()
	}
	return fieldByIndex(rv, f.index)
}

// isZeroAny reports whether v is a zero value (nil counts as zero).
// isZeroAny 判断 v 是否为零值（nil 视为零值）。
func isZeroAny(v any) bool {
	if v == nil {
		return true
	}
	return reflect.ValueOf(v).IsZero()
}
