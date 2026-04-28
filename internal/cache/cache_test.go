// 遵循project_guide.md
package cache

import (
	"testing"
	"time"
)

func TestTTLCache_SetAndGet(t *testing.T) {
	c := New[string, string](1 * time.Minute)
	defer c.Close()

	c.Set("key1", "value1")

	got, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected Get to return true for existing key")
	}
	if got != "value1" {
		t.Fatalf("expected %q, got %q", "value1", got)
	}
}

func TestTTLCache_GetMissingKey(t *testing.T) {
	c := New[string, int](1 * time.Minute)
	defer c.Close()

	got, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected false for missing key")
	}
	if got != 0 {
		t.Fatalf("expected zero value, got %d", got)
	}
}

func TestTTLCache_ExpiredEntryNotReturned(t *testing.T) {
	c := New[string, string](50 * time.Millisecond)
	defer c.Close()

	c.Set("expiring", "soon")

	// Immediately readable.
	if _, ok := c.Get("expiring"); !ok {
		t.Fatal("expected entry before expiry")
	}

	time.Sleep(100 * time.Millisecond)

	_, ok := c.Get("expiring")
	if ok {
		t.Fatal("expected entry to be expired after TTL elapsed")
	}
}

func TestTTLCache_Delete(t *testing.T) {
	c := New[string, string](1 * time.Minute)
	defer c.Close()

	c.Set("k", "v")
	c.Delete("k")

	_, ok := c.Get("k")
	if ok {
		t.Fatal("expected false after Delete")
	}
}

func TestTTLCache_Flush(t *testing.T) {
	c := New[string, string](1 * time.Minute)
	defer c.Close()

	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")

	if c.Len() != 3 {
		t.Fatalf("expected Len=3, got %d", c.Len())
	}

	c.Flush()

	if c.Len() != 0 {
		t.Fatalf("expected Len=0 after Flush, got %d", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected Get to return false after Flush")
	}
}

func TestTTLCache_OverwriteResetsExpiry(t *testing.T) {
	c := New[string, string](50 * time.Millisecond)
	defer c.Close()

	c.Set("k", "first")
	time.Sleep(40 * time.Millisecond)

	// Overwrite before expiry — resets the clock.
	c.Set("k", "second")
	time.Sleep(40 * time.Millisecond)

	// 80ms total from first set; 40ms from second set — should still be live.
	got, ok := c.Get("k")
	if !ok {
		t.Fatal("expected entry to be alive after overwrite reset")
	}
	if got != "second" {
		t.Fatalf("expected %q, got %q", "second", got)
	}
}

func TestTTLCache_PointerValues(t *testing.T) {
	type payload struct{ Name string }
	c := New[uint, *payload](1 * time.Minute)
	defer c.Close()

	p := &payload{Name: "Balanciz"}
	c.Set(42, p)

	got, ok := c.Get(42)
	if !ok {
		t.Fatal("expected pointer to be stored")
	}
	if got.Name != "Balanciz" {
		t.Fatalf("unexpected name %q", got.Name)
	}
}

func TestTTLCache_Len(t *testing.T) {
	c := New[int, int](1 * time.Minute)
	defer c.Close()

	if c.Len() != 0 {
		t.Fatalf("expected empty, got %d", c.Len())
	}
	c.Set(1, 100)
	c.Set(2, 200)
	if c.Len() != 2 {
		t.Fatalf("expected 2, got %d", c.Len())
	}
}

func TestTTLCache_BoundedEvictsOldestOnInsert(t *testing.T) {
	// Cap of 3 — fourth Set should evict "a" (oldest by write time).
	c := NewBounded[string, string](1*time.Minute, 3)
	defer c.Close()

	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")
	if c.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", c.Len())
	}

	c.Set("d", "4")
	if c.Len() != 3 {
		t.Fatalf("expected cap to hold Len at 3, got %d", c.Len())
	}
	if _, ok := c.Get("a"); ok {
		t.Error("expected oldest entry 'a' to be evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("expected %q to remain", k)
		}
	}
}

func TestTTLCache_BoundedSetMovesToFront(t *testing.T) {
	// Re-Setting a key bumps it back to the front; subsequent eviction
	// targets the next-oldest, not the just-refreshed key.
	c := NewBounded[string, string](1*time.Minute, 3)
	defer c.Close()

	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")
	c.Set("a", "1-refreshed") // bumps "a" back to front; LRU back is now "b"

	c.Set("d", "4") // should evict "b" (now oldest), not "a"
	if _, ok := c.Get("a"); !ok {
		t.Error("expected refreshed 'a' to survive")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("expected 'b' to be evicted")
	}
}

func TestTTLCache_BoundedZeroMeansUnbounded(t *testing.T) {
	c := NewBounded[int, int](1*time.Minute, 0)
	defer c.Close()
	for i := 0; i < 100; i++ {
		c.Set(i, i)
	}
	if c.Len() != 100 {
		t.Fatalf("expected 100 entries (unbounded), got %d", c.Len())
	}
}

func TestTTLCache_FlushWhere(t *testing.T) {
	c := New[string, string](1 * time.Minute)
	defer c.Close()

	c.Set("sp:c1|account|", "a")
	c.Set("sp:c1|vendor|", "b")
	c.Set("sp:c2|account|", "c")

	c.FlushWhere(func(k string) bool {
		return len(k) >= 5 && k[:5] == "sp:c1"
	})

	if _, ok := c.Get("sp:c1|account|"); ok {
		t.Fatal("expected company 1 account key to be flushed")
	}
	if _, ok := c.Get("sp:c1|vendor|"); ok {
		t.Fatal("expected company 1 vendor key to be flushed")
	}
	if got, ok := c.Get("sp:c2|account|"); !ok || got != "c" {
		t.Fatalf("expected company 2 key to remain, got %q ok=%v", got, ok)
	}
}
