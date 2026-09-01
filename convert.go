package rainbowsquirrel

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// converterFunc is the internal shape of a registered converter.
// converterFunc 为注册表内部存储的转换函数形态。
type converterFunc func(driverVal any) (any, error)

var (
	converterMu sync.RWMutex
	converters  = map[reflect.Type]converterFunc{}
)

// RegisterConverter registers a custom DB→Go converter matched by target type.
// It should be done before first execution; runtime registration is safe under
// the lock but should be avoided. RegisterConverter 注册 DB→Go 方向的自定义转换器，
// 按目标类型匹配。需在首次执行前完成；运行期注册在锁保护下安全但应避免。
func RegisterConverter[T any](fn func(driverVal any) (T, error)) {
	var zero T
	typ := reflect.TypeOf(zero)
	converterMu.Lock()
	converters[typ] = func(v any) (any, error) { return fn(v) }
	converterMu.Unlock()
}

func lookupConverter(t reflect.Type) (converterFunc, bool) {
	converterMu.RLock()
	fn, ok := converters[t]
	converterMu.RUnlock()
	return fn, ok
}

// ConvertAssign converts a raw driver cell value into the target pointed to by
// dest (a non-nil pointer). It uses package-level default rules
// (nullToZero=true, RFC3339Nano, no custom location) and is reused by the cache
// recording path. ConvertAssign 将驱动原始单元格值转换为 dest（非 nil 指针）指向的
// 目标值。使用包级默认规则（nullToZero=true、RFC3339Nano、无自定义时区），
// 供 cache 录制路径复用。
func ConvertAssign(dest any, driverVal any) error {
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return fmt.Errorf("convertAssign: dest must be a non-nil pointer: %w", ErrUnsupportedType)
	}
	return convertAssign(dv, driverVal, defaultConfig())
}

// convertAssign is the internal conversion entry; cfg provides
// nullToZero/timeLayout/timeLocation. Priority: target implements sql.Scanner →
// registry → built-in conversion matrix → ErrUnsupportedType.
// convertAssign 为内部转换入口，cfg 提供 nullToZero/timeLayout/timeLocation。
// 优先级：目标实现 sql.Scanner → 注册表 → 内置转换矩阵 → ErrUnsupportedType。
func convertAssign(dv reflect.Value, src any, cfg *config) error {
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return fmt.Errorf("convertAssign: dest must be a non-nil pointer: %w", ErrUnsupportedType)
	}
	// 1) Target type implements sql.Scanner (including *time.Time etc.).
	// 1) 目标类型实现 sql.Scanner（含 *time.Time 等）。
	if scanner, ok := dv.Interface().(sql.Scanner); ok {
		return scanner.Scan(src)
	}
	elem := dv.Elem()
	// 2) nil handling. 2) nil 处理。
	if src == nil {
		return assignNull(elem, cfg)
	}
	// Exact type match (including pointer targets). 类型完全匹配（含指针目标）。
	if sv := reflect.ValueOf(src); sv.IsValid() {
		if elem.Kind() == reflect.Ptr {
			if sv.Type() == elem.Type() {
				elem.Set(sv)
				return nil
			}
			if sv.Type().AssignableTo(elem.Type()) {
				elem.Set(sv)
				return nil
			}
		} else if sv.Type() == elem.Type() {
			elem.Set(sv)
			return nil
		} else if sv.Type().AssignableTo(elem.Type()) {
			elem.Set(sv)
			return nil
		}
	}
	// 3) Registry. 3) 注册表。
	if fn, ok := lookupConverter(elem.Type()); ok {
		v, err := fn(src)
		if err != nil {
			return fmt.Errorf("convertAssign: converter for %s: %w", elem.Type(), err)
		}
		rv := reflect.ValueOf(v)
		if !rv.IsValid() {
			return assignNull(elem, cfg)
		}
		elem.Set(rv)
		return nil
	}
	// Pointer target: allocate an element and convert recursively.
	// 指针目标：分配元素后递归转换。
	if elem.Kind() == reflect.Ptr {
		elem.Set(reflect.New(elem.Type().Elem()))
		return convertAssign(elem, src, cfg)
	}
	// 4) Built-in conversion matrix. 4) 内置转换矩阵。
	return builtinConvert(elem, src, cfg)
}

func assignNull(elem reflect.Value, cfg *config) error {
	nullToZero := true
	if cfg != nil {
		nullToZero = cfg.nullToZero
	}
	if elem.Kind() == reflect.Ptr {
		elem.SetZero()
		return nil
	}
	if nullToZero {
		elem.SetZero()
		return nil
	}
	return fmt.Errorf("convertAssign: NULL into %s: %w", elem.Type(), ErrNullNotAllowed)
}

func builtinConvert(elem reflect.Value, src any, cfg *config) error {
	layout := time.RFC3339Nano
	var loc *time.Location
	if cfg != nil {
		layout = cfg.timeLayout
		loc = cfg.timeLocation
	}
	switch elem.Kind() {
	case reflect.String:
		switch v := src.(type) {
		case string:
			elem.SetString(v)
			return nil
		case []byte:
			elem.SetString(string(v))
			return nil
		}
	case reflect.Slice:
		if elem.Type().Elem().Kind() == reflect.Uint8 {
			switch v := src.(type) {
			case []byte:
				elem.SetBytes(v)
				return nil
			case string:
				elem.SetBytes([]byte(v))
				return nil
			}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return convertToInt(elem, src)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return convertToUint(elem, src)
	case reflect.Float32, reflect.Float64:
		return convertToFloat(elem, src)
	case reflect.Bool:
		return convertToBool(elem, src)
	case reflect.Struct:
		if elem.Type() == timeType {
			return convertToTime(elem, src, layout, loc)
		}
	}
	return fmt.Errorf("convertAssign: cannot convert %T to %s: %w", src, elem.Type(), ErrUnsupportedType)
}

func convertToInt(elem reflect.Value, src any) error {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	var i int64
	switch v := src.(type) {
	case int64:
		i = v
	case int:
		i = int64(v)
	case int32:
		i = int64(v)
	case int16:
		i = int64(v)
	case int8:
		i = int64(v)
	case uint64:
		if v > uint64(maxInt64) {
			return overflowErr(src, elem.Type())
		}
		i = int64(v)
	case uint:
		i = int64(v)
	case uint32:
		i = int64(v)
	case uint16:
		i = int64(v)
	case uint8:
		i = int64(v)
	case float64:
		if v > float64(maxInt64) || v < float64(minInt64) {
			return overflowErr(src, elem.Type())
		}
		i = int64(v)
	case float32:
		i = int64(v)
	case []byte:
		return parseIntString(elem, string(v))
	case string:
		return parseIntString(elem, v)
	default:
		return fmt.Errorf("convertAssign: cannot convert %T to %s: %w", src, elem.Type(), ErrUnsupportedType)
	}
	if elem.OverflowInt(i) {
		return overflowErr(src, elem.Type())
	}
	elem.SetInt(i)
	return nil
}

func convertToUint(elem reflect.Value, src any) error {
	var u uint64
	switch v := src.(type) {
	case int64:
		if v < 0 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case int:
		if v < 0 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case int32:
		if v < 0 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case int16:
		if v < 0 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case int8:
		if v < 0 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case uint64:
		u = v
	case uint:
		u = uint64(v)
	case uint32:
		u = uint64(v)
	case uint16:
		u = uint64(v)
	case uint8:
		u = uint64(v)
	case float64:
		if v < 0 || v > 1<<64-1 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case float32:
		if v < 0 {
			return overflowErr(src, elem.Type())
		}
		u = uint64(v)
	case []byte:
		return parseUintString(elem, string(v))
	case string:
		return parseUintString(elem, v)
	default:
		return fmt.Errorf("convertAssign: cannot convert %T to %s: %w", src, elem.Type(), ErrUnsupportedType)
	}
	if elem.OverflowUint(u) {
		return overflowErr(src, elem.Type())
	}
	elem.SetUint(u)
	return nil
}

func convertToFloat(elem reflect.Value, src any) error {
	var f float64
	switch v := src.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int64:
		f = float64(v)
	case int:
		f = float64(v)
	case int32:
		f = float64(v)
	case uint64:
		f = float64(v)
	case uint:
		f = float64(v)
	case []byte:
		return parseFloatString(elem, string(v))
	case string:
		return parseFloatString(elem, v)
	default:
		return fmt.Errorf("convertAssign: cannot convert %T to %s: %w", src, elem.Type(), ErrUnsupportedType)
	}
	if elem.OverflowFloat(f) {
		return overflowErr(src, elem.Type())
	}
	elem.SetFloat(f)
	return nil
}

func convertToBool(elem reflect.Value, src any) error {
	switch v := src.(type) {
	case bool:
		elem.SetBool(v)
		return nil
	case int64:
		elem.SetBool(v != 0)
		return nil
	case int:
		elem.SetBool(v != 0)
		return nil
	case []byte:
		return parseBoolString(elem, string(v))
	case string:
		return parseBoolString(elem, v)
	default:
		return fmt.Errorf("convertAssign: cannot convert %T to %s: %w", src, elem.Type(), ErrUnsupportedType)
	}
}

func convertToTime(elem reflect.Value, src any, layout string, loc *time.Location) error {
	switch v := src.(type) {
	case time.Time:
		elem.Set(reflect.ValueOf(v))
		return nil
	case []byte:
		return parseTimeString(elem, string(v), layout, loc)
	case string:
		return parseTimeString(elem, v, layout, loc)
	default:
		return fmt.Errorf("convertAssign: cannot convert %T to %s: %w", src, elem.Type(), ErrUnsupportedType)
	}
}

func parseTimeString(elem reflect.Value, s, layout string, loc *time.Location) error {
	var (
		t   time.Time
		err error
	)
	if loc != nil {
		t, err = time.ParseInLocation(layout, s, loc)
	} else {
		t, err = time.Parse(layout, s)
	}
	if err != nil {
		return fmt.Errorf("convertAssign: parse time %q with layout %q: %w", s, layout, err)
	}
	elem.Set(reflect.ValueOf(t))
	return nil
}

func parseIntString(elem reflect.Value, s string) error {
	i, err := strconv.ParseInt(s, 10, elem.Type().Bits())
	if err != nil {
		return fmt.Errorf("convertAssign: parse int %q: %w", s, err)
	}
	elem.SetInt(i)
	return nil
}

func parseUintString(elem reflect.Value, s string) error {
	u, err := strconv.ParseUint(s, 10, elem.Type().Bits())
	if err != nil {
		return fmt.Errorf("convertAssign: parse uint %q: %w", s, err)
	}
	elem.SetUint(u)
	return nil
}

func parseFloatString(elem reflect.Value, s string) error {
	f, err := strconv.ParseFloat(s, elem.Type().Bits())
	if err != nil {
		return fmt.Errorf("convertAssign: parse float %q: %w", s, err)
	}
	elem.SetFloat(f)
	return nil
}

func parseBoolString(elem reflect.Value, s string) error {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("convertAssign: parse bool %q: %w", s, err)
	}
	elem.SetBool(b)
	return nil
}

func overflowErr(src any, dst reflect.Type) error {
	return fmt.Errorf("convertAssign: value %v overflows %s: %w", src, dst, ErrUnsupportedType)
}
