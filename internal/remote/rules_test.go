package remote

import (
	"net/http"
	"testing"
	"time"
)

func TestCacheFreshnessRules(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		mutable      bool
		age, ttl     time.Duration
		fresh, stale bool
	}{
		{"unique ignores repository ttl", "", false, 24 * time.Hour, time.Second, true, true},
		{"metadata fresh", "", true, 9 * time.Second, 10 * time.Second, true, true},
		{"metadata exact expiry", "", true, 10 * time.Second, 10 * time.Second, false, true},
		{"zero ttl", "", true, 0, 0, false, true},
		{"clock rollback", "", true, -time.Second, time.Hour, false, true},
		{"unique header expiry", "max-age=5", false, 6 * time.Second, time.Hour, false, true},
		{"metadata stricter ttl", "max-age=100", true, 6 * time.Second, 5 * time.Second, false, true},
		{"metadata stricter header", "max-age=5", true, 6 * time.Second, time.Hour, false, true},
		{"no cache", "no-cache=\"ETag, Last-Modified\"", false, 0, time.Hour, false, false},
		{"must revalidate", "must-revalidate", true, 2 * time.Hour, time.Hour, false, false},
		{"no store", "private, no-store", false, 0, time.Hour, false, false},
		{"bad age", "max-age=-1", false, 0, time.Hour, false, false},
		{"overflow age", "max-age=99999999999999999999", false, 0, time.Hour, false, false},
		{"repeated age", "max-age=10, MAX-AGE=\"3\"", true, 5 * time.Second, time.Hour, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parseCacheRules(tc.header)
			if got := r.fresh(tc.mutable, now.Add(-tc.age), now, tc.ttl); got != tc.fresh {
				t.Fatalf("fresh=%v", got)
			}
			if r.permitsStale() != tc.stale {
				t.Fatal("fallback policy")
			}
		})
	}
	if (cacheRules{}).fresh(false, time.Time{}, now, time.Hour) {
		t.Fatal("no successful validation")
	}
}

func TestVaryAndRetryBounds(t *testing.T) {
	for _, v := range []string{"", "Accept", "Accept-Encoding, Accept"} {
		if !cacheableVary(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"*", "Cookie", "Authorization", "Accept, X-Tenant"} {
		if cacheableVary(v) {
			t.Fatal(v)
		}
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", time.Second},
		{"-1", time.Second},
		{"0", time.Second},
		{"12", 12 * time.Second},
		{"999999", 300 * time.Second},
		{"bad", time.Second},
		{now.Add(20 * time.Second).Format(http.TimeFormat), 20 * time.Second},
		{now.Add(-time.Hour).Format(http.TimeFormat), time.Second},
	} {
		if got := retryAfter(tc.value, now); got != tc.want {
			t.Fatal(tc.value, got)
		}
	}
}
