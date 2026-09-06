package rainbowsquirrel

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// PlaceholderStyle defines SQL placeholder dialects.
// PlaceholderStyle 定义 SQL 占位符方言。
type PlaceholderStyle int

const (
	// PlaceholderQuestion keeps ? as-is (default for non-PostgreSQL drivers).
	// PlaceholderQuestion 保持 ? 原样（非 PostgreSQL 驱动的默认值）。
	PlaceholderQuestion PlaceholderStyle = iota
	// PlaceholderDollar rewrites to $1, $2… (PostgreSQL).
	// PlaceholderDollar 转为 $1、$2…（PostgreSQL）。
	PlaceholderDollar
	// PlaceholderAt rewrites to @1, @2….
	// PlaceholderAt 转为 @1、@2…。
	PlaceholderAt
)

// NameMapper maps a Go field name to a column name.
// NameMapper 将 Go 字段名映射为列名。
type NameMapper func(string) string

// config is the runtime configuration shared by *DB/*Tx. All fields except
// plugins/executed are immutable after New.
// config 为 *DB/*Tx 共享的运行时配置。除 plugins/executed 外，其余字段在 New 后不可变。
type config struct {
	tagName        string
	nameMapper     NameMapper
	placeholder    PlaceholderStyle
	strictMode     bool
	sliceExpansion bool
	nullToZero     bool
	timeLocation   *time.Location
	timeLayout     string
	mapKeyFunc     func(string) string

	pluginErrHandler func(plugin string, err error)

	mu       sync.RWMutex // protects plugins and executed. 保护 plugins 与 executed。
	plugins  []Plugin
	executed bool
}

func defaultConfig() *config {
	return &config{
		tagName:        "db",
		nameMapper:     snakeCase,
		placeholder:    PlaceholderQuestion,
		strictMode:     false,
		sliceExpansion: false,
		nullToZero:     true,
		timeLayout:     time.RFC3339Nano,
		mapKeyFunc:     func(s string) string { return s },
	}
}

// addPlugins appends plugins before first execution.
// addPlugins 在首次执行前追加插件。
func (c *config) addPlugins(ps ...Plugin) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.executed {
		return ErrPluginRegisteredTooLate
	}
	c.plugins = append(c.plugins, ps...)
	return nil
}

// begin marks the config as executed and returns a snapshot of the plugin chain.
// begin 标记配置已进入执行，并返回当前插件链快照。
func (c *config) begin() []Plugin {
	c.mu.Lock()
	c.executed = true
	plugins := append([]Plugin(nil), c.plugins...)
	c.mu.Unlock()
	return plugins
}

// Option is a global configuration item applied at New.
// Option 为 New 时的全局配置项。
type Option func(*config)

// WithTagName sets the struct tag name, default "db".
// WithTagName 设置结构体 tag 名，默认 "db"。
func WithTagName(name string) Option {
	return func(c *config) { c.tagName = name }
}

// WithNameMapper sets the field-name → column-name mapper, default snake_case.
// WithNameMapper 设置字段名 → 列名映射函数，默认 snake_case。
func WithNameMapper(m NameMapper) Option {
	return func(c *config) { c.nameMapper = m }
}

// WithPlaceholder sets the placeholder dialect. The default is
// PlaceholderQuestion, except that PostgreSQL drivers (pgx / lib/pq) are
// auto-detected and default to PlaceholderDollar; an explicit setting wins.
// WithPlaceholder 设置占位符方言。默认为 PlaceholderQuestion，但 PostgreSQL
// 驱动（pgx / lib/pq）会被自动识别并默认 PlaceholderDollar；显式设置优先。
func WithPlaceholder(p PlaceholderStyle) Option {
	return func(c *config) { c.placeholder = p }
}

// WithStrictMode sets strict mode: a query column with no matching field returns
// ErrColumnNotFound, default false. WithStrictMode 设置 strict 模式：查询列无对应
// 字段时报 ErrColumnNotFound，默认 false。
func WithStrictMode(strict bool) Option {
	return func(c *config) { c.strictMode = strict }
}

// WithSliceExpansion enables IN expansion when a named placeholder binds a slice
// value, default false. WithSliceExpansion 开启命名占位符绑定切片值时的 IN 展开，默认 false。
func WithSliceExpansion(expand bool) Option {
	return func(c *config) { c.sliceExpansion = expand }
}

// WithNullToZeroValue sets NULL → zero value for basic types, default true;
// false returns ErrNullNotAllowed. WithNullToZeroValue 设置 NULL → 基础类型零值，
// 默认 true；false 时报 ErrNullNotAllowed。
func WithNullToZeroValue(nullToZero bool) Option {
	return func(c *config) { c.nullToZero = nullToZero }
}

// WithTimeLocation sets the base location for time.Time parsing/formatting,
// default driver default. WithTimeLocation 设置 time.Time 解析/格式化基准时区，默认驱动默认。
func WithTimeLocation(loc *time.Location) Option {
	return func(c *config) { c.timeLocation = loc }
}

// WithTimeLayout sets the time parsing/formatting layout, default time.RFC3339Nano.
// WithTimeLayout 设置时间解析/格式化布局，默认 time.RFC3339Nano。
func WithTimeLayout(layout string) Option {
	return func(c *config) { c.timeLayout = layout }
}

// WithMapKeyFunc sets the key handler for map results, default identity.
// WithMapKeyFunc 设置 map 结果的 key 处理函数，默认原样。
func WithMapKeyFunc(f func(string) string) Option {
	return func(c *config) { c.mapKeyFunc = f }
}

// WithPlugin registers plugins at New.
// WithPlugin 在 New 时注册插件。
func WithPlugin(p ...Plugin) Option {
	return func(c *config) { _ = c.addPlugins(p...) }
}

// WithPluginErrorHandler sets the After-hook error reporting callback
// (After errors do not affect the main result). WithPluginErrorHandler 设置 After
// 钩子错误上报回调（After 错误不影响主结果）。
func WithPluginErrorHandler(h func(plugin string, err error)) Option {
	return func(c *config) { c.pluginErrHandler = h }
}

// CallOption is the sealed interface for call-level options. Sub-packages build
// them via NewCallOption and retrieve them via CallOptionKeyValue.
// CallOption 为调用级选项的密封接口。子包经 NewCallOption 构造、经 CallOptionKeyValue 取回。
type CallOption interface {
	callOption()
}

type callOption struct {
	key, value any
}

func (callOption) callOption() {}

// NewCallOption constructs a sealed call-level option. key is usually a custom
// unexported type of the sub-package and value is its strongly typed payload;
// plugins retrieve it in Before/After via CallOptionKeyValue.
// NewCallOption 构造密封的调用级选项。key 通常为子包自定义的未导出类型，
// value 为该选项的强类型载荷；插件在 Before/After 中经 CallOptionKeyValue 取回。
func NewCallOption(key, value any) CallOption {
	return callOption{key: key, value: value}
}

// CallOptionKeyValue returns the key and value of a call-level option,
// for plugin sub-packages to retrieve their own options.
// CallOptionKeyValue 返回调用级选项的 key 与 value，供插件子包取回自建选项。
func CallOptionKeyValue(o CallOption) (key, value any) {
	if co, ok := o.(callOption); ok {
		return co.key, co.value
	}
	return nil, nil
}

type strictModeCallKey struct{}

// WithStrictModeCall overrides the global strict setting for this call.
// WithStrictModeCall 在本次调用覆盖全局 strict 配置。
func WithStrictModeCall(strict bool) CallOption {
	return NewCallOption(strictModeCallKey{}, strict)
}

// callConfig is the merged per-call configuration override.
// callConfig 为本次调用的合并后配置覆盖项。
type callConfig struct {
	strictMode *bool
}

func applyCallOptions(opts []CallOption) callConfig {
	var cc callConfig
	for _, o := range opts {
		if co, ok := o.(callOption); ok {
			switch co.key.(type) {
			case strictModeCallKey:
				if v, ok := co.value.(bool); ok {
					cc.strictMode = &v
				}
			}
		}
	}
	return cc
}

// snakeCase converts a Go exported field name to snake_case:
// UserID → user_id, HTTPServer → http_server.
// snakeCase 将 Go 导出字段名转为 snake_case：UserID → user_id，HTTPServer → http_server。
func snakeCase(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
