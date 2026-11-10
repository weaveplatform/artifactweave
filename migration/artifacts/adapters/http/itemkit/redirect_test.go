package itemkit

import (
	"testing"
	"time"
)

func TestAccThrottle(t *testing.T) {
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	th := newThrottle(time.Minute)
	for _, c := range []struct {
		after time.Duration
		ok    bool
		held  int
	}{
		{0, true, 0},
		{time.Second, false, 0},
		{59 * time.Second, false, 0},
		{time.Minute, true, 2},
		{90 * time.Second, false, 0},
		{3 * time.Minute, true, 1},
	} {
		if ok, held := th.allow(start.Add(c.after)); ok != c.ok || held != c.held {
			t.Fatalf("at %v: allow %v held %d, want %v %d", c.after, ok, held, c.ok, c.held)
		}
	}
	var none *throttle
	if ok, _ := none.allow(start); !ok {
		t.Fatal("a nil throttle holds nothing back")
	}
}
