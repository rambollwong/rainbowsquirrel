package rainbowsquirrel

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

type eventPlugin struct {
	events []string
}

func (p *eventPlugin) Name() string { return "event" }
func (p *eventPlugin) Before(ctx context.Context, info *ExecInfo) (context.Context, error) {
	p.events = append(p.events, "before:"+info.Op.String())
	return ctx, nil
}
func (p *eventPlugin) After(ctx context.Context, info *ExecInfo) error {
	p.events = append(p.events, "after:"+info.Op.String())
	return nil
}

type gatePlugin struct{}

func (gatePlugin) Name() string { return "gate" }
func (gatePlugin) Before(ctx context.Context, info *ExecInfo) (context.Context, error) {
	return ctx, errors.New("denied")
}
func (gatePlugin) After(ctx context.Context, info *ExecInfo) error { return nil }

func TestGetWithPluginOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type User struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name FROM users WHERE id = ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "alice"))

	p := &eventPlugin{}
	d := New(db, WithPlugin(p))
	u, err := d.Get[User](context.Background(), "SELECT id, name FROM users WHERE id = :id", map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 1 || u.Name != "alice" {
		t.Fatalf("u = %#v", u)
	}
	if len(p.events) != 2 || p.events[0] != "before:Get" || p.events[1] != "after:Get" {
		t.Fatalf("events = %v", p.events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetNoRows(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM t WHERE id = ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	d := New(db)
	_, err := d.Get[int64](context.Background(), "SELECT id FROM t WHERE id = :id", map[string]any{"id": 1})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestSelectEmpty(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM t")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	d := New(db)
	ids, err := d.Select[int64](context.Background(), "SELECT id FROM t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids == nil || len(ids) != 0 {
		t.Fatalf("ids = %#v, want empty non-nil", ids)
	}
}

func TestBindErrorStillRunsAfter(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	p := &eventPlugin{}
	d := New(db, WithPlugin(p))
	// Named placeholder + non-struct/map arg → bind error; After still runs.
	// 命名占位符 + 非 struct/map 参数 → 绑定错误；After 仍触发。
	_, err := d.Get[int64](context.Background(), "SELECT id FROM t WHERE id = :id", 1)
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
	if len(p.events) != 1 || p.events[0] != "after:Get" {
		t.Fatalf("events = %v, want only after:Get", p.events)
	}
}

func TestBeforeGateErrorStillRunsAfter(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	var events []string
	g := &eventPlugin{}
	d := New(db, WithPlugin(gatePlugin{}, g))
	_, err := d.Get[int64](context.Background(), "SELECT 1", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(g.events) != 1 || g.events[0] != "after:Get" {
		t.Fatalf("events = %v, want only after:Get", g.events)
	}
	_ = events
}

func TestQueryAfterDelayedUntilClose(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))

	p := &eventPlugin{}
	d := New(db, WithPlugin(p))
	rows, err := d.Query(context.Background(), "SELECT ?", []any{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.events) != 1 || p.events[0] != "before:Query" {
		t.Fatalf("events = %v, want only before:Query before Close", p.events)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(p.events) != 2 || p.events[1] != "after:Query" {
		t.Fatalf("events = %v, want after:Query after Close", p.events)
	}
}

func TestUseAfterFirstExecFails(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectExec(regexp.QuoteMeta("SELECT 1")).WillReturnResult(sqlmock.NewResult(0, 0))

	d := New(db)
	if _, err := d.Exec(context.Background(), "SELECT 1", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Use(&eventPlugin{}); !errors.Is(err, ErrPluginRegisteredTooLate) {
		t.Fatalf("err = %v, want ErrPluginRegisteredTooLate", err)
	}
}

type panicAfterPlugin struct{}

func (panicAfterPlugin) Name() string { return "panicAfter" }
func (panicAfterPlugin) Before(ctx context.Context, info *ExecInfo) (context.Context, error) {
	return ctx, nil
}
func (panicAfterPlugin) After(ctx context.Context, info *ExecInfo) error { panic("boom") }

func TestAfterPanicDoesNotBlock(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))

	p := &eventPlugin{}
	var reported []string
	d := New(db,
		WithPlugin(p, panicAfterPlugin{}),
		WithPluginErrorHandler(func(plugin string, err error) {
			reported = append(reported, plugin)
		}),
	)
	// After runs in reverse order: panicAfter first, then event.
	// After 逆序：panicAfter 先执行，随后 event 仍应执行。
	if _, err := d.Get[int64](context.Background(), "SELECT ?", []any{1}); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0] != "panicAfter" {
		t.Fatalf("reported = %v, want [panicAfter]", reported)
	}
	if len(p.events) != 2 || p.events[1] != "after:Get" {
		t.Fatalf("events = %v, want later plugin After still run", p.events)
	}
}

func TestTxSharesPlugins(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT ?")).WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))
	mock.ExpectCommit()

	p := &eventPlugin{}
	d := New(db, WithPlugin(p))
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	n, err = tx.Get[int64](context.Background(), "SELECT ?", []any{1})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n = %d", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(p.events) != 2 {
		t.Fatalf("events = %v", p.events)
	}
}
