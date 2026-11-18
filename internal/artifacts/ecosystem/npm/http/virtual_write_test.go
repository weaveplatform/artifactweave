package npmhttp_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// A repository lookup fault must never become the expected missing-DDR400.
func TestAccVirtualDeploymentFaults(t *testing.T) {
	for _, tc := range []struct {
		name, target, fail string
		members            []string
	}{
		{"target lookup", "npm-local", "npm-local", []string{"npm-local"}},
		{"member lookup", "npm-local", "broken", []string{"broken", "npm-local"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, func(st pkgroute.Store) pkgroute.Store { return npmWriteFaultStore{Store: st, fail: tc.fail} })
			c := *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: tc.members, DefaultDeploymentRepo: tc.target})
			if err := f.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			r := f.send(http.MethodPut, "/artifactory/api/npm/npm-virtual/pkg", "admin", strings.NewReader("not json"))
			wantStatus(t, r, 500)
			if len(f.faults) != 1 || !errors.Is(f.faults[0], errBroken) {
				t.Fatal(f.faults)
			}
		})
	}
}

type npmWriteFaultStore struct {
	pkgroute.Store
	fail string
}

func (s npmWriteFaultStore) Repos() port.RepoStore {
	return npmWriteFaultRepos{RepoStore: s.Store.Repos(), fail: s.fail}
}

type npmWriteFaultRepos struct {
	port.RepoStore
	fail string
}

func (s npmWriteFaultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == s.fail {
		return repo.Config{}, errBroken
	}
	return s.RepoStore.Get(ctx, key)
}

func TestAccVirtualDeploymentLocalRefusals(t *testing.T) {
	f := newFixture(t)
	c := *repo.Defaults(&repo.Config{Key: "npm-virtual", Class: repo.Virtual, PackageType: repo.NPM, Repositories: []string{"npm-local"}, DefaultDeploymentRepo: "npm-local"})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	// Hiding belongs to the original virtual, never to its target.
	local, err := f.db.Repos().Get(t.Context(), "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	local.HideUnauthorizedResources = new(true)
	if err = f.db.Repos().Update(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"", "anonymous", "reader"} {
		t.Run("user-"+user, func(t *testing.T) {
			for _, tc := range []struct{ method, path, body string }{{"PUT", "pkg", "not json"}, {"PUT", "-/package/pkg/dist-tags/next", `"1.0.0"`}, {"DELETE", "pkg/-rev/1-0", ""}} {
				body := func(key string) string { return "/artifactory/api/npm/" + key + "/" + tc.path }
				direct := f.send(tc.method, body("npm-local"), user, strings.NewReader(tc.body))
				mapped := f.send(tc.method, body("npm-virtual"), user, strings.NewReader(tc.body))
				if direct.Code != mapped.Code || direct.Body.String() != mapped.Body.String() ||
					direct.Header().Get("WWW-Authenticate") != mapped.Header().Get("WWW-Authenticate") {
					t.Fatal(direct.Code, direct.Body.String(), direct.Header(), mapped.Code, mapped.Body.String(), mapped.Header())
				}
			}
		})
	}
}
