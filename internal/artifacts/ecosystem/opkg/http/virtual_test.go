package opkghttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// readsOnly is a decider that lets its user read the repositories keys,
// all of each, and nothing else: a grant on members alone, which no
// permission target on a virtual repository can widen (decision 0006).
func readsOnly(keys ...string) *accessapitest.Decider {
	in := func(_ accessapi.ResourceType, r accessapi.Repo, _ string, _ accessapi.Actions) bool {
		return slices.Contains(keys, r.Key)
	}
	return &accessapitest.Decider{AllowsFunc: in, AllowsFolderFunc: in, AllowsAllFunc: in}
}

// readAs serves GET on path in the repository key as the user name, who
// may read only the repositories keys; an empty name is the anonymous
// user.
func (w *itemWorld) readAs(key, path, name string, keys ...string) *httptest.ResponseRecorder {
	w.t.Helper()
	p := kernel.Principal{Name: name, Anonymous: name == ""}
	r := httptest.NewRequestWithContext(w.t.Context(), http.MethodGet, "/artifactory/"+key+"/"+path, nil)
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, readsOnly(keys...)))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
}

// failingSource is a remote's upstream that answers every read with err.
func failingSource(err error) func(*Config) {
	return withSource(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		return source.Result{}, err
	})
}

// TestAccOpkgVirtualWalksNestedMembersOnce: a nested virtual member's
// packages are listed and served, a virtual repository reached again,
// itself or through its member, is skipped rather than failing the feed
// (decision 0008, R2), and the feed is built on every read, since Opkg's
// virtual repositories carry no virtualRetrievalCachePeriodSecs.
func TestAccOpkgVirtualWalksNestedMembersOnce(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t)
	w.opkgRepos(
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{Key: "opkg-other", Class: repo.Local},
		repo.Config{Key: "opkg-inner", Class: repo.Virtual, Repositories: []string{"opkg-local", "opkg-virtual"}},
		repo.Config{
			Key: "opkg-virtual", Class: repo.Virtual,
			Repositories: []string{"opkg-virtual", "opkg-inner", "opkg-other"},
		},
	)
	w.put("opkg-local", opkgFeed, index)
	w.put("opkg-local", opkgXlat, ipk)
	all := []string{"feed/464xlat_13_x86_64.ipk", "feed/6in4_29_all.ipk"}
	if got := feedPaths(t, w.do(http.MethodGet, "/artifactory/opkg-virtual/"+opkgFeed, "admin", "")); !slices.Equal(got, all) {
		t.Errorf("feed lists %v, want %v", got, all)
	}
	if res := w.do(http.MethodGet, "/artifactory/opkg-virtual/"+opkgXlat, "admin", ""); res.Code != http.StatusOK {
		t.Errorf("package answered %d %s, want 200", res.Code, res.Body)
	}
	if _, err := w.db.Items().Delete(t.Context(), "opkg-local", false, opkgFeed); err != nil {
		t.Fatal(err)
	}
	if got := feedPaths(t, w.do(http.MethodGet, "/artifactory/opkg-virtual/"+opkgFeed, "admin", "")); len(got) != 0 {
		t.Errorf("the feed was kept: %v", got)
	}
}

// TestAccOpkgVirtualFeedPassesOverMembers: a member that cannot serve the
// feed is passed over and the feed lists the others' packages: one of
// another package type (R11), a remote whose upstream misses or fails,
// which 7.x passes over as any unfound resource (R3) [I], and a remote the
// caller may not read.
func TestAccOpkgVirtualFeedPassesOverMembers(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	for _, tc := range []struct {
		name   string
		tune   func(*Config)
		member string
		reader bool
	}{
		{name: "member of another package type", tune: func(*Config) {}, member: "generic-local"},
		{name: "remote whose upstream misses", tune: failingSource(kernel.ErrNotFound), member: "opkg-remote"},
		{name: "remote whose upstream fails", tune: failingSource(source.ErrUnavailable), member: "opkg-remote"},
		{name: "remote the caller may not read", tune: withSource(feedOf(t, index, ipk)), member: "opkg-remote", reader: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, tc.tune)
			w.opkgRepos(
				repo.Config{Key: "opkg-local", Class: repo.Local},
				repo.Config{Key: "opkg-remote", Class: repo.Remote},
				repo.Config{Key: "opkg-virtual", Class: repo.Virtual, Repositories: []string{tc.member, "opkg-local"}},
			)
			generic := repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic}
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&generic)); err != nil {
				t.Fatal(err)
			}
			w.put("generic-local", opkgFeed, index)
			w.put("opkg-local", opkgFeed, index)
			rec := w.do(http.MethodGet, "/artifactory/opkg-virtual/"+opkgFeed, "admin", "")
			if tc.reader {
				rec = w.readAs("opkg-virtual", opkgFeed, "reader", "opkg-local")
			}
			if got := feedPaths(t, rec); len(got) != 2 {
				t.Errorf("feed lists %v, want the local member's two packages", got)
			}
		})
	}
}

// TestAccOpkgVirtualReadsThroughItsMembers: a caller who may read only the
// members reads the feed and an IPK through the virtual repository
// (decision 0008, R3); a caller who may read no member is refused, 403 or
// the 401 challenge, or 404 "Resource not found" while the virtual
// repository hides unauthorized resources (R4).
func TestAccOpkgVirtualReadsThroughItsMembers(t *testing.T) {
	index, ipk := opkgUpstreamFixture(t)
	w := newItemWorld(t)
	w.opkgRepos(
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{Key: "opkg-virtual", Class: repo.Virtual, Repositories: []string{"opkg-local"}},
		repo.Config{
			Key: "opkg-hidden", Class: repo.Virtual, Repositories: []string{"opkg-local"},
			HideUnauthorizedResources: new(true),
		},
	)
	w.put("opkg-local", opkgFeed, index)
	w.put("opkg-local", opkgXlat, ipk)
	if got := feedPaths(t, w.readAs("opkg-virtual", opkgFeed, "member", "opkg-local")); len(got) != 2 {
		t.Errorf("member reader's feed lists %v, want two packages", got)
	}
	if res := w.readAs("opkg-virtual", opkgXlat, "member", "opkg-local"); res.Code != http.StatusOK {
		t.Errorf("member reader's IPK answered %d %s, want 200", res.Code, res.Body)
	}
	wantError(t, w.readAs("opkg-virtual", opkgFeed, "nobody"), http.StatusForbidden, "Forbidden")
	wantError(t, w.readAs("opkg-virtual", opkgFeed, ""), http.StatusUnauthorized,
		"Authentication is required")
	for _, path := range []string{opkgFeed, opkgXlat} {
		wantError(t, w.readAs("opkg-hidden", path, "nobody"), http.StatusNotFound,
			"Resource not found; Path: 'opkg-hidden:"+path+"'")
	}
}

// TestUnitOpkgVirtualReadsNeedTheRouterKitsStore: a server whose item kit
// was built without the router kit's store cannot walk a virtual
// repository's members, a fault of its own: the feed and an IPK answer
// 500.
func TestUnitOpkgVirtualReadsNeedTheRouterKitsStore(t *testing.T) {
	w := newItemWorld(t)
	w.opkgRepos(
		repo.Config{Key: "opkg-local", Class: repo.Local},
		repo.Config{Key: "opkg-virtual", Class: repo.Virtual, Repositories: []string{"opkg-local"}},
	)
	w.kit.PackageOptions = pkgroute.Options{}
	for _, path := range []string{opkgFeed, opkgXlat} {
		wantError(t, w.do(http.MethodGet, "/artifactory/opkg-virtual/"+path, "admin", ""),
			http.StatusInternalServerError, "internal error")
	}
}
