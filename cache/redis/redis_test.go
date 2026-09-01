package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// newTestStore starts a miniredis instance and returns a Store plus the client.
// newTestStore 启动 miniredis 并返回 Store 与客户端。
func newTestStore(t *testing.T) (*Store, *goredis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewStore(client), client, mr
}

func TestStoreSetGet(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestStore(t)

	if err := s.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.Get(ctx, "k")
	if err != nil || !ok || string(b) != "v" {
		t.Fatalf("got %q, %v, %v", b, ok, err)
	}
	// Missing key is a miss without error. 缺失 key 为未命中且无错误。
	if _, ok, err := s.Get(ctx, "nope"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
}

func TestStoreTTL(t *testing.T) {
	ctx := context.Background()
	s, _, mr := newTestStore(t)

	if err := s.Set(ctx, "k", []byte("v"), 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(20 * time.Millisecond)
	if _, ok, _ := s.Get(ctx, "k"); ok {
		t.Fatal("key should be expired")
	}
}

func TestStoreDelete(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestStore(t)
	_ = s.Set(ctx, "k", []byte("v"), 0)
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "k"); ok {
		t.Fatal("key should be deleted")
	}
}

func TestStoreDeletePrefix(t *testing.T) {
	ctx := context.Background()
	s, client, _ := newTestStore(t)
	_ = s.Set(ctx, "ns1\x00a", []byte("1"), 0)
	_ = s.Set(ctx, "ns1\x00b", []byte("2"), 0)
	_ = s.Set(ctx, "ns2\x00c", []byte("3"), 0)

	if err := s.DeletePrefix(ctx, "ns1\x00"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "ns1\x00a"); ok {
		t.Fatal("ns1 key should be deleted")
	}
	if _, ok, _ := s.Get(ctx, "ns2\x00c"); !ok {
		t.Fatal("ns2 key should survive")
	}
	_ = client
}

func TestStoreFlushWithPrefix(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })

	s := NewStore(client, WithKeyPrefix("p\x00"))
	_ = s.Set(ctx, "a", []byte("1"), 0)
	_ = client.Set(ctx, "other", "x", 0).Err()

	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "a"); ok {
		t.Fatal("prefixed key should be flushed")
	}
	if v, _ := client.Get(ctx, "other").Result(); v != "x" {
		t.Fatalf("unprefixed key should survive, got %q", v)
	}
}

func TestStoreFlushWholeDB(t *testing.T) {
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: mustMiniredis(t)})
	t.Cleanup(func() { client.Close() })
	s := NewStore(client) // no prefix → FlushDB. 无前缀 → FlushDB。
	_ = client.Set(ctx, "k", "v", 0).Err()

	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := client.DBSize(ctx).Result(); n != 0 {
		t.Fatalf("db size = %d, want 0", n)
	}
}

func mustMiniredis(t *testing.T) string {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	return mr.Addr()
}
