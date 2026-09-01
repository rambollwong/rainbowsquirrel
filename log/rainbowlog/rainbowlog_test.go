package rainbowlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	rlevel "github.com/rambollwong/rainbowlog/level"
	rlog "github.com/rambollwong/rainbowlog/log"

	"github.com/rambollwong/rainbowsquirrel"
)

// newCaptureLogger builds a rainbowlog logger writing JSON into a buffer.
// newCaptureLogger 构建输出到 buffer 的 rainbowlog JSON logger。
func newCaptureLogger(t *testing.T) (*rlog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := rlog.New(rlog.WithDefault(), rlog.AppendsEncoderWriters(rlog.JsonEnc, &buf))
	t.Cleanup(func() { _ = logger.Flush() })
	return logger, &buf
}

func TestAfterBasic(t *testing.T) {
	logger, buf := newCaptureLogger(t)
	l := New(logger)
	info := &rainbowsquirrel.ExecInfo{
		Op:        rainbowsquirrel.OpGet,
		Query:     "SELECT * FROM t WHERE id = :id",
		BoundArgs: []any{1},
		Duration:  2 * time.Millisecond,
		Start:     time.Now(),
	}
	if err := l.After(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	_ = logger.Flush()
	out := buf.String()
	if !strings.Contains(out, "SELECT * FROM t WHERE id = :id") {
		t.Fatalf("query missing: %s", out)
	}
	if strings.Contains(out, `"args"`) {
		t.Fatalf("args should be hidden by default: %s", out)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
}

func TestAfterError(t *testing.T) {
	logger, buf := newCaptureLogger(t)
	l := New(logger)
	info := &rainbowsquirrel.ExecInfo{
		Op:        rainbowsquirrel.OpExec,
		Query:     "INSERT INTO t VALUES (:id)",
		BoundArgs: []any{1},
		Err:       errors.New("boom"),
		Duration:  time.Millisecond,
		Start:     time.Now(),
	}
	if err := l.After(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	_ = logger.Flush()
	out := buf.String()
	if !strings.Contains(out, "ERROR") {
		t.Fatalf("expected ERROR level: %s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("error message missing: %s", out)
	}
}

func TestAfterSlowQuery(t *testing.T) {
	logger, buf := newCaptureLogger(t)
	l := New(logger, WithSlowQueryThreshold(100*time.Millisecond))
	info := &rainbowsquirrel.ExecInfo{
		Op:       rainbowsquirrel.OpSelect,
		Query:    "SELECT * FROM t",
		Duration: time.Second,
		Start:    time.Now(),
	}
	if err := l.After(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	_ = logger.Flush()
	if !strings.Contains(buf.String(), "WARN") {
		t.Fatalf("expected WARN level: %s", buf.String())
	}
}

func TestAfterLevelThreshold(t *testing.T) {
	logger, buf := newCaptureLogger(t)
	l := New(logger, WithLevel(rlevel.Error))
	info := &rainbowsquirrel.ExecInfo{
		Op:       rainbowsquirrel.OpGet,
		Query:    "SELECT 1",
		Duration: time.Millisecond,
		Start:    time.Now(),
	}
	if err := l.After(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	_ = logger.Flush()
	if buf.Len() != 0 {
		t.Fatalf("info should be below Error threshold, got: %s", buf.String())
	}
}
