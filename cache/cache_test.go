package cache

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/rambollwong/rainbowsquirrel"
)

func TestCodecRoundTrip(t *testing.T) {
	tm := time.Date(2024, 1, 2, 3, 4, 5, 6, time.FixedZone("CST", 8*3600))
	rd := &RowData{
		Columns: []string{"a", "b", "c", "d", "e", "f", "g"},
		Rows: [][]any{
			{nil, int64(-42), 3.14, true, "hi", []byte{0x01, 0x02}, tm},
			{int64(7), nil, 0.0, false, "", nil, nil},
		},
	}
	c := binaryCodec{}
	data, err := c.Encode(rd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) != len(rd.Columns) || len(got.Rows) != 2 {
		t.Fatalf("got = %#v", got)
	}
	if got.Rows[0][1] != int64(-42) {
		t.Fatalf("int64 = %#v", got.Rows[0][1])
	}
	if got.Rows[0][2] != 3.14 {
		t.Fatalf("float = %#v", got.Rows[0][2])
	}
	gt := got.Rows[0][6].(time.Time)
	if !gt.Equal(tm) {
		t.Fatalf("time = %v, want %v", gt, tm)
	}
	if got.Rows[1][6] != nil {
		t.Fatalf("last cell = %#v, want nil", got.Rows[1][6])
	}
}

func TestMemoryStoreLRU(t *testing.T) {
	s := NewMemoryStore(2, 0)
	ctx := context.Background()
	_ = s.Set(ctx, "a", []byte("1"), 0)
	_ = s.Set(ctx, "b", []byte("2"), 0)
	_, ok, _ := s.Get(ctx, "a")
	if !ok {
		t.Fatal("a should exist")
	}
	_ = s.Set(ctx, "c", []byte("3"), 0)
	if _, ok, _ := s.Get(ctx, "b"); ok {
		t.Fatal("b should be evicted")
	}
	if _, ok, _ := s.Get(ctx, "a"); !ok {
		t.Fatal("a should survive (recently used)")
	}
}

func TestMemoryStoreTTL(t *testing.T) {
	s := NewMemoryStore(0, 0)
	ctx := context.Background()
	_ = s.Set(ctx, "k", []byte("v"), 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if _, ok, _ := s.Get(ctx, "k"); ok {
		t.Fatal("k should be expired")
	}
}

func TestMemoryStoreDeletePrefix(t *testing.T) {
	s := NewMemoryStore(0, 0)
	ctx := context.Background()
	_ = s.Set(ctx, "ns1\x00k1", []byte("1"), 0)
	_ = s.Set(ctx, "ns2\x00k2", []byte("2"), 0)
	_ = s.DeletePrefix(ctx, "ns1\x00")
	if _, ok, _ := s.Get(ctx, "ns1\x00k1"); ok {
		t.Fatal("ns1 should be deleted")
	}
	if _, ok, _ := s.Get(ctx, "ns2\x00k2"); !ok {
		t.Fatal("ns2 should survive")
	}
}

func TestCacheKeyConsistency(t *testing.T) {
	k1, err := CacheKey("ns", "SELECT * FROM t WHERE id = :id", map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	k2, err := CacheKey("ns", "SELECT * FROM t WHERE id = :id", map[string]any{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Fatalf("keys differ: %q vs %q", k1, k2)
	}
	k3, _ := CacheKey("ns", "SELECT * FROM t WHERE id = :id", map[string]any{"id": 2})
	if k1 == k3 {
		t.Fatal("different args should give different keys")
	}
}

func TestCachePluginHitAndInvalidate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type User struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	q := "SELECT id, name FROM users WHERE id = :id"
	arg := map[string]any{"id": 1}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name FROM users WHERE id = ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "alice"))

	c := New(NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(db, rainbowsquirrel.WithPlugin(c))
	ctx := context.Background()

	u, err := d.Get[User](ctx, q, arg, WithTTL(time.Minute), WithNamespace("users"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "alice" {
		t.Fatalf("u = %#v", u)
	}

	// Second Get hits the cache: no extra mock expectation proves no real query
	// was issued. 第二次命中缓存：不新增 mock 期望即证明未发真实查询。
	u2, err := d.Get[User](ctx, q, arg, WithTTL(time.Minute), WithNamespace("users"))
	if err != nil {
		t.Fatal(err)
	}
	if u2.ID != 1 {
		t.Fatalf("u2 = %#v", u2)
	}

	// After precise invalidation the query should run again.
	// 精确失效后应再次查询。
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name FROM users WHERE id = ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "alice"))
	if err := c.InvalidateQuery(ctx, "users", q, arg); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get[User](ctx, q, arg, WithTTL(time.Minute), WithNamespace("users")); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCachePluginNoCache(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))

	c := New(NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(db, rainbowsquirrel.WithPlugin(c))
	ctx := context.Background()
	_, err := d.Get[int64](ctx, "SELECT ?", []any{1}, WithTTL(time.Minute), WithNoCache())
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCachePluginSkipInTx(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))
	mock.ExpectCommit()

	c := New(NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(db, rainbowsquirrel.WithPlugin(c))
	ctx := context.Background()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Get[int64](ctx, "SELECT ?", []any{1}, WithTTL(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheReplayUsesDBConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type Event struct {
		At time.Time `db:"at"`
	}
	// Custom layout: the default RFC3339Nano would fail on this value, so a
	// passing replay proves the core applies DB-level config.
	// 自定义 layout：默认 RFC3339Nano 无法解析此值，重放成功即证明核心
	// 使用了 DB 级配置。
	mock.ExpectQuery(regexp.QuoteMeta("SELECT at FROM events")).
		WillReturnRows(sqlmock.NewRows([]string{"at"}).AddRow("02/01/2024 03:04:05"))

	c := New(NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(db,
		rainbowsquirrel.WithPlugin(c),
		rainbowsquirrel.WithTimeLayout("02/01/2006 15:04:05"),
	)
	ctx := context.Background()
	opts := WithTTL(time.Minute)

	e1, err := d.Get[Event](ctx, "SELECT at FROM events", nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Cache hit: no extra mock expectation; replay must still parse the value.
	// 缓存命中：不新增 mock 期望；重放仍应成功解析。
	e2, err := d.Get[Event](ctx, "SELECT at FROM events", nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !e1.At.Equal(e2.At) || e2.At.Year() != 2024 {
		t.Fatalf("e1.At = %v, e2.At = %v", e1.At, e2.At)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCachePluginSingleflight(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT ?")).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))

	c := New(NewMemoryStore(0, 0))
	d := rainbowsquirrel.New(db, rainbowsquirrel.WithPlugin(c))
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Get[int64](ctx, "SELECT ?", []any{1}, WithTTL(time.Minute)); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
