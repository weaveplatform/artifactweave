package debianhttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// grantAllButTheIndexes gives the caller, the user dev or, when anonymous is
// set, the anonymous user with anonymous access on, a real permission
// target: Read, Write and Manage on deb-target, and on deb-source but for
// dists/stable/main/**, where the stable distribution's indexes lie, so the
// caller may read its Release and nothing it declares. It returns the
// caller as the server authenticates it.
func grantAllButTheIndexes(t *testing.T, w *itemWorld, anonymous bool) kernel.Principal {
	t.Helper()
	ctx := t.Context()
	cheapSeed(t, w.db)
	name := "dev"
	if anonymous {
		name = kernel.AnonymousUser
		if err := w.db.Settings().Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	} else if err := w.db.Users().Create(ctx,
		accesstest.User{Name: name, PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(ctx, accesstest.Permission{
		Name: "all-but-the-indexes",
		Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{
				name: accessapi.ActionRead | accessapi.ActionWrite | accessapi.ActionManage,
			}},
			Targets: map[string]accesstest.Target{
				snapshotJob.Source: {
					Include: []string{"**"}, Exclude: []string{"dists/stable/main/**"},
				},
				snapshotJob.Target: {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if anonymous {
		return kernel.Anonymous
	}
	p, err := accesstest.Over(t, w.db).API().Authenticator.Basic(ctx, name, "pw")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// wantNoSnapshot fails the test when snapshotJob's target holds the
// snapshot's Release or a marker that records its source, which serves the
// tag; the claim a failed request leaves for its retry is not a snapshot.
func wantNoSnapshot(t *testing.T, w *itemWorld) {
	t.Helper()
	if _, err := w.db.Items().Get(t.Context(), snapshotJob.Target, false,
		snapshotMarker+"/"+debRelease); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("refused snapshot copied the Release: %v", err)
	}
	marker, err := w.db.Items().Get(t.Context(), snapshotJob.Target, false, snapshotMarker)
	if errors.Is(err, kernel.ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	props, err := w.db.Items().Properties(t.Context(), marker.ID)
	if err != nil || len(props["deb.snapshot.source"]) != 0 {
		t.Errorf("refused snapshot recorded its source: %v (%v)", props, err)
	}
}

// TestAccDebianSnapshotAnswersAPermissionDenial: a caller who may read the
// source's Release, which the snapshot checks first, but not an index the
// Release declares is refused as that first check refuses: 403 "Forbidden",
// or 401 "Authentication is required" to an anonymous caller, whether the
// snapshot runs at once or through the work queue, and the target gets no
// snapshot. Read permission is settled before the download policy (DV-057),
// so patterns that also reject the index do not turn the refusal into the
// policy's 404.
func TestAccDebianSnapshotAnswersAPermissionDenial(t *testing.T) {
	const (
		url  = "/artifactory/api/deb/deb-source/snapshot"
		body = `{"tag":"first","targetRepo":"deb-target","distribution":"stable"}`
	)
	for _, tc := range []struct {
		name      string
		queued    bool
		anonymous bool
		rejected  bool
		status    int
		message   string
	}{
		{name: "at once", status: http.StatusForbidden, message: "Forbidden"},
		{
			name: "through the work queue", queued: true,
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name: "anonymous caller at once", anonymous: true,
			status: http.StatusUnauthorized, message: "Authentication is required",
		},
		{
			name: "anonymous caller through the work queue", queued: true, anonymous: true,
			status: http.StatusUnauthorized, message: "Authentication is required",
		},
		{
			name: "index the patterns also reject", rejected: true,
			status: http.StatusForbidden, message: "Forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := snapshotWorld(t)
			if tc.rejected {
				setRepo(t, w, snapshotJob.Source, func(c *repo.Config) {
					c.ExcludesPattern = new("**/Packages")
				})
			}
			caller := grantAllButTheIndexes(t, w, tc.anonymous)
			access := accesstest.Over(t, w.db).API()
			deps := itemkit.Deps{Store: w.db, Blobs: w.blobs, Access: access}
			if tc.queued {
				q, err := work.New(w.db, work.Options{})
				if err != nil {
					t.Fatal(err)
				}
				deps.Work = q
				if err := RegisterDebianSnapshotWork(q, itemkit.New(deps)); err != nil {
					t.Fatal(err)
				}
			}
			p, d, err := access.Authorizer.Authorize(t.Context(), caller, caller.Anonymous)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(accessapi.WithCaller(t.Context(), p, d),
				http.MethodPost, url, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			strictHandler(&DebianAPI{itemkit.New(deps)}).ServeHTTP(rec, r)
			wantError(t, rec, tc.status, tc.message)
			wantNoSnapshot(t, w)
		})
	}
}
