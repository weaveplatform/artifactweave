package pkgroute_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestAccItemKitWalksTheRepositoryItBinds: a kit from ItemKit claims no
// route, and Bind hands its helpers a request for the repository an item
// route resolved, as Mount's routes are handed one. Through a virtual
// repository MergeMembers then walks the members the protocol has sources
// for, a cycle skipped and the remote, which this protocol has no source
// for, passed over; a repository that is not virtual is its own one step.
func TestAccItemKitWalksTheRepositoryItBinds(t *testing.T) {
	f, _ := walkFixture(t, nil)
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Virtual: pkgroute.Virtual{},
	}
	k := pkgroute.ItemKit(f.o, walkFormat(sources))
	all := func(api.ResourceType, api.Repo, string, api.Actions) bool { return true }
	d := &accessapitest.Decider{AllowsFunc: all, AllowsFolderFunc: all, AllowsAllFunc: all}
	r := httptest.NewRequestWithContext(
		api.WithCaller(t.Context(), kernel.Principal{Name: "root", Admin: true}, d),
		http.MethodGet, "/artifactory/outer/idx/versions", nil)
	for key, want := range map[string][]string{
		"outer":   {"local-a", "local-b"},
		"local-b": {"local-b"},
	} {
		c, err := f.db.Repos().Get(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		bound := k.Bind(r, c)
		if got := pkgroute.RequestFrom(bound).Repo.Key; got != key {
			t.Fatalf("bound request names %q, want %q", got, key)
		}
		members, err := k.MergeMembers(bound, "idx/versions")
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, mr := range members {
			keys = append(keys, pkgroute.RequestFrom(mr).Repo.Key)
		}
		if !slices.Equal(keys, want) {
			t.Errorf("%s walks %v, want %v", key, keys, want)
		}
	}
}
