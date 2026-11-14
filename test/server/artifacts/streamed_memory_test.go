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

// The memory figure #1409 asks for: a deep file list of memoryItems files
// over a SQLite store, served through a real server.
const (
	memoryItems = 50000
	// memoryListBound is the most the heap may grow above its level before
	// the request while the list streams. Measured by this test on SQLite
	// (darwin/arm64, go1.27): 4.5 to 4.6 MiB streamed, where the collected
	// list it replaces grew the heap by 59.9 to 60.0 MiB for the same files.
	// The bound leaves room for runners whose allocator and GC timing
	// differ, and still fails a collected list of this size.
	memoryListBound = 16 << 20
)

// TestAccStreamedFileListMemory serves the deep file list of 50000 files
// and samples the heap while it does: the streamed list holds one store
// page per tree and its encoding, never the whole list, so the heap grows
// by less than memoryListBound however many files there are.
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
		for i := range memoryItems / 500 {
			if _, err := db.Items().Put(ctx, artmodel.Item{
				RepoKey: "big-local", Path: ".", Name: fmt.Sprintf("d%03d", i), Folder: true,
				Modified: modified,
			}); err != nil {
				return err //nolint:wrapcheck // test seeding
			}
			for j := range 500 {
				content := fmt.Sprintf("file %d/%d", i, j)
				if _, err := db.Items().Put(ctx, artmodel.Item{
					RepoKey: "big-local", Path: fmt.Sprintf("d%03d", i),
					Name: fmt.Sprintf("artifact-%04d.jar", j), Size: int64(len(content)),
					Modified: modified, Actual: kernel.Checksums{
						SHA1: sha1Hex(content), SHA256: sha256Hex(content), MD5: md5Hex(content),
					},
				}); err != nil {
					return err //nolint:wrapcheck // test seeding
				}
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
		srv.URL+"/artifactory/api/storage/big-local/?list&deep=1&listFolders=1&mdTimestamps=1", nil)
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
	n, err := countFiles(res)
	close(stop)
	<-sampled
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("%d after %d entries: %v", res.StatusCode, n, err)
	}
	if want := memoryItems + memoryItems/500; n != want {
		t.Fatalf("listed %d entries, want %d", n, want)
	}
	growth := peak - base
	t.Logf("peak heap growth listing %d entries: %d bytes (%.1f MiB)", n, growth,
		float64(growth)/(1<<20))
	if growth > memoryListBound {
		t.Fatalf("the heap grew by %d bytes, over the %d-byte bound", growth, memoryListBound)
	}
}

// countFiles counts the entries of a FileList's files, decoding one at a
// time.
func countFiles(res *http.Response) (int, error) {
	defer func() { _ = res.Body.Close() }()
	dec := json.NewDecoder(res.Body)
	n := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return n, err //nolint:wrapcheck // reported as it is
		}
		if tok != "files" {
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
