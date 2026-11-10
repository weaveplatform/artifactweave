package debianhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
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
// properties answers 404 for a repository that does not exist, and, since
// the caller waits for it, the format's 400 for a stored package it cannot
// read, rather than reporting the rebuild accepted.
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
	var body struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != http.StatusBadRequest || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 ||
		!strings.HasPrefix(body.Errors[0].Message, "Invalid Debian package") {
		t.Errorf("unreadable package answered %d %s, want the format's 400", rec.Code, rec.Body)
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
