package rainbowsquirrel

import (
	"testing"
	"time"
)

// benchUser is the fast-path scan target. benchUser 为快路径扫描目标。
type benchUser struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
	Age  int    `db:"age"`
}

// benchSlowUser is the slow-path scan target (time.Time conversion).
// benchSlowUser 为慢路径扫描目标（time.Time 转换）。
type benchSlowUser struct {
	ID int64     `db:"id"`
	At time.Time `db:"at"`
}

func benchFastRows() [][]any {
	return [][]any{{int64(1), "alice", int64(30)}}
}

func benchSlowRows() [][]any {
	return [][]any{{int64(1), "2024-01-02T03:04:05Z"}}
}

// BenchmarkScanStructFastPath measures single-row scan on the fast path.
// BenchmarkScanStructFastPath 测量快路径单行扫描。
func BenchmarkScanStructFastPath(b *testing.B) {
	rows := benchFastRows()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs := &fakeRowSource{cols: []string{"id", "name", "age"}, rows: rows}
		var u benchUser
		if err := Scan(rs, &u); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScanStructSlowPath measures single-row scan on the slow path.
// BenchmarkScanStructSlowPath 测量慢路径单行扫描。
func BenchmarkScanStructSlowPath(b *testing.B) {
	rows := benchSlowRows()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs := &fakeRowSource{cols: []string{"id", "at"}, rows: rows}
		var u benchSlowUser
		if err := Scan(rs, &u); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHandwrittenScan is the baseline for the §18 1.5x target.
// BenchmarkHandwrittenScan 为 §18 1.5x 目标的对照基线（手写 rows.Scan）。
func BenchmarkHandwrittenScan(b *testing.B) {
	rows := benchFastRows()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs := &fakeRowSource{cols: []string{"id", "name", "age"}, rows: rows}
		if !rs.Next() {
			b.Fatal("no row")
		}
		var id int64
		var name string
		var age int64
		if err := rs.Scan(&id, &name, &age); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBindNamed measures named binding for a struct.
// BenchmarkBindNamed 测量 struct 命名绑定。
func BenchmarkBindNamed(b *testing.B) {
	type U struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	u := U{ID: 1, Name: "alice"}
	q := "SELECT * FROM t WHERE id = :id AND name = :name"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := BindNamed(q, u); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBindNamedMany measures batch binding of 100 objects.
// BenchmarkBindNamedMany 测量 100 个对象的批量绑定。
func BenchmarkBindNamedMany(b *testing.B) {
	args := make([]any, 100)
	for i := range args {
		args[i] = map[string]any{"id": i, "name": "x"}
	}
	q := "INSERT INTO t (id, name) VALUES (:id, :name)"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := BindNamedMany(q, args); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRebind measures ? → $n rewriting.
// BenchmarkRebind 测量 ? → $n 重绑定。
func BenchmarkRebind(b *testing.B) {
	q := "SELECT * FROM t WHERE a = ? AND b = ? AND c = ?"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Rebind(q, PlaceholderDollar)
	}
}
