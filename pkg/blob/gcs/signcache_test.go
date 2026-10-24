package gcs

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
)

// signCalls counts the signatures the fake has made, each standing for one
// IAM signBlob request.
func signCalls(f *fakeGCS) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls["SignedURL"]
}

// expiresIn reads the fake URL's expiry and returns how long it has left.
func expiresIn(t *testing.T, raw string) time.Duration {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := strconv.ParseInt(u.Query().Get("X-Fake-Expires"), 10, 64)
	if err != nil {
		t.Fatalf("expiry in %q: %v", raw, err)
	}
	return time.Unix(sec, 0).Sub(time.Now())
}

func networkStore(t *testing.T, f *fakeGCS, overNetwork bool) *Store {
	t.Helper()
	s, err := New(f, Options{
		Prefix: "art", Sessions: session.NewMemory(), Presign: true,
		SignsOverNetwork: overNetwork,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestAccSignedURLCacheOverNetwork: a store that signs through IAM signBlob
// makes one signature per blob, overrides and window, and every URL it hands
// out has at least the TTL left (decision 0029).
func TestAccSignedURLCacheOverNetwork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		const ttl = 5 * time.Minute
		f := newFake()
		s := networkStore(t, f, true)
		sha := strings.Repeat("ab", 32)
		sign := func(o blob.SignOptions) string {
			t.Helper()
			o.TTL = ttl
			u, err := s.SignURL(ctx, sha, o)
			if err != nil {
				t.Fatal(err)
			}
			if left := expiresIn(t, u); left < ttl || left > 2*ttl {
				t.Fatalf("URL has %s left; want between the TTL and twice it", left)
			}
			return u
		}

		first := sign(blob.SignOptions{Filename: "a.tgz", ContentType: "application/gzip"})
		for range 4 {
			if again := sign(
				blob.SignOptions{Filename: "a.tgz", ContentType: "application/gzip"},
			); again != first {
				t.Fatalf("same window, new URL: %q then %q", first, again)
			}
		}
		if n := signCalls(f); n != 1 {
			t.Fatalf("%d signatures for one blob in one window, want 1", n)
		}
		// Each override set is its own URL.
		if sign(blob.SignOptions{Filename: "b.tgz", ContentType: "application/gzip"}) == first ||
			sign(blob.SignOptions{Filename: "a.tgz"}) == first {
			t.Fatal("other overrides reused the first URL")
		}
		if n := signCalls(f); n != 3 {
			t.Fatalf("%d signatures, want 3", n)
		}

		// Late in the window the cached URL still has more than the TTL left.
		time.Sleep(ttl - time.Second)
		if sign(blob.SignOptions{Filename: "a.tgz", ContentType: "application/gzip"}) != first {
			t.Fatal("still the first window, but a new URL")
		}
		// The next window signs afresh.
		time.Sleep(time.Second)
		next := sign(blob.SignOptions{Filename: "a.tgz", ContentType: "application/gzip"})
		if next == first || signCalls(f) != 4 {
			t.Fatalf("next window reused %q (%d signatures)", next, signCalls(f))
		}
		// Across many windows, never less than the TTL left.
		for range 100 {
			time.Sleep(7 * time.Second)
			sign(blob.SignOptions{Filename: "a.tgz", ContentType: "application/gzip"})
		}

		// A failed signature is not cached; the next request signs. failOn
		// restarts the fake's count.
		time.Sleep(ttl)
		f.failOn("SignedURL", 0, errInjected)
		if _, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: ttl}); !errors.Is(err, errInjected) {
			t.Fatalf("signing failure: %v", err)
		}
		f.failOn("SignedURL", 1<<20, nil)
		sign(blob.SignOptions{})
		sign(blob.SignOptions{})
		if n := signCalls(f); n != 1 {
			t.Fatalf("%d signatures after a failure, want 1", n)
		}
	})
}

// TestAccSignedURLUncached: local signers, TTLs over half the V4 limit (a
// window URL lives up to twice the TTL) and sub-second TTLs sign afresh,
// expiring the TTL from now.
func TestAccSignedURLUncached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		sha := strings.Repeat("cd", 32)
		for _, c := range []struct {
			name        string
			overNetwork bool
			ttl         time.Duration
		}{
			{"local signer", false, 5 * time.Minute},
			{"over half the V4 limit", true, 3*24*time.Hour + 13*time.Hour},
			{"under a second", true, 500 * time.Millisecond},
		} {
			f := newFake()
			s := networkStore(t, f, c.overNetwork)
			for range 3 {
				u, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: c.ttl})
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				// The fake keeps whole seconds, as V4 does.
				if left := expiresIn(t, u); left > c.ttl || left < c.ttl-time.Second {
					t.Fatalf("%s: %s left, want the TTL %s", c.name, left, c.ttl)
				}
			}
			if n := signCalls(f); n != 3 {
				t.Fatalf("%s: %d signatures, want 3", c.name, n)
			}
		}
	})
}

// TestAccSignedURLCacheEvicts: the cache holds at most signedURLCacheSize URLs
// and drops the least recently used.
func TestAccSignedURLCacheEvicts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		swap(t, &signedURLCacheSize, 3)
		ctx := context.Background()
		f := newFake()
		s := networkStore(t, f, true)
		shas := []string{
			strings.Repeat("01", 32), strings.Repeat("02", 32),
			strings.Repeat("03", 32), strings.Repeat("04", 32),
		}
		for _, sha := range shas {
			if _, err := s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute}); err != nil {
				t.Fatal(err)
			}
		}
		if n := s.signed.len(); n != 3 {
			t.Fatalf("cache holds %d URLs, want 3", n)
		}
		calls := signCalls(f)
		// The most recent is cached; the oldest was evicted and signs again.
		if _, err := s.SignURL(ctx, shas[3], blob.SignOptions{TTL: time.Minute}); err != nil {
			t.Fatal(err)
		}
		if signCalls(f) != calls {
			t.Fatal("the most recent URL was not cached")
		}
		if _, err := s.SignURL(ctx, shas[0], blob.SignOptions{TTL: time.Minute}); err != nil {
			t.Fatal(err)
		}
		if signCalls(f) != calls+1 || s.signed.len() != 3 {
			t.Fatalf("evicted URL: %d signatures, %d cached", signCalls(f)-calls, s.signed.len())
		}
	})
}

func TestUnitURLCachePutReplaces(t *testing.T) {
	c := newURLCache(2)
	k := urlKey{sha: "a", expires: 1}
	c.put(k, "one")
	c.put(k, "two")
	if u, ok := c.get(k); !ok || u != "two" || c.len() != 1 {
		t.Fatalf("get = %q, %v, len %d", u, ok, c.len())
	}
	if _, ok := c.get(urlKey{sha: "b"}); ok {
		t.Fatal("missing key found")
	}
}
