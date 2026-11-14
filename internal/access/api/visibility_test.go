package api_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

// A set holds every repository, its classes' and its keys'; a visibility
// holds what all of its sets hold, so none at all shows everything.
func TestUnitRepoVisibilityHas(t *testing.T) {
	libs := api.Repo{Key: "libs-local", Class: "local"}
	ext := api.Repo{Key: "ext-remote", Class: "remote"}
	locals := api.RepoSet{Classes: []string{"local", "federated"}}
	named := api.RepoSet{Keys: []string{"ext-remote"}}
	for _, c := range []struct {
		name string
		v    api.RepoVisibility
		libs bool
		ext  bool
	}{
		{"no set", nil, true, true},
		{"every", api.RepoVisibility{{Every: true}}, true, true},
		{"empty set", api.RepoVisibility{{}}, false, false},
		{"class", api.RepoVisibility{locals}, true, false},
		{"key", api.RepoVisibility{named}, false, true},
		{"class or key", api.RepoVisibility{{Classes: []string{"local"}, Keys: []string{"ext-remote"}}}, true, true},
		{"class and key", api.RepoVisibility{locals, named}, false, false},
		{"every and key", api.RepoVisibility{{Every: true}, named}, false, true},
	} {
		if got := c.v.Has(libs); got != c.libs {
			t.Errorf("%s: Has(libs-local) = %v", c.name, got)
		}
		if got := c.v.Has(ext); got != c.ext {
			t.Errorf("%s: Has(ext-remote) = %v", c.name, got)
		}
	}
}
