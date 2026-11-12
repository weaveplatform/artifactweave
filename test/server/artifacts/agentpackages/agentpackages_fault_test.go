package agentpackages_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/agentcommon"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

var errStore = errors.New("test store unavailable")

// faultyStore fails the store read that fail names: "item", an item lookup;
// "package", a package lookup; "versions", a package's version list. With
// "metadata" it lists the versions with index metadata that names no
// digest.
type faultyStore struct {
	harness.Store
	fail string
}

func (s faultyStore) Items() artport.ItemStore { return faultyItems{s.Store.Items(), s.fail} }

func (s faultyStore) Packages() artport.PackageStore {
	return faultyPackages{s.Store.Packages(), s.fail}
}

type faultyItems struct {
	artport.ItemStore
	fail string
}

func (s faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (artmodel.Item, error) {
	if s.fail == "item" {
		return artmodel.Item{}, errStore
	}
	return s.ItemStore.Get(ctx, key, cached, path)
}

type faultyPackages struct {
	artport.PackageStore
	fail string
}

func (s faultyPackages) Find(ctx context.Context, key, name string) (artmodel.Package, error) {
	if s.fail == "package" {
		return artmodel.Package{}, errStore
	}
	return s.PackageStore.Find(ctx, key, name)
}

func (s faultyPackages) Versions(ctx context.Context, id int64) ([]artmodel.PackageVersion, error) {
	if s.fail == "versions" {
		return nil, errStore
	}
	all, err := s.PackageStore.Versions(ctx, id)
	if s.fail == "metadata" {
		for i := range all {
			all[i].Metadata = []byte("{}")
		}
	}
	return all, err
}

// A store fault answers 500 in the problem JSON APM expects, with no
// Cache-Control: a version's archive is marked immutable for a day, and its
// version list cacheable for a minute, only when they are served, since a
// client or proxy that kept a fault would not ask again. A stored version
// whose index metadata cannot be read fails the list rather than dropping
// out of it.
func TestAccAgentPackagesStoreFaults(t *testing.T) {
	w := agentcommon.NewWorld(t)
	c := repo.Config{Key: "apm-local", Class: repo.Local, PackageType: repo.AgentPackages}
	if err := w.DB.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	versions := "/artifactory/api/agentpackages/apm-local/v1/packages/acme/my-pkg/versions"
	wantStatus(t, w.Do(http.MethodPut, versions+"/1.0.0", "admin",
		packageZIP(t, "my-pkg", "1.0.0"), "Content-Type", "application/zip"), http.StatusCreated)
	a, err := authn.NewAuthenticator(w.DB.Users(), w.DB.Settings(),
		authn.Options{Hash: support.TestHash})
	if err != nil {
		t.Fatal(err)
	}
	const problem = `{"detail":"internal error","status":500,"title":"Internal Server Error"}` + "\n"
	for _, tc := range []struct{ fail, path string }{
		{"item", versions + "/1.0.0/download"},
		{"package", versions},
		{"versions", versions},
		{"metadata", versions},
	} {
		h := harness.Router(harness.Config{
			BaseURL: "https://reg.example", CompatVersion: "7.155.0",
			Store: faultyStore{Store: w.DB, fail: tc.fail}, Blobs: w.Blobs, Auth: a,
		})
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := support.Do(t, h, method, tc.path, support.Basic("admin", "pw"), "")
			wantStatus(t, rec, http.StatusInternalServerError)
			if rec.Header().Get("Content-Type") != "application/problem+json; charset=utf-8" ||
				rec.Header().Get("Cache-Control") != "" ||
				method == http.MethodGet && rec.Body.String() != problem {
				t.Fatalf("%s %s with a %s fault: %v %s", method, tc.path, tc.fail, rec.Header(),
					rec.Body)
			}
		}
	}
}
