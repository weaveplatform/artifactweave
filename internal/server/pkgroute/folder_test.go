package pkgroute_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RequireFolder lets through a caller who may read somewhere below the
// folder, as npm's packument asks it, and refuses anyone else as Require
// does: 401 to a caller without a user, 403 to one with a user.
func TestAccRequireFolder(t *testing.T) {
	f := newKitFixture(t, nil)
	p := pkgroute.Protocol{
		Name: "folders", Types: []repo.PackageType{repo.Generic}, Surface: apierror.SurfaceNPM,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			r.Get("/folder/*", func(w http.ResponseWriter, r *http.Request) {
				if k.RequireFolder(w, r, chi.URLParam(r, "*"), types.ActionRead) {
					w.WriteHeader(http.StatusNoContent)
				}
			})
		},
	}
	h := f.router(t, pkgroute.Options{Store: f.db}, p)
	for _, c := range []struct {
		path, user string
		expected   int
	}{
		{"work", "writer", http.StatusNoContent},
		{"a", "reader", http.StatusNoContent},
		{"a", "writer", http.StatusForbidden},
		{"a", "", http.StatusUnauthorized},
		{"a", "anonymous", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(
			http.MethodGet,
			"/artifactory/api/folders/"+local+"/folder/"+c.path,
			nil,
		)
		if c.user != "" {
			req.Header.Set("X-Test-User", c.user)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.expected {
			t.Errorf("%s as %q: %d, want %d: %s", c.path, c.user, rec.Code, c.expected, rec.Body)
		}
	}
}
