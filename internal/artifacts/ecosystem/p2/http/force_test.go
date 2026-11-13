package p2http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestUnitP2ForcedAuthentication: an anonymous request to a P2 virtual
// repository with forceP2Authentication on answers 401 "Unauthorized" with
// the Basic challenge, even with a decider that would let it read
// everything, and before anything is read: the server has no remote source
// service, so a read of the remote member that got past the check would
// answer 500. A remote's flag changes nothing, and neither does a nested
// virtual repository's when it is read through an outer one whose flag is
// off; a signed-in caller gets past the check too.
func TestUnitP2ForcedAuthentication(t *testing.T) {
	w := kittest.New(t, inmem.New())
	kit := w.Kit(itemkit.Deps{
		Logger:   slog.New(slog.DiscardHandler),
		Overlays: []itemkit.Overlay{TryP2Item},
	})
	on := true
	for _, c := range []repo.Config{
		{
			Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://site.example/updates/", ForceP2Authentication: &on,
		},
		{
			Key: "p2-forced", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-remote"}, ForceP2Authentication: &on,
		},
		{
			Key: "p2-open", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-forced"},
		},
	} {
		if err := w.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	anonymous := func(method, target string) *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), method, target, nil)
		return r.WithContext(accessapi.WithCaller(r.Context(), kernel.Anonymous,
			&accessapitest.Decider{Admin: true}))
	}
	child := "__p2_ext/" + strings.Repeat("a", 64) + "/content.jar"
	for _, tc := range []struct {
		name string
		r    *http.Request
		code int
	}{
		{"virtual index", anonymous(http.MethodGet, "/artifactory/p2-forced/p2.index"), 401},
		{"virtual composite", anonymous(http.MethodHead, "/artifactory/p2-forced/compositeContent.xml"), 401},
		{"virtual folder", anonymous(http.MethodGet, "/artifactory/p2-forced/plugins/"), 401},
		{"virtual unclassed path", anonymous(http.MethodGet, "/artifactory/p2-forced/unknown.txt"), 401},
		{
			"virtual member",
			anonymous(http.MethodGet, "/artifactory/p2-forced/p2-remote/compositeContent.xml"),
			401,
		},
		{"remote composite", anonymous(http.MethodGet, "/artifactory/p2-remote/compositeContent.xml"), 500},
		{"remote child", anonymous(http.MethodGet, "/artifactory/p2-remote/"+child), 500},
		{
			"nested virtual through an open one",
			anonymous(http.MethodGet, "/artifactory/p2-open/p2-forced/p2-remote/compositeContent.xml"),
			500,
		},
		{
			"signed in",
			kittest.Request(t, http.MethodGet, "/artifactory/p2-forced/p2-remote/compositeContent.xml"),
			500,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			kit.GetItem(rec, tc.r)
			if tc.code == http.StatusUnauthorized {
				wantError(t, rec, http.StatusUnauthorized, "Unauthorized")
				if got := rec.Header().Get("WWW-Authenticate"); got != apierror.BasicChallenge {
					t.Errorf("challenge %q, want %q", got, apierror.BasicChallenge)
				}
				return
			}
			wantError(t, rec, tc.code, "internal error")
		})
	}
}
