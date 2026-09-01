package rainbowsquirrel

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestConvertAssignExact(t *testing.T) {
	var n int64
	if err := ConvertAssign(&n, int64(5)); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("n = %d", n)
	}
}

func TestConvertAssignIntConversion(t *testing.T) {
	var n int32
	if err := ConvertAssign(&n, int64(42)); err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Fatalf("n = %d", n)
	}
}

func TestConvertAssignOverflow(t *testing.T) {
	var n int8
	err := ConvertAssign(&n, int64(1000))
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v, want ErrUnsupportedType", err)
	}
}

func TestConvertAssignBytesToString(t *testing.T) {
	var s string
	if err := ConvertAssign(&s, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if s != "hi" {
		t.Fatalf("s = %q", s)
	}
}

func TestConvertAssignBytesToTime(t *testing.T) {
	var tm time.Time
	if err := ConvertAssign(&tm, []byte("2024-01-02T03:04:05Z")); err != nil {
		t.Fatal(err)
	}
	if tm.Year() != 2024 || tm.Month() != time.January || tm.Day() != 2 {
		t.Fatalf("tm = %v", tm)
	}
}

func TestConvertAssignStringToTime(t *testing.T) {
	var tm time.Time
	if err := ConvertAssign(&tm, "2024-01-02T03:04:05Z"); err != nil {
		t.Fatal(err)
	}
	if tm.Year() != 2024 {
		t.Fatalf("tm = %v", tm)
	}
}

func TestConvertAssignNullZero(t *testing.T) {
	var n int64 = 99
	if err := ConvertAssign(&n, nil); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
}

func TestConvertAssignNullDisallowed(t *testing.T) {
	cfg := defaultConfig()
	cfg.nullToZero = false
	var n int64
	err := convertAssign(reflect.ValueOf(&n), nil, cfg)
	if !errors.Is(err, ErrNullNotAllowed) {
		t.Fatalf("err = %v, want ErrNullNotAllowed", err)
	}
}

func TestConvertAssignPointerTarget(t *testing.T) {
	var s *string
	if err := ConvertAssign(&s, "hi"); err != nil {
		t.Fatal(err)
	}
	if s == nil || *s != "hi" {
		t.Fatalf("s = %v", s)
	}
}

func TestConvertAssignScanner(t *testing.T) {
	var ns sql.NullString
	if err := ConvertAssign(&ns, "abc"); err != nil {
		t.Fatal(err)
	}
	if !ns.Valid || ns.String != "abc" {
		t.Fatalf("ns = %#v", ns)
	}
}

func TestConvertAssignCustomConverter(t *testing.T) {
	type MyInt int64
	RegisterConverter[MyInt](func(driverVal any) (MyInt, error) {
		return MyInt(driverVal.(int64) * 10), nil
	})
	var m MyInt
	if err := ConvertAssign(&m, int64(4)); err != nil {
		t.Fatal(err)
	}
	if m != 40 {
		t.Fatalf("m = %d, want 40", m)
	}
}
