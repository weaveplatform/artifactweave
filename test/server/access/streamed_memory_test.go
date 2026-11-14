package access_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// The memory figure #1409 asks for: the legacy users list over a SQLite
// store of memoryUsers users, served through a real server.
const (
	memoryUsers = 50000
	// memoryBound is the most the heap may grow above its level before the
	// request while the list streams. Measured by this test on SQLite
	// (darwin/arm64, go1.27): 1.7 to 2.1 MiB streamed, where the collected
	// handler it replaces grew the heap by 28.6 to 29.2 MiB for the same
	// list. The bound leaves room for runners whose allocator and GC timing
	// differ, and still fails a collected list of this size.
	memoryBound = 12 << 20
)

// TestAccStreamedUsersListMemory serves 50000 users and samples the heap
// while it does: the streamed list holds one store page and its encoding,
// never the whole list, so the heap grows by less than memoryBound however
// many users there are.
func TestAccStreamedUsersListMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 50000 users")
	}
	ctx := t.Context()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "memory.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	support.Seed(t, db)
	if err := db.Tx(ctx, func(ctx context.Context) error {
		for i := range memoryUsers {
			if err := db.Users().Create(ctx, accesstest.User{
				Name: fmt.Sprintf("user-%05d", i), Email: fmt.Sprintf("user-%05d@example.org", i),
				PasswordHash: cheapHash,
			}); err != nil {
				return err //nolint:wrapcheck // test seeding
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Config{
		CompatVersion: "7.155.0", Store: db, Auth: a, Logger: slog.New(slog.DiscardHandler),
	}))
	defer srv.Close()

	// A low GC target keeps the heap near what is live, so the samples
	// measure what the handler holds rather than when the collector ran.
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base, peak := ms.HeapAlloc, ms.HeapAlloc
	stop, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/artifactory/api/security/users", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "pw")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// The client decodes one element at a time, so it holds no more than
	// the server does.
	n, err := countElements(res)
	close(stop)
	<-sampled
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%d after %d elements: %v", res.StatusCode, n, err)
	}
	// The seed's built-in users are listed too.
	if n < memoryUsers {
		t.Fatalf("listed %d users, want at least %d", n, memoryUsers)
	}
	growth := peak - base
	t.Logf("peak heap growth serving %d users: %d bytes (%.1f MiB)", n, growth,
		float64(growth)/(1<<20))
	if growth > memoryBound {
		t.Fatalf("the heap grew by %d bytes, over the %d-byte bound", growth, memoryBound)
	}
}

func countElements(res *http.Response) (int, error) {
	defer func() { _ = res.Body.Close() }()
	dec := json.NewDecoder(res.Body)
	if _, err := dec.Token(); err != nil {
		return 0, err //nolint:wrapcheck // reported as it is
	}
	n := 0
	for dec.More() {
		var v struct {
			Name string `json:"name"`
		}
		if err := dec.Decode(&v); err != nil {
			return n, err //nolint:wrapcheck // reported as it is
		}
		n++
	}
	_, err := dec.Token()
	return n, err //nolint:wrapcheck // reported as it is
}
