package rainbowsquirrel

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// fakeRowSource simulates the *sql.Rows RowSource implementation; Scan assigns
// values directly (mimicking basic driver behavior).
// fakeRowSource 模拟 *sql.Rows 的 RowSource 实现，Scan 直接赋值（模拟驱动基础行为）。
type fakeRowSource struct {
	cols   []string
	rows   [][]any
	idx    int
	closed bool
}

func (f *fakeRowSource) Columns() ([]string, error) { return f.cols, nil }
func (f *fakeRowSource) Next() bool {
	if f.idx < len(f.rows) {
		f.idx++
		return true
	}
	return false
}
func (f *fakeRowSource) Scan(dest ...any) error {
	if f.idx == 0 || f.idx > len(f.rows) {
		return fmt.Errorf("no current row")
	}
	row := f.rows[f.idx-1]
	for i, d := range dest {
		if i >= len(row) {
			break
		}
		v := row[i]
		rv := reflect.ValueOf(d)
		if rv.Kind() != reflect.Ptr || rv.IsNil() {
			return fmt.Errorf("dest %d is not a non-nil pointer", i)
		}
		// Mimic database/sql: dereference pointer targets down to the non-pointer
		// layer. 模拟 database/sql：解引用指针目标到非指针层。
		target := rv
		for target.Elem().Kind() == reflect.Ptr {
			if target.Elem().IsNil() {
				target.Elem().Set(reflect.New(target.Elem().Type().Elem()))
			}
			target = target.Elem()
		}
		if v == nil {
			target.Elem().SetZero()
			continue
		}
		sv := reflect.ValueOf(v)
		if sv.Type().AssignableTo(target.Elem().Type()) {
			target.Elem().Set(sv)
		} else if sv.Type().ConvertibleTo(target.Elem().Type()) {
			target.Elem().Set(sv.Convert(target.Elem().Type()))
		} else {
			return fmt.Errorf("cannot assign %T to %s", v, target.Elem().Type())
		}
	}
	return nil
}
func (f *fakeRowSource) Err() error   { return nil }
func (f *fakeRowSource) Close() error { f.closed = true; return nil }

func TestScanStruct(t *testing.T) {
	type User struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
		Age  *int   `db:"age"`
	}
	rs := &fakeRowSource{
		cols: []string{"id", "name", "age"},
		rows: [][]any{{int64(1), "alice", int64(30)}},
	}
	var u User
	if err := Scan(rs, &u); err != nil {
		t.Fatal(err)
	}
	if u.ID != 1 || u.Name != "alice" || u.Age == nil || *u.Age != 30 {
		t.Fatalf("u = %#v", u)
	}
}

func TestScanSlice(t *testing.T) {
	type User struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	rs := &fakeRowSource{
		cols: []string{"id", "name"},
		rows: [][]any{{int64(1), "a"}, {int64(2), "b"}},
	}
	var users []User
	if err := Scan(rs, &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].ID != 1 || users[1].Name != "b" {
		t.Fatalf("users = %#v", users)
	}
}

func TestScanEmptySlice(t *testing.T) {
	rs := &fakeRowSource{cols: []string{"id"}, rows: nil}
	var users []int64
	if err := Scan(rs, &users); err != nil {
		t.Fatal(err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("users = %#v, want empty non-nil", users)
	}
}

func TestScanNoRows(t *testing.T) {
	rs := &fakeRowSource{cols: []string{"id"}, rows: nil}
	var n int64
	err := Scan(rs, &n)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v, want sql.ErrNoRows", err)
	}
}

func TestScanMap(t *testing.T) {
	rs := &fakeRowSource{
		cols: []string{"id", "name"},
		rows: [][]any{{int64(1), "a"}},
	}
	var m map[string]any
	if err := Scan(rs, &m); err != nil {
		t.Fatal(err)
	}
	if m["id"] != int64(1) || m["name"] != "a" {
		t.Fatalf("m = %#v", m)
	}
}

func TestScanScalar(t *testing.T) {
	rs := &fakeRowSource{
		cols: []string{"n"},
		rows: [][]any{{int64(7)}},
	}
	var n int64
	if err := Scan(rs, &n); err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("n = %d", n)
	}
}

func TestScanPointerType(t *testing.T) {
	type User struct {
		ID int64 `db:"id"`
	}
	rs := &fakeRowSource{
		cols: []string{"id"},
		rows: [][]any{{int64(3)}},
	}
	var u *User
	if err := Scan(rs, &u); err != nil {
		t.Fatal(err)
	}
	if u == nil || u.ID != 3 {
		t.Fatalf("u = %#v", u)
	}
}

func TestScanStrictColumnNotFound(t *testing.T) {
	type User struct {
		ID int64 `db:"id"`
	}
	rs := &fakeRowSource{
		cols: []string{"id", "extra"},
		rows: [][]any{{int64(1), "x"}},
	}
	cfg := defaultConfig()
	cfg.strictMode = true
	var u User
	err := scanRows(rs, &u, cfg, callConfig{})
	if !errors.Is(err, ErrColumnNotFound) {
		t.Fatalf("err = %v, want ErrColumnNotFound", err)
	}
}

func TestScanJSONField(t *testing.T) {
	type Doc struct {
		Meta map[string]any `db:"meta,json"`
	}
	rs := &fakeRowSource{
		cols: []string{"meta"},
		rows: [][]any{{[]byte(`{"k":1}`)}},
	}
	var d Doc
	if err := Scan(rs, &d); err != nil {
		t.Fatal(err)
	}
	if d.Meta["k"] != float64(1) {
		t.Fatalf("Meta = %#v", d.Meta)
	}
}

func TestScanEmbedded(t *testing.T) {
	type Base struct {
		ID int64 `db:"id"`
	}
	type User struct {
		Base
		Name string `db:"name"`
	}
	rs := &fakeRowSource{
		cols: []string{"id", "name"},
		rows: [][]any{{int64(1), "a"}},
	}
	var u User
	if err := Scan(rs, &u); err != nil {
		t.Fatal(err)
	}
	if u.ID != 1 || u.Name != "a" {
		t.Fatalf("u = %#v", u)
	}
}

func TestScanTimeSlowPath(t *testing.T) {
	type Event struct {
		At time.Time `db:"at"`
	}
	rs := &fakeRowSource{
		cols: []string{"at"},
		rows: [][]any{{[]byte("2024-01-02T03:04:05Z")}},
	}
	var e Event
	if err := Scan(rs, &e); err != nil {
		t.Fatal(err)
	}
	if e.At.Year() != 2024 {
		t.Fatalf("At = %v", e.At)
	}
}

func TestScanCaseInsensitiveMatch(t *testing.T) {
	type User struct {
		ID int64
	}
	rs := &fakeRowSource{
		cols: []string{"ID"},
		rows: [][]any{{int64(9)}},
	}
	var u User
	if err := Scan(rs, &u); err != nil {
		t.Fatal(err)
	}
	if u.ID != 9 {
		t.Fatalf("u.ID = %d", u.ID)
	}
}
