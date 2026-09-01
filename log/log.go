// Package log is the RainbowSquirrel slog logging plugin, the second reference
// implementation of the plugin system. Package log 为 RainbowSquirrel 的日志插件
// （slog），作为插件系统的第二参考实现。
package log

import (
	"context"
	"log/slog"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
)

// Log implements rainbowsquirrel.Plugin.
// Log 实现 rainbowsquirrel.Plugin。
type Log struct {
	logger *slog.Logger
	cfg    logConfig
}

type logConfig struct {
	level         slog.Level
	slowThreshold time.Duration
	withQuery     bool
	withArgs      bool
}

// Option is a logging plugin configuration item.
// Option 为日志插件配置项。
type Option func(*logConfig)

// WithLevel sets the recording threshold, default Info.
// WithLevel 设置记录阈值，默认 Info。
func WithLevel(l slog.Level) Option {
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

// New creates the logging plugin; a nil logger falls back to slog.Default().
// New 创建日志插件；logger 为 nil 时使用 slog.Default()。
func New(logger *slog.Logger, opts ...Option) *Log {
	if logger == nil {
		logger = slog.Default()
	}
	cfg := logConfig{
		level:     slog.LevelInfo,
		withQuery: true,
		withArgs:  false,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &Log{logger: logger, cfg: cfg}
}

func (l *Log) Name() string { return "log" }

func (l *Log) Before(ctx context.Context, info *rainbowsquirrel.ExecInfo) (context.Context, error) {
	return ctx, nil
}

func (l *Log) After(ctx context.Context, info *rainbowsquirrel.ExecInfo) error {
	level := slog.LevelInfo
	if info.Err != nil {
		level = slog.LevelError
	} else if l.cfg.slowThreshold > 0 && info.Duration >= l.cfg.slowThreshold {
		level = slog.LevelWarn
	}
	if level < l.cfg.level {
		return nil
	}
	attrs := []slog.Attr{
		slog.String("op", info.Op.String()),
		slog.Duration("duration", info.Duration),
		slog.Bool("in_tx", info.InTx),
	}
	if l.cfg.withQuery {
		attrs = append(attrs, slog.String("query", info.Query))
	}
	if l.cfg.withArgs {
		attrs = append(attrs, slog.Any("args", info.BoundArgs))
	}
	if info.Err != nil {
		attrs = append(attrs, slog.String("error", info.Err.Error()))
	}
	l.logger.LogAttrs(ctx, level, "rainbowsquirrel "+info.Op.String(), attrs...)
	return nil
}
