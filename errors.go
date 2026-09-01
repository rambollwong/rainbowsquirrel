package rainbowsquirrel

import (
	"errors"
	"fmt"
	"strings"
)

// Unified framework errors. All return paths wrap errors with fmt.Errorf("...: %w", err),
// keeping errors.Is usable. 框架统一错误。所有返回路径均以 fmt.Errorf("...: %w", err)
// 包装，保证 errors.Is 可用。
var (
	// ErrUnsupportedType indicates an unsupported bind/scan target type.
	// ErrUnsupportedType 表示不支持的绑定/扫描目标类型。
	ErrUnsupportedType = errors.New("rainbowsquirrel: unsupported type")
	// ErrPlaceholderNotFound indicates a named placeholder is missing from the args.
	// ErrPlaceholderNotFound 表示命名占位符在参数中缺失。
	ErrPlaceholderNotFound = errors.New("rainbowsquirrel: placeholder not found")
	// ErrColumnNotFound indicates a query column has no matching field in strict mode.
	// ErrColumnNotFound 表示 strict 模式下查询列无对应字段。
	ErrColumnNotFound = errors.New("rainbowsquirrel: column not found")
	// ErrTagInvalid indicates an illegal db tag format.
	// ErrTagInvalid 表示 db tag 格式非法。
	ErrTagInvalid = errors.New("rainbowsquirrel: invalid tag")
	// ErrSliceExpansion indicates an empty slice cannot be expanded into IN params.
	// ErrSliceExpansion 表示空切片无法展开为 IN 参数。
	ErrSliceExpansion = errors.New("rainbowsquirrel: empty slice for IN expansion")
	// ErrPluginRegisteredTooLate indicates a plugin was registered after first execution.
	// ErrPluginRegisteredTooLate 表示首次执行后注册插件。
	ErrPluginRegisteredTooLate = errors.New("rainbowsquirrel: plugin registered too late")
	// ErrNullNotAllowed indicates a NULL was assigned to a basic type when WithNullToZeroValue(false).
	// ErrNullNotAllowed 表示 WithNullToZeroValue(false) 时遇到 NULL 列值。
	ErrNullNotAllowed = errors.New("rainbowsquirrel: null not allowed")
)

// wrapOpErr wraps an error with the op, the full SQL, and a type-only arg
// summary (no values, per §16 redaction).
// wrapOpErr 将错误包装为带 op、SQL 全文与仅类型参数摘要（§16 脱敏，不含值）。
func wrapOpErr(op Op, query string, args []any, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: query %q, %s: %w", op, query, summarizeArgs(args), err)
}

// summarizeArgs returns a type-only summary of args (no values).
// summarizeArgs 返回参数的类型摘要（不含值）。
func summarizeArgs(args []any) string {
	if len(args) == 0 {
		return "0 args"
	}
	parts := make([]string, len(args))
	for i, a := range args {
		if a == nil {
			parts[i] = "nil"
		} else {
			parts[i] = fmt.Sprintf("%T", a)
		}
	}
	return fmt.Sprintf("%d args: %s", len(args), strings.Join(parts, ", "))
}
