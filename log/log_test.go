package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
)

func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	return logger, &buf
}

func TestLogAfterBasic(t *testing.T) {
	logger, buf := captureLogger()
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
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log output not JSON: %v\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "SELECT * FROM t WHERE id = :id") {
		t.Fatalf("query missing: %s", buf.String())
	}
	if strings.Contains(buf.String(), `"args"`) {
		t.Fatalf("args should be hidden by default: %s", buf.String())
	}
}

func TestLogAfterError(t *testing.T) {
	logger, buf := captureLogger()
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
	if !strings.Contains(buf.String(), "ERROR") {
		t.Fatalf("expected ERROR level: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("error message missing: %s", buf.String())
	}
}

func TestLogAfterSlowQuery(t *testing.T) {
	logger, buf := captureLogger()
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
	if !strings.Contains(buf.String(), "WARN") {
		t.Fatalf("expected WARN level: %s", buf.String())
	}
}

func TestLogAfterCompactsQueryWhitespace(t *testing.T) {
	logger, buf := captureLogger()
	l := New(logger)
	info := &rainbowsquirrel.ExecInfo{
		Op:       rainbowsquirrel.OpGet,
		Query:    "SELECT *\n\tFROM t\nWHERE id = :id",
		Duration: time.Millisecond,
		Start:    time.Now(),
	}
	if err := l.After(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "SELECT * FROM t WHERE id = :id") {
		t.Fatalf("query should be compacted to a single line: %s", out)
	}
	if strings.Contains(out, `\n`) || strings.Contains(out, `\t`) {
		t.Fatalf("query should not contain newline/tab escapes: %s", out)
	}
}
