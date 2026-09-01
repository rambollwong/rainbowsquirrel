package rainbowsquirrel

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestBindNamedStruct(t *testing.T) {
	type User struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	q, args, err := BindNamed("SELECT * FROM users WHERE id = :id AND name = :name", User{ID: 1, Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	wantQ := "SELECT * FROM users WHERE id = ? AND name = ?"
	if q != wantQ {
		t.Fatalf("query = %q, want %q", q, wantQ)
	}
	wantArgs := []any{int64(1), "alice"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestBindNamedMap(t *testing.T) {
	q, args, err := BindNamed("SELECT * FROM t WHERE a = :a", map[string]any{"a": 7})
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT * FROM t WHERE a = ?" {
		t.Fatalf("query = %q", q)
	}
	if !reflect.DeepEqual(args, []any{7}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBindNamedMissingKey(t *testing.T) {
	_, _, err := BindNamed("SELECT * FROM t WHERE a = :a", map[string]any{"b": 1})
	if !errors.Is(err, ErrPlaceholderNotFound) {
		t.Fatalf("err = %v, want ErrPlaceholderNotFound", err)
	}
}

func TestBindNamedSkipsLiteralsAndComments(t *testing.T) {
	q, args, err := BindNamed(
		"SELECT ':name' AS s, -- :comment\n id = :id /* :block */ AND x::text",
		map[string]any{"id": 9},
	)
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT ':name' AS s, -- :comment\n id = ? /* :block */ AND x::text" {
		t.Fatalf("query = %q", q)
	}
	if !reflect.DeepEqual(args, []any{9}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBindNamedPositional(t *testing.T) {
	q, args, err := BindNamed("SELECT * FROM t WHERE a = ? AND b = ?", []any{1, "x"})
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT * FROM t WHERE a = ? AND b = ?" {
		t.Fatalf("query = %q", q)
	}
	if !reflect.DeepEqual(args, []any{1, "x"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBindNamedMany(t *testing.T) {
	q, args, err := BindNamedMany("INSERT INTO users (name, age) VALUES (:name, :age)", []any{
		map[string]any{"name": "a", "age": 1},
		map[string]any{"name": "b", "age": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantQ := "INSERT INTO users (name, age) VALUES (?, ?),(?, ?)"
	if q != wantQ {
		t.Fatalf("query = %q, want %q", q, wantQ)
	}
	if !reflect.DeepEqual(args, []any{"a", 1, "b", 2}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBindNamedManyGroupOutside(t *testing.T) {
	_, _, err := BindNamedMany("UPDATE t SET x = :x WHERE id IN (:ids)", []any{map[string]any{"x": 1, "ids": []int{1}}})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestBindNamedManyNoGroup(t *testing.T) {
	_, _, err := BindNamedMany("SELECT * FROM t", []any{map[string]any{"a": 1}})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestRebind(t *testing.T) {
	got := Rebind("SELECT * FROM t WHERE a = ? AND b = ?", PlaceholderDollar)
	want := "SELECT * FROM t WHERE a = $1 AND b = $2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = Rebind("SELECT '?' AS q FROM t WHERE a = ?", PlaceholderDollar)
	want = "SELECT '?' AS q FROM t WHERE a = $1"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = Rebind("SELECT * FROM t WHERE a = ?", PlaceholderAt)
	if got != "SELECT * FROM t WHERE a = @1" {
		t.Fatalf("got %q", got)
	}
}

func TestBindNamedSliceExpansion(t *testing.T) {
	cfg := defaultConfig()
	cfg.sliceExpansion = true
	q, args, bindArgs, err := bindNamed("SELECT * FROM t WHERE id IN (:ids)", map[string]any{"ids": []int{1, 2, 3}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT * FROM t WHERE id IN (?,?,?)" {
		t.Fatalf("query = %q", q)
	}
	if !reflect.DeepEqual(args, []any{1, 2, 3}) {
		t.Fatalf("args = %#v", args)
	}
	if !reflect.DeepEqual(bindArgs, []any{[]int{1, 2, 3}}) {
		t.Fatalf("bindArgs = %#v", bindArgs)
	}
}

func TestBindNamedSliceExpansionEmpty(t *testing.T) {
	cfg := defaultConfig()
	cfg.sliceExpansion = true
	_, _, _, err := bindNamed("SELECT * FROM t WHERE id IN (:ids)", map[string]any{"ids": []int{}}, cfg)
	if !errors.Is(err, ErrSliceExpansion) {
		t.Fatalf("err = %v, want ErrSliceExpansion", err)
	}
}

func TestBindNamedJSONTag(t *testing.T) {
	type Doc struct {
		Meta map[string]any `db:"meta,json"`
	}
	q, args, err := BindNamed("INSERT INTO docs (meta) VALUES (:meta)", Doc{Meta: map[string]any{"k": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if q != "INSERT INTO docs (meta) VALUES (?)" {
		t.Fatalf("query = %q", q)
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d", len(args))
	}
	b, ok := args[0].([]byte)
	if !ok {
		t.Fatalf("arg type = %T, want []byte", args[0])
	}
	if string(b) != `{"k":1}` {
		t.Fatalf("arg = %s", b)
	}
}

func TestBindNamedEmbedded(t *testing.T) {
	type Base struct {
		ID int64 `db:"id"`
	}
	type User struct {
		Base
		Name string `db:"name"`
	}
	q, args, err := BindNamed("SELECT * FROM users WHERE id = :id AND name = :name", User{Base: Base{ID: 5}, Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT * FROM users WHERE id = ? AND name = ?" {
		t.Fatalf("query = %q", q)
	}
	if !reflect.DeepEqual(args, []any{int64(5), "a"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBindNamedOmitEmptyBindsNull(t *testing.T) {
	type User struct {
		ID    int64  `db:"id"`
		Email string `db:"email,omitempty"`
	}
	q, args, err := BindNamed("INSERT INTO users (id, email) VALUES (:id, :email)", User{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if q != "INSERT INTO users (id, email) VALUES (?, ?)" {
		t.Fatalf("query = %q", q)
	}
	if !reflect.DeepEqual(args, []any{int64(1), nil}) {
		t.Fatalf("args = %#v, want nil for omitempty zero value", args)
	}
	// Non-zero value binds normally. 非零值正常绑定。
	_, args2, err := BindNamed("INSERT INTO users (id, email) VALUES (:id, :email)", User{ID: 1, Email: "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args2, []any{int64(1), "a@b.c"}) {
		t.Fatalf("args2 = %#v", args2)
	}
}

func TestBindNamedTimeLocationJSON(t *testing.T) {
	type Meta struct {
		At time.Time `json:"at"`
	}
	type Doc struct {
		Meta Meta `db:"meta,json"`
	}
	loc := time.FixedZone("CST", 8*3600)
	cfg := defaultConfig()
	cfg.timeLocation = loc
	_, args, _, err := bindNamed("INSERT INTO docs (meta) VALUES (:meta)",
		Doc{Meta: Meta{At: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d", len(args))
	}
	b, ok := args[0].([]byte)
	if !ok {
		t.Fatalf("arg type = %T, want []byte", args[0])
	}
	// The nested time must be converted to +08:00. 嵌套时间应转换为 +08:00。
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := "2024-01-02T11:04:05+08:00"
	if m["at"] != want {
		t.Fatalf("json time = %v, want %q", m["at"], want)
	}
}
