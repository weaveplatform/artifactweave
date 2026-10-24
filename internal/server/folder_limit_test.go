package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// gatedBlobs holds every Open while armed, reporting it on opened, until
// release is closed: a folder download then stays running for as long as a
// test needs it to.
type gatedBlobs struct {
	blob.Store
	armed   atomic.Bool
	opened  chan struct{}
	release chan struct{}
}

func (g *gatedBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if g.armed.Load() {
		g.opened <- struct{}{}
		<-g.release
	}
	return g.Store.Open(ctx, sha) //nolint:wrapcheck // a pass-through store
}

// Folder downloads beyond the most that may run at once are refused with
// 400 until one finishes: 7.x's "Max Parallel Folder Downloads", 10 by
// default, and 6.20's refusal (FolderDownloadServiceImpl).
func TestAccFolderDownloadLimit(t *testing.T) {
	for _, c := range []struct {
		name  string
		limit int
		runs  int
	}{
		{"default", 0, 10},
		{"configured", 2, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			gate := &gatedBlobs{opened: make(chan struct{}), release: make(chan struct{})}
			w := newItemWorld(t, false, func(cfg *Config) {
				gate.Store = cfg.Blobs
				cfg.Blobs = gate
				cfg.FolderDownloadMaxConcurrent = c.limit
			})
			w.put("/artifactory/libs-local/d/a.txt", "a")
			if err := w.db.Settings().Put(context.Background(), types.SettingFolderDownload,
				types.FolderDownloadSettings{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			const archive = "/artifactory/api/archive/download/libs-local/d?archiveType=zip"

			gate.armed.Store(true)
			var wg sync.WaitGroup
			running := make([]*httptest.ResponseRecorder, c.runs)
			for i := range c.runs {
				wg.Go(func() { running[i] = w.do(http.MethodGet, archive, "admin", "") })
				<-gate.opened
			}
			rec := w.do(http.MethodGet, archive, "admin", "")
			wantStatus(t, rec, http.StatusBadRequest)
			if !strings.Contains(rec.Body.String(),
				"There are too many folder download requests currently running. Try again later.") {
				t.Fatalf("refusal %s", rec.Body)
			}
			// A refusal for other reasons comes first: the limit is checked
			// last, as Artifactory checks it.
			wantStatus(t, w.do(http.MethodGet, archive, "user", ""), http.StatusForbidden)

			gate.armed.Store(false)
			close(gate.release)
			wg.Wait()
			for i, r := range running {
				if r.Code != http.StatusOK {
					t.Fatalf("download %d: %d %s", i, r.Code, r.Body)
				}
			}
			// Finished downloads free their places.
			wantStatus(t, w.do(http.MethodGet, archive, "admin", ""), http.StatusOK)
		})
	}
}
