package artifacts_test

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

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

// The memory figures #1409 asks for: a deep file list of memoryItems files
// and the folder information of a folder holding them all, over a SQLite
// store, served through a real server.
const (
	memoryItems = 50000
	// memoryListBound is the most the heap may grow above its level before
	// the request while a list streams. Measured by this test on SQLite
	// (darwin/arm64, go1.27): the deep file list grew it by 4.0 to 4.5 MiB
	// streamed, where the collected list it replaces grew it by 59.8 MiB;
	// folder information by 4.0 to 4.2 MiB streamed, where the collected
	// children grew it by 42.3 MiB. The bound leaves room for runners whose
	// allocator and GC timing differ, and still fails a collected list of
	// this size.
	memoryListBound = 16 << 20
)

// TestAccStreamedFileListMemory serves the deep file list of 50000 files,
// and the folder information of the folder that holds them, and samples
// the heap while it does: each streamed list holds one store page per tree
// and its encoding, never the whole list, so the heap grows by less than
// memoryListBound however many files there are.
func TestAccStreamedFileListMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 50000 files")
	}
	ctx := t.Context()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "memory.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cheapSeed(t, db)
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "big-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	if err := db.Tx(ctx, func(ctx context.Context) error {
		if _, err := db.Items().Put(ctx, artmodel.Item{
			RepoKey: "big-local", Path: ".", Name: "flat", Folder: true, Modified: modified,
		}); err != nil {
			return err //nolint:wrapcheck // test seeding
		}
		for i := range memoryItems {
			content := fmt.Sprintf("file %d", i)
			if _, err := db.Items().Put(ctx, artmodel.Item{
				RepoKey: "big-local", Path: "flat", Name: fmt.Sprintf("artifact-%05d.jar", i),
				Size: int64(len(content)), Modified: modified, Actual: kernel.Checksums{
					SHA1: sha1Hex(content), SHA256: sha256Hex(content), MD5: md5Hex(content),
				},
			}); err != nil {
				return err //nolint:wrapcheck // test seeding
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Config{
		BaseURL: "https://reg.example", Store: db, Blobs: blobs, Auth: a,
		Logger: slog.New(slog.DiscardHandler),
	}))
	defer srv.Close()
	for _, c := range []struct {
		what, target, member string
		want                 int
	}{
		{"deep file list", "?list&deep=1&listFolders=1&mdTimestamps=1", "files", memoryItems + 1},
		{"folder information", "flat", "children", memoryItems},
	} {
		n, growth := streamedGrowth(
			t,
			srv,
			"/artifactory/api/storage/big-local/"+c.target,
			c.member,
		)
		if n != c.want {
			t.Fatalf("%s listed %d entries, want %d", c.what, n, c.want)
		}
		t.Logf("peak heap growth for a %s of %d entries: %d bytes (%.1f MiB)", c.what, n, growth,
			float64(growth)/(1<<20))
		if growth > memoryListBound {
			t.Fatalf("the %s grew the heap by %d bytes, over the %d-byte bound",
				c.what, growth, memoryListBound)
		}
	}
}

// streamedGrowth gets target as admin, counting the entries of the
// answer's member list as it decodes them, and returns them and how far
// the heap grew above its level before the request while it was served.
func streamedGrowth(t *testing.T, srv *httptest.Server, target, member string) (int, uint64) {
	t.Helper()
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "pw")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// The client decodes one entry at a time, so it holds no more than the
	// server does.
	n, err := countMember(res, member)
	close(stop)
	<-sampled
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d after %d entries: %v", target, res.StatusCode, n, err)
	}
	return n, peak - base
}

// countMember counts the entries of the answer's member list, decoding
// one at a time.
func countMember(res *http.Response, member string) (int, error) {
	defer func() { _ = res.Body.Close() }()
	dec := json.NewDecoder(res.Body)
	n := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return n, err //nolint:wrapcheck // reported as it is
		}
		if tok != member {
			continue
		}
		if _, err := dec.Token(); err != nil {
			return n, err //nolint:wrapcheck // reported as it is
		}
		for dec.More() {
			var e struct {
				URI string `json:"uri"`
			}
			if err := dec.Decode(&e); err != nil {
				return n, err //nolint:wrapcheck // reported as it is
			}
			n++
		}
		return n, nil
	}
}
