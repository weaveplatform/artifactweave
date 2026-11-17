package itemkit_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// membersOnly is an Access that may read the repositories keys and nothing
// else.
func membersOnly(keys ...string) itemkit.Access {
	in := func(_ accessapi.ResourceType, r accessapi.Repo, _ string, _ accessapi.Actions) bool {
		return slices.Contains(keys, r.Key)
	}
	return itemkit.Access{Decider: &accessapitest.Decider{
		AllowsFunc: in, AllowsFolderFunc: in, AllowsAllFunc: in,
	}}
}

// TestUnitVirtualReadsByTheMembers: a read through a virtual repository
// passes for a caller who may read the path in the virtual repository
// itself or in a member the walk reaches, nested ones included, and not
// for one who may read neither; VirtualGate lets the first on and refuses
// the second, and a member lookup that fails is an error, which it answers
// 500, never a refusal.
func TestUnitVirtualReadsByTheMembers(t *testing.T) {
	rs := virtual.Configs{
		"local": {Key: "local", Class: repo.Local},
		"inner": {Key: "inner", Class: repo.Virtual, Repositories: []string{"local"}},
		"v":     {Key: "v", Class: repo.Virtual, Repositories: []string{"inner"}},
	}
	for _, tc := range []struct {
		name string
		acc  itemkit.Access
		want bool
	}{
		{"the virtual repository's reader", membersOnly("v"), true},
		{"a nested member's reader", membersOnly("local"), true},
		{"a reader of neither", membersOnly("other"), false},
	} {
		got, err := tc.acc.VirtualReads(t.Context(), rs, rs["v"], "a/b.txt")
		if err != nil || got != tc.want {
			t.Errorf("%s: reads %t, %v, want %t", tc.name, got, err, tc.want)
		}
	}
	w := kittest.New(t, inmem.New())
	broken := failingRepos{w.Store}
	if _, err := membersOnly().VirtualReads(t.Context(), broken.Repos(), rs["v"], "a"); err == nil {
		t.Error("a failing member lookup was taken for a refusal")
	}

	k := w.Kit(itemkit.Deps{})
	stored := repo.Config{Key: "broken", Class: repo.Virtual, Repositories: []string{"absent"}}
	rec := httptest.NewRecorder()
	if k.VirtualGate(rec, kittest.Request(t, http.MethodGet, "/"), broken, stored,
		"a", kernel.Principal{Name: "u"}, membersOnly(), "op") || rec.Code != http.StatusInternalServerError {
		t.Errorf("a failing member lookup answered %d", rec.Code)
	}
	plain := repo.Config{Key: "v", Class: repo.Virtual}
	for _, tc := range []struct {
		acc    itemkit.Access
		allows bool
		status int
	}{
		{membersOnly("v"), true, http.StatusOK},
		{membersOnly(), false, http.StatusForbidden},
	} {
		rec := httptest.NewRecorder()
		got := k.VirtualGate(rec, kittest.Request(t, http.MethodGet, "/"), w.Store, plain, "a",
			kernel.Principal{Name: "u"}, tc.acc, "op")
		if got != tc.allows || rec.Code != tc.status {
			t.Errorf("gate passed %t answering %d, want %t and %d", got, rec.Code, tc.allows, tc.status)
		}
	}
}

// TestUnitVirtualRefusedHidesWhenAsked: a refused read through a virtual
// repository answers 403, or the 401 challenge to an anonymous caller, and
// 404 "Resource not found" while the repository hides unauthorized
// resources (decision 0008, R4).
func TestUnitVirtualRefusedHidesWhenAsked(t *testing.T) {
	plain := repo.Config{Key: "v", Class: repo.Virtual}
	hidden := plain
	hidden.HideUnauthorizedResources = new(true)
	for _, tc := range []struct {
		c       repo.Config
		caller  kernel.Principal
		status  int
		message string
	}{
		{plain, kernel.Principal{Name: "u"}, http.StatusForbidden, "Forbidden"},
		{plain, kernel.Principal{Name: "anonymous", Anonymous: true}, http.StatusUnauthorized, "Authentication is required"},
		{hidden, kernel.Principal{Name: "u"}, http.StatusNotFound, "Resource not found; Path: 'v:a/b'"},
		{hidden, kernel.Principal{Name: "anonymous", Anonymous: true}, http.StatusNotFound, "Resource not found; Path: 'v:a/b'"},
	} {
		var got *apierror.Error
		if !errors.As(itemkit.VirtualRefused(tc.c, "a/b", tc.caller), &got) ||
			got.Status != tc.status || got.Message != tc.message {
			t.Errorf("hidden %t as %s: %v, want %d %q", tc.c.HideUnauthorizedResources != nil,
				tc.caller.Name, got, tc.status, tc.message)
		}
	}
}

// TestUnitPackagesNeedsTheRouterKitsStore: an overlay's router kit is
// built over PackageOptions, and a kit built without their store has none.
func TestUnitPackagesNeedsTheRouterKitsStore(t *testing.T) {
	w := kittest.New(t, inmem.New())
	p := pkgroute.Protocol{Name: "t", Types: []repo.PackageType{repo.Generic}}
	if _, err := w.Kit(itemkit.Deps{}).Packages(p, true); err == nil {
		t.Error("a kit without the router kit's store built one")
	}
	k := w.Kit(itemkit.Deps{PackageOptions: pkgroute.Options{Store: w.Store}})
	for _, keep := range []bool{true, false} {
		if got, err := k.Packages(p, keep); err != nil || got == nil {
			t.Errorf("keep %t: %v, %v", keep, got, err)
		}
	}
}
