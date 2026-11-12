package debianhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// queueFault is a store whose work queue refuses every new item; every
// other call passes to Store.
type queueFault struct{ *inmem.Store }

func (s queueFault) WorkQueue() work.WorkQueue { return refusingQueue{s.Store.WorkQueue()} }

type refusingQueue struct{ work.WorkQueue }

func (refusingQueue) Enqueue(context.Context, string, string, []byte, time.Time) error {
	return errFault
}

// adminContext is ctx with kittest's administrator as the caller, as the
// router's authentication leaves an operation's context.
func adminContext(ctx context.Context) context.Context {
	return accessapi.WithCaller(ctx, kittest.Admin, kittest.AdminAccess().Decider)
}

// wantAPIError fails the test unless err is the API error with status and
// message that the strict handler would write.
func wantAPIError(t *testing.T, err error, status int, message string) {
	t.Helper()
	var api *apierror.Error
	if !errors.As(err, &api) || api.Status != status || api.Message != message {
		t.Errorf("returned %v, want %d %q", err, status, message)
	}
}

// TestAccDebianReindexWithoutPropertiesFailures: the rebuild that writes no
// properties answers 404 for a repository that does not exist. A stored
// package it cannot read is left out of the indexes rather than failing the
// rebuild the caller waits for: 7.x keeps such a package unindexed, and
// RTFACT-12513 (fixed in 5.0.0) says a package with bad metadata must not
// break indexing (#1355).
func TestAccDebianReindexWithoutPropertiesFailures(t *testing.T) {
	w := newItemWorld(t)
	w.debRepos(repo.Config{Key: "deb-local", Class: repo.Local})
	w.put("deb-local", debAlpha, false, []byte("not a deb"))
	rec := w.do(http.MethodPost, "/artifactory/api/deb/reindex/absent?writeProps=0", "admin", "")
	if rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "Repository 'absent' does not exist") {
		t.Errorf("absent repository answered %d %s, want 404", rec.Code, rec.Body)
	}
	rec = w.do(http.MethodPost, "/artifactory/api/deb/reindex/deb-local?writeProps=0", "admin", "")
	if rec.Code != http.StatusOK ||
		rec.Body.String() != "Debian repository deb-local metadata recalculation accepted" {
		t.Errorf("unreadable package answered %d %q, want 200", rec.Code, rec.Body)
	}
}

// TestUnitDebianReindexWithoutPropertiesQueueFault: a rebuild that cannot
// be queued is a server fault, never a rebuild reported as accepted.
func TestUnitDebianReindexWithoutPropertiesQueueFault(t *testing.T) {
	w := newItemWorld(t)
	w.debRepos(repo.Config{Key: "deb-local", Class: repo.Local})
	svc := DebianAPI{newKit(withAccess(t, Config{Store: queueFault{w.db}, Blobs: w.blobs}))}
	writeProps := artgen.CalculateDebianRepositoryMetadataParamsWritePropsN0
	_, err := svc.CalculateDebianRepositoryMetadata(adminContext(t.Context()),
		artgen.CalculateDebianRepositoryMetadataRequestObject{
			RepoKey: "deb-local",
			Params:  artgen.CalculateDebianRepositoryMetadataParams{WriteProps: &writeProps},
		})
	wantAPIError(t, err, http.StatusInternalServerError, "internal error")
}
