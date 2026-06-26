package cache

import (
	"testing"
	"time"
)

func TestCache_TTLAndInvalidate(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	c := New[string, int](10 * time.Second)

	c.Set("a", 42, base)

	if v, ok := c.Get("a", base.Add(5*time.Second)); !ok || v != 42 {
		t.Fatalf("within TTL: got (%d,%v), want (42,true)", v, ok)
	}
	if _, ok := c.Get("a", base.Add(11*time.Second)); ok {
		t.Fatalf("after TTL: expected miss")
	}

	// Re-set extends the lifetime from the new now.
	c.Set("a", 7, base.Add(11*time.Second))
	if v, ok := c.Get("a", base.Add(15*time.Second)); !ok || v != 7 {
		t.Fatalf("after re-set: got (%d,%v), want (7,true)", v, ok)
	}

	c.Invalidate("a")
	if _, ok := c.Get("a", base.Add(15*time.Second)); ok {
		t.Fatalf("after invalidate: expected miss")
	}
}

func TestCache_Miss(t *testing.T) {
	c := New[string, string](time.Minute)
	if _, ok := c.Get("nope", time.Unix(0, 0)); ok {
		t.Fatalf("expected miss for absent key")
	}
}
