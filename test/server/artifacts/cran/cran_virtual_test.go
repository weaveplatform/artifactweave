package cran_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// cranZeta is the source archive of the package zeta at version.
func cranZeta(t *testing.T, version string) string {
	t.Helper()
	return conanTestArchive(t, "zeta/DESCRIPTION", "Package: zeta\nVersion: "+version+"\n")
}

// cranMembers is a world whose upstream serves the published assertthat to
// every remote CRAN repository but cran-down, whose upstream fails, and
// cran-gone, whose upstream has no index, with the remote cran-remote, the
// local cran-local holding zeta 1.0 and each of extra.
func cranMembers(t *testing.T, extra ...repo.Config) *itemWorld {
	t.Helper()
	up := cranUpstream(t)
	read := up.HTTP
	up.HTTP = sourceBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
		switch q.Repo.Key {
		case "cran-down":
			return source.Result{}, source.ErrUnavailable
		case "cran-gone":
			return source.Result{}, kernel.ErrNotFound
		}
		return read.Read(ctx, q, p)
	})
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = up })
	cranPublish(t, w, "cran-local", func(*repo.Config) {}, cranZeta(t, "1.0"))
	for _, c := range append([]repo.Config{
		{Key: "cran-remote", Class: repo.Remote, PackageType: repo.Cran, URL: "https://up.example"},
	}, extra...) {
		if c.PackageType == "" {
			c.PackageType = repo.Cran
		}
		cranRepo(t, w, c)
	}
	return w
}

// TestAccCRANVirtualWalksNestedMembersOnce (#1552): a virtual repository
// over [remote, nested virtual -> local], whose nested member lists the
// outer one again and itself, beside a Generic member, lists the remote's
// and the local's packages once each and serves both archives, where the
// cycle and the Generic member answered 500 (decision 0008, R2 and R11). A
// remote whose upstream fails or has no index is passed over (R3) [I].
func TestAccCRANVirtualWalksNestedMembersOnce(t *testing.T) {
	w := cranMembers(t,
		repo.Config{Key: "cran-down", Class: repo.Remote, URL: "https://down.example"},
		repo.Config{Key: "cran-gone", Class: repo.Remote, URL: "https://gone.example"},
		repo.Config{
			Key: "cran-inner", Class: repo.Virtual,
			Repositories: []string{"cran-local", "cran-outer", "cran-inner"},
		},
		repo.Config{
			Key: "cran-outer", Class: repo.Virtual,
			Repositories: []string{"cran-down", "cran-remote", "cran-outer", "cran-inner", "libs-local", "cran-gone"},
		},
	)
	index := w.do(http.MethodGet, "/artifactory/cran-outer/"+cranIndex, "admin", "")
	wantStatus(t, index, http.StatusOK)
	for _, file := range []string{"assertthat_0.2.1.tar.gz", "zeta_1.0.tar.gz"} {
		if strings.Count(index.Body.String(), "File: "+file) != 1 {
			t.Errorf("the index does not list %s once:\n%s", file, index.Body)
		}
	}
	got := w.do(http.MethodGet, "/artifactory/cran-outer/"+cranAssertthat, "admin", "")
	wantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")) {
		t.Error("the remote's archive changed through the virtual repository")
	}
	wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-outer/src/contrib/zeta_1.0.tar.gz",
		"admin", ""), http.StatusOK)
}

// TestAccCRANVirtualListsTheGreatestVersionOfAName (#1552): the virtual
// index lists a package at the greatest version its members list, even when
// a later member lists it (RTFACT-31049, RTFACT-20544), where it listed the
// first member's; each member's archive is still served at its own path,
// the first member that holds the file serving it (decision 0008, R3).
func TestAccCRANVirtualListsTheGreatestVersionOfAName(t *testing.T) {
	w := cranMembers(t, repo.Config{
		Key: "cran-versions", Class: repo.Virtual, Repositories: []string{"cran-local", "cran-newer"},
	})
	cranPublish(t, w, "cran-newer", func(*repo.Config) {}, cranZeta(t, "1.10"))
	index := w.do(http.MethodGet, "/artifactory/cran-versions/"+cranIndex, "admin", "")
	wantStatus(t, index, http.StatusOK)
	if !strings.Contains(index.Body.String(), "File: zeta_1.10.tar.gz") ||
		strings.Contains(index.Body.String(), "zeta_1.0.tar.gz") {
		t.Errorf("the index does not list zeta 1.10 alone:\n%s", index.Body)
	}
	for _, file := range []string{"zeta_1.0.tar.gz", "zeta_1.10.tar.gz"} {
		wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-versions/src/contrib/"+file,
			"admin", ""), http.StatusOK)
	}
}

// TestAccCRANVirtualReadsThroughItsMembers (#1552): a caller who may read
// a member, and no virtual repository, reads the virtual index and the
// member's archive through it, listing only what that member serves, a
// member whose index it may not read passed over, where
// it was refused 403; the member that serves a row decides it (decision
// 0008, R3). A caller who may read no member is refused 403, and an
// anonymous caller 401.
func TestAccCRANVirtualReadsThroughItsMembers(t *testing.T) {
	w := cranMembers(t, repo.Config{
		Key: "cran-view", Class: repo.Virtual, Repositories: []string{"cran-remote", "cran-local"},
	})
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "cran-local-read",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			// The remote's archives alone, so its index is refused and the
			// remote passed over.
			Targets: map[string]accesstest.Target{
				"cran-local":  {Include: []string{"**"}},
				"cran-remote": {Include: []string{"src/contrib/*.tar.gz"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	index := w.do(http.MethodGet, "/artifactory/cran-view/"+cranIndex, "user", "")
	wantStatus(t, index, http.StatusOK)
	if !strings.Contains(index.Body.String(), "File: zeta_1.0.tar.gz") ||
		strings.Contains(index.Body.String(), "assertthat") {
		t.Errorf("the member's reader got an index of\n%s\nwant zeta alone", index.Body)
	}
	wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-view/src/contrib/zeta_1.0.tar.gz",
		"user", ""), http.StatusOK)
	if err := w.db.Permissions().Delete(t.Context(), "cran-local-read"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-view/"+cranIndex, "user", ""),
		http.StatusForbidden)
	wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-view/"+cranIndex, "", ""),
		http.StatusUnauthorized)
}

// TestAccCRANVirtualIndexIsKeptForThePeriod (#1552): a virtual index read
// again within the virtual repository's virtualRetrievalCachePeriodSecs is
// the one first built, though a member has published a package since, and
// a period of -1 builds it on every read (decision 0008, R8; RTFACT-18674).
func TestAccCRANVirtualIndexIsKeptForThePeriod(t *testing.T) {
	w := cranMembers(t,
		repo.Config{Key: "cran-kept", Class: repo.Virtual, Repositories: []string{"cran-local"}},
		repo.Config{
			Key: "cran-fresh", Class: repo.Virtual, Repositories: []string{"cran-local"},
			VirtualRetrievalCachePeriodSecs: new(-1),
		},
	)
	read := func(key string) string {
		t.Helper()
		rec := w.do(http.MethodGet, "/artifactory/"+key+"/"+cranIndex, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		return rec.Body.String()
	}
	kept, fresh := read("cran-kept"), read("cran-fresh")
	wantStatus(t, w.do(http.MethodPost, "/artifactory/api/cran/cran-local/sources", "admin",
		conanTestArchive(t, "omega/DESCRIPTION", "Package: omega\nVersion: 1.0\n")), http.StatusCreated)
	if got := read("cran-kept"); got != kept {
		t.Errorf("a read within the period got\n%s\nwant the first\n%s", got, kept)
	}
	if got := read("cran-fresh"); got == fresh || !strings.Contains(got, "Package: omega") {
		t.Errorf("a period of -1 served\n%s\nwant omega added", got)
	}
}

// TestAccCRANVirtualAnswersAStorageFault (#1552): a member whose stored
// index cannot be read is a fault of this server, never passed over as a
// member that does not serve the index, so the virtual index and a virtual
// package answer 500 rather than leave the member's packages out (decision
// 0008, R3: only a miss, a refusal or an upstream fault is passed over), as
// a hand-written member loop answered it before.
func TestAccCRANVirtualAnswersAStorageFault(t *testing.T) {
	var failure *cranMigrationFailure
	w := newItemWorld(t, false, func(c *Config) {
		failure = &cranMigrationFailure{Store: c.Store}
		c.Store = failure
	})
	cranPublish(t, w, "cran-local", func(*repo.Config) {}, cranZeta(t, "1.0"))
	cranRepo(t, w, repo.Config{
		Key: "cran-view", Class: repo.Virtual, PackageType: repo.Cran,
		Repositories: []string{"cran-local"},
	})
	// No merge is kept yet, so none can be served as a stale fallback
	// (virtual.Cache.Get).
	failure.mode = "index"
	for _, path := range []string{cranIndex, "src/contrib/zeta_1.0.tar.gz"} {
		wantStatus(t, w.do(http.MethodGet, "/artifactory/cran-view/"+path, "admin", ""),
			http.StatusInternalServerError)
	}
}
