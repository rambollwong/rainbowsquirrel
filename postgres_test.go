package rainbowsquirrel_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rambollwong/rainbowsquirrel"
)

type pgUser struct {
	ID        int64          `db:"id"`
	Name      string         `db:"name"`
	Age       *int           `db:"age"`
	Profile   map[string]any `db:"profile,json"`
	CreatedAt time.Time      `db:"created_at"`
}

func newPGTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; skipping PostgreSQL integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`DROP TABLE IF EXISTS users;
CREATE TABLE users (
	id BIGSERIAL PRIMARY KEY,
	name TEXT NOT NULL,
	age INT,
	profile JSONB,
	created_at TIMESTAMPTZ
)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPostgresCRUD(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t), rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar))

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

	u, err := d.Get[pgUser](ctx,
		`SELECT id, name, age, profile, created_at FROM users WHERE id = :id`,
		map[string]any{"id": 1},
	)
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

	count, err := d.Get[int64](ctx, `SELECT COUNT(*) FROM users`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
}

func TestPostgresCastSkip(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t), rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar))

	s, err := d.Get[string](ctx, `SELECT :name::text`, map[string]any{"name": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if s != "abc" {
		t.Fatalf("s = %q", s)
	}
	n, err := d.Get[int64](ctx, `SELECT :n::int`, map[string]any{"n": 7})
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("n = %d", n)
	}
}

func TestPostgresPositionalRebind(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t), rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar))

	if _, err := d.Exec(ctx, `INSERT INTO users (name) VALUES (?)`, []any{"q"}); err != nil {
		t.Fatal(err)
	}
	u, err := d.Get[pgUser](ctx, `SELECT id, name FROM users WHERE id = ?`, []any{1})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "q" {
		t.Fatalf("u.Name = %q", u.Name)
	}
}

func TestPostgresSliceExpansion(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t),
		rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar),
		rainbowsquirrel.WithSliceExpansion(true),
	)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := d.Exec(ctx, `INSERT INTO users (name) VALUES (:name)`, map[string]any{"name": n}); err != nil {
			t.Fatal(err)
		}
	}
	users, err := d.Select[pgUser](ctx, `SELECT id, name FROM users WHERE id IN (:ids) ORDER BY id`, map[string]any{"ids": []int{1, 3}})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].ID != 1 || users[1].ID != 3 {
		t.Fatalf("users = %#v", users)
	}
}

func TestPostgresJSONBField(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t), rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar))

	// Bind direction: json-tag fields are serialized to JSON and written into a
	// JSONB column. 绑定方向：json tag 字段序列化为 JSON 后写入 JSONB 列。
	res, err := d.Exec(ctx,
		`INSERT INTO users (name, profile) VALUES (:name, :profile)`,
		pgUser{Name: "json", Profile: map[string]any{"k": 1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d", n)
	}
	u, err := d.Get[pgUser](ctx, `SELECT id, name, profile FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u.Profile["k"] != float64(1) {
		t.Fatalf("Profile = %#v", u.Profile)
	}
}

func TestPostgresTx(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t), rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar))

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
	u, err := d.Get[pgUser](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "tx" {
		t.Fatalf("u.Name = %q", u.Name)
	}
}

func TestPostgresNoRows(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t), rainbowsquirrel.WithPlaceholder(rainbowsquirrel.PlaceholderDollar))

	_, err := d.Get[pgUser](ctx, `SELECT id, name FROM users WHERE id = :id`, map[string]any{"id": 1})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
	users, err := d.Select[pgUser](ctx, `SELECT id, name FROM users`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("users = %#v, want empty non-nil", users)
	}
}

// TestPostgresDefaultPlaceholderMultilineInsert reproduces the bug where a
// multi-line INSERT ... RETURNING query fails under the default placeholder
// style on PostgreSQL drivers: `?` is parsed by PostgreSQL as the jsonb
// operator instead of a parameter, yielding "syntax error at or near ','".
// TestPostgresDefaultPlaceholderMultilineInsert 复现默认占位符下多行 INSERT ...
// RETURNING 在 PostgreSQL 驱动上失败的问题：`?` 被 PostgreSQL 当作 jsonb
// 操作符而非参数，报 "syntax error at or near ','"。
func TestPostgresDefaultPlaceholderMultilineInsert(t *testing.T) {
	ctx := context.Background()
	d := rainbowsquirrel.New(newPGTestDB(t)) // 默认配置，未显式设置 WithPlaceholder

	const q = `INSERT INTO users (name, age)
		VALUES (:name, :age)
		RETURNING id`
	id, err := d.Get[int64](ctx, q, map[string]any{"name": "multiline", "age": 18})
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("id = %d, want 1", id)
	}
}
