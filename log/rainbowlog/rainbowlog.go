// Package rainbowlog is a RainbowSquirrel logging plugin backed by the
// rainbowlog structured logging library. Package rainbowlog 是基于 rainbowlog
// 结构化日志库的 RainbowSquirrel 日志插件。
package rainbowlog

import (
	"context"
	"strings"
	"time"

	rlevel "github.com/rambollwong/rainbowlog/level"
	rlog "github.com/rambollwong/rainbowlog/log"

	"github.com/rambollwong/rainbowsquirrel"
)

// Log implements rainbowsquirrel.Plugin.
// Log 实现 rainbowsquirrel.Plugin。
type Log struct {
	logger *rlog.Logger
	cfg    logConfig
}

type logConfig struct {
	level         rlevel.Level
	slowThreshold time.Duration
	withQuery     bool
	withArgs      bool
}

// Option is a logging plugin configuration item.
// Option 为日志插件配置项。
type Option func(*logConfig)

// WithLevel sets the recording threshold, default Info.
// WithLevel 设置记录阈值，默认 Info。
func WithLevel(l rlevel.Level) Option {
	return func(c *logConfig) { c.level = l }
}

// WithSlowQueryThreshold sets the slow-query threshold; exceeding it logs a
// separate Warn. WithSlowQueryThreshold 设置慢查询阈值，超时单独 Warn。
func WithSlowQueryThreshold(d time.Duration) Option {
	return func(c *logConfig) { c.slowThreshold = d }
}

// WithQuery controls whether SQL is emitted, default true.
// WithQuery 是否输出 SQL，默认 true。
func WithQuery(enabled bool) Option {
	return func(c *logConfig) { c.withQuery = enabled }
}

// WithArgs controls whether args are emitted, default false (avoid leakage).
// WithArgs 是否输出参数，默认 false（防泄漏）。
func WithArgs(enabled bool) Option {
	return func(c *logConfig) { c.withArgs = enabled }
}

// New creates the plugin; a nil logger falls back to a default rainbowlog
// logger. New 创建插件；logger 为 nil 时使用默认 rainbowlog logger。
func New(logger *rlog.Logger, opts ...Option) *Log {
	if logger == nil {
		logger = rlog.New(rlog.WithDefault())
	}
	cfg := logConfig{
		level:     rlevel.Info,
		withQuery: true,
		withArgs:  false,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &Log{logger: logger, cfg: cfg}
}

func (l *Log) Name() string { return "rainbowlog" }

func (l *Log) Before(ctx context.Context, info *rainbowsquirrel.ExecInfo) (context.Context, error) {
	return ctx, nil
}

func (l *Log) After(ctx context.Context, info *rainbowsquirrel.ExecInfo) error {
	lv := rlevel.Info
	if info.Err != nil {
		lv = rlevel.Error
	} else if l.cfg.slowThreshold > 0 && info.Duration >= l.cfg.slowThreshold {
		lv = rlevel.Warn
	}
	if lv < l.cfg.level {
		return nil
	}
	rec := l.logger.Level(lv).Msg("rainbowsquirrel "+info.Op.String()).
		Str("op", info.Op.String()).
		Dur("duration", time.Millisecond, info.Duration).
		Any("in_tx", info.InTx)
	if l.cfg.withQuery {
		rec.Str("query", compactQuery(info.Query))
	}
	if l.cfg.withArgs {
		rec.Any("args", info.BoundArgs)
	}
	if info.Err != nil {
		rec.Err(info.Err)
	}
	rec.Done()
	return nil
}

// compactQuery collapses runs of whitespace (newlines, tabs, spaces) into a
// single space for log display only; the executed SQL is never altered.
// compactQuery 将连续空白（换行、制表符、空格）折叠为单个空格，仅用于日志
// 展示；执行的 SQL 不会被改动。
func compactQuery(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
