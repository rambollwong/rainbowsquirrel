package rainbowsquirrel_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rambollwong/rainbowsquirrel"
	"github.com/rambollwong/rainbowsquirrel/cache"
	_ "modernc.org/sqlite"
)

type itUser struct {
	ID        int64          `db:"id"`
	Name      string         `db:"name"`
	Age       *int           `db:"age"`
	Profile   map[string]any `db:"profile,json"`
	CreatedAt time.Time      `db:"created_at"`
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	db, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		age INTEGER,
		profile TEXT,
		created_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestIntegrationCRUD(t *testing.T) {
	ctx := context.Background()
	raw := newTestDB(t)
	d := rainbowsquirrel.New(raw)

	// Exec: named binding insert (time stored as string to exercise the
	// string → time.Time parse path). Exec：命名绑定插入（时间用 string 存储，
	// 验证 string → time.Time 解析路径）。
	res, err := d.Exec(ctx,
		`INSERT INTO users (name, age, profile, created_at) VALUES (:name, :age, :profile, :created_at)`,
		map[string]any{
			"name":       "alice",
			"age":        30,
			"profile":    `{"k":1}`,
			"created_at": "2024-01-02T03:04:05Z",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d", n)
	}

	// Get: named placeholder + struct target. Get：命名占位符 + struct 目标。
	u, err := d.Get[itUser](ctx, `SELECT id, name, age, profile, created_at FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 1 || u.Name != "alice" || u.Age == nil || *u.Age != 30 {
		t.Fatalf("u = %#v", u)
	}
	if u.Profile["k"] != float64(1) {
		t.Fatalf("Profile = %#v", u.Profile)
	}
	if !u.CreatedAt.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("CreatedAt = %v", u.CreatedAt)
	}

	// Select: multiple rows + scalar COUNT. Select：多行 + 标量 COUNT。
	names, err := d.Select[string](ctx, `SELECT name FROM users ORDER BY id`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "alice" {
		t.Fatalf("names = %#v", names)
	}
	count, err := d.Get[int64](ctx, `SELECT COUNT(*) FROM users`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}

	// Map target. map 目标。
	m, err := d.Get[map[string]any](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if m["id"] != int64(1) || m["name"] != "alice" {
		t.Fatalf("m = %#v", m)
	}
}

func TestIntegrationBindNamedMany(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))

	res, err := d.Exec(ctx,
		`INSERT INTO users (name, age) VALUES (:name, :age)`,
		[]map[string]any{
			{"name": "a", "age": 1},
			{"name": "b", "age": 2},
			{"name": "c", "age": 3},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 3 {
		t.Fatalf("RowsAffected = %d", n)
	}
	users, err := d.Select[itUser](ctx, `SELECT id, name, age FROM users ORDER BY id`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 || users[2].Name != "c" {
		t.Fatalf("users = %#v", users)
	}
}

func TestIntegrationSliceExpansion(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t), rainbowsquirrel.WithSliceExpansion(true))

	for _, n := range []string{"a", "b", "c"} {
		if _, err := d.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": n}); err != nil {
			t.Fatal(err)
		}
	}
	users, err := d.Select[itUser](ctx, `SELECT id, name FROM users WHERE id IN (:ids) ORDER BY id`, map[string]any{"ids": []int{1, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].ID != 1 || users[1].ID != 3 {
		t.Fatalf("users = %#v", users)
	}
}

func TestIntegrationPointerTarget(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))
	if _, err := d.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "p"}); err != nil {
		t.Fatal(err)
	}
	u, err := d.Get[*itUser](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.ID != 1 {
		t.Fatalf("u = %#v", u)
	}
	// No rows → nil. 无行 → nil。
	if _, err := d.Get[*itUser](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 999}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestIntegrationNoRows(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))
	_, err := d.Get[itUser](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 1})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
	users, err := d.Select[itUser](ctx, `SELECT id, name FROM users`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("users = %#v, want empty non-nil", users)
	}
}

func TestIntegrationNullPointer(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))
	if _, err := d.Exec(ctx, `INSERT INTO users (name, age) VALUES (:name, :age)`, map[string]any{"name": "n", "age": nil}); err != nil {
		t.Fatal(err)
	}
	u, err := d.Get[itUser](ctx, `SELECT id, name, age FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u.Age != nil {
		t.Fatalf("Age = %v, want nil", u.Age)
	}
}

func TestIntegrationTx(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "tx"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	u, err := d.Get[itUser](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "tx" {
		t.Fatalf("u.Name = %q", u.Name)
	}

	// Invisible after Rollback. Rollback 后不可见。
	tx2, _ := d.BeginTx(ctx, nil)
	if _, err := tx2.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "rollback"}); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}
	count, err := d.Get[int64](ctx, `SELECT COUNT(*) FROM users`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestIntegrationNestedTx(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "outer"}); err != nil {
		t.Fatal(err)
	}

	// Nested tx rollback: inner insert disappears, outer stays.
	// 嵌套回滚：内层插入消失，外层保留。
	sub, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "inner-rollback"}); err != nil {
		t.Fatal(err)
	}
	if err := sub.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Get[int64](ctx, `SELECT 1`, nil); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("sub after rollback: err = %v, want sql.ErrTxDone", err)
	}

	// Nested tx commit: inner insert stays with the outer tx.
	// 嵌套提交：内层插入随外层事务保留。
	sub2, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub2.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "inner-commit"}); err != nil {
		t.Fatal(err)
	}
	if err := sub2.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	names, err := d.Select[string](ctx, `SELECT name FROM users ORDER BY id`, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"outer", "inner-commit"}
	if len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
}

func TestIntegrationQueryRows(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newTestDB(t))
	if _, err := d.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "q"}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(ctx, `SELECT id, name FROM users WHERE id > ?`, []any{0})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var id int64
	var name string
	if !rows.Next() {
		t.Fatal("expected one row")
	}
	if err := rows.Scan(&id, &name); err != nil {
		t.Fatal(err)
	}
	if id != 1 || name != "q" {
		t.Fatalf("id = %d, name = %q", id, name)
	}
	if rows.Next() {
		t.Fatal("expected exactly one row")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationCachePlugin(t *testing.T) {
	ctx := context.Background()
	raw := newTestDB(t)
	c := cache.New(cache.NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(raw, rainbowsquirrel.WithPlugin(c))

	q := `SELECT id, name FROM users WHERE id = :id`
	arg := map[string]any{"id": 1}
	if _, err := d.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": "old"}); err != nil {
		t.Fatal(err)
	}

	u, err := d.Get[itUser](ctx, q, arg, cache.WithTTL(time.Minute), cache.WithNamespace("users"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "old" {
		t.Fatalf("u.Name = %q", u.Name)
	}

	// Update the DB directly, bypassing cache invalidation.
	// 直接改库，绕过缓存失效。
	if _, err := raw.Exec(`UPDATE users SET name = 'new' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	// Cache hit: the old value is still returned. 命中缓存：仍返回旧值。
	u2, err := d.Get[itUser](ctx, q, arg, cache.WithTTL(time.Minute), cache.WithNamespace("users"))
	if err != nil {
		t.Fatal(err)
	}
	if u2.Name != "old" {
		t.Fatalf("u2.Name = %q, want cached old", u2.Name)
	}

	// After precise invalidation the new value is read.
	// 精确失效后读到新值。
	if err := c.InvalidateQuery(ctx, "users", q, arg); err != nil {
		t.Fatal(err)
	}
	u3, err := d.Get[itUser](ctx, q, arg, cache.WithTTL(time.Minute), cache.WithNamespace("users"))
	if err != nil {
		t.Fatal(err)
	}
	if u3.Name != "new" {
		t.Fatalf("u3.Name = %q, want new", u3.Name)
	}
}
