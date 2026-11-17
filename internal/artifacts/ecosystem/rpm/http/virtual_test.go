package rpmhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
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

// listedAs is rpmListed for the user name, who may read only keys.
func listedAs(t *testing.T, w *itemWorld, key, name string, keys ...string) []string {
	t.Helper()
	res := w.readAs(key, "repodata/repomd.xml", name, keys...)
	if res.Code != http.StatusOK {
		t.Fatalf("%s repomd.xml answered %d %s, want 200", key, res.Code, res.Body)
	}
	primaryPath, sum, err := rpm.DecodeRepomd(res.Body.Bytes(), "")
	if err != nil {
		t.Fatalf("%s repomd.xml does not decode: %v", key, err)
	}
	res = w.readAs(key, primaryPath, name, keys...)
	packages, err := rpm.DecodePrimary(res.Body.Bytes(), "", sum)
	if res.Code != http.StatusOK || err != nil {
		t.Fatalf("%s primary answered %d (%v), want 200", key, res.Code, err)
	}
	paths := make([]string, 0, len(packages))
	for _, pkg := range packages {
		paths = append(paths, pkg.Path)
	}
	return paths
}

// rpmUpstream serves the createrepo_c fixture's repodata as a remote's
// upstream.
func rpmUpstream(t *testing.T) func(*Config) {
	manifest, primary, primaryPath := rpmUpstreamFixture(t)
	responses := map[string][]byte{"repodata/repomd.xml": manifest, primaryPath: primary}
	return func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(func(
			_ context.Context, q source.Request, _ source.Permit,
		) (source.Result, error) {
			body, ok := responses[q.Resource.Path]
			if !ok {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		})}
	}
}

// failingUpstream is a remote's upstream that answers every read with err.
func failingUpstream(err error) func(*Config) {
	return func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(func(
			context.Context, source.Request, source.Permit,
		) (source.Result, error) {
			return source.Result{}, err
		})}
	}
}

// TestAccRPMVirtualWalksNestedMembersOnce: a nested virtual member's
// packages are listed and served, and a virtual repository reached again,
// itself or through its member, is skipped rather than failing the
// repodata (decision 0008, R2).
func TestAccRPMVirtualWalksNestedMembersOnce(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{Key: "rpm-inner", Class: repo.Virtual, Repositories: []string{"rpm-local", "rpm-virtual"}},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-virtual", "rpm-inner"}},
	)
	w.publish("rpm-local", "")
	if got := rpmListed(t, w, "rpm-virtual"); !slices.Equal(got, []string{rpmAlphaPath}) {
		t.Errorf("virtual repodata lists %v, want alpha", got)
	}
	if res := w.do(http.MethodGet, "/artifactory/rpm-virtual/"+rpmAlphaPath, "admin", ""); res.Code != http.StatusOK {
		t.Errorf("package answered %d %s, want 200", res.Code, res.Body)
	}
}

// TestAccRPMVirtualRepodataPassesOverMembers: a member that cannot serve
// the repodata is passed over and the repodata lists the others'
// packages: one of another package type (R11), a remote whose upstream
// misses or fails, which 7.x passes over as any unfound resource (R3;
// RTFACT-30422's YUM virtual serves repomd.xml without its failing
// remote) [I], and a remote the caller may not read.
func TestAccRPMVirtualRepodataPassesOverMembers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tune   func(*Config)
		member string
		reader bool
	}{
		{name: "member of another package type", tune: func(*Config) {}, member: "generic-local"},
		{name: "remote whose upstream misses", tune: failingUpstream(kernel.ErrNotFound), member: "rpm-remote"},
		{name: "remote whose upstream fails", tune: failingUpstream(source.ErrUnavailable), member: "rpm-remote"},
		{name: "remote the caller may not read", tune: rpmUpstream(t), member: "rpm-remote", reader: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, tc.tune)
			w.rpmRepos(
				repo.Config{Key: "rpm-local", Class: repo.Local},
				repo.Config{Key: "rpm-remote", Class: repo.Remote},
				repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{tc.member, "rpm-local"}},
			)
			generic := repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic}
			if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&generic)); err != nil {
				t.Fatal(err)
			}
			w.publish("generic-local", "")
			w.publish("rpm-local", "")
			got := rpmListed(t, w, "rpm-virtual")
			if tc.reader {
				got = listedAs(t, w, "rpm-virtual", "reader", "rpm-local")
			}
			if !slices.Equal(got, []string{rpmAlphaPath}) {
				t.Errorf("virtual repodata lists %v, want the local member's alpha", got)
			}
		})
	}
}

// TestAccRPMVirtualReadsThroughItsMembers: a caller who may read only the
// members reads the repodata and a package through the virtual
// repository (decision 0008, R3); a caller who may read no member is
// refused, 403 or the 401 challenge, or 404 "Resource not found" while the
// virtual repository hides unauthorized resources (R4).
func TestAccRPMVirtualReadsThroughItsMembers(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-local"}},
		repo.Config{
			Key: "rpm-hidden", Class: repo.Virtual, Repositories: []string{"rpm-local"},
			HideUnauthorizedResources: new(true),
		},
	)
	w.publish("rpm-local", "")
	if got := listedAs(t, w, "rpm-virtual", "member", "rpm-local"); !slices.Equal(got, []string{rpmAlphaPath}) {
		t.Errorf("member reader's repodata lists %v, want alpha", got)
	}
	if res := w.readAs("rpm-virtual", rpmAlphaPath, "member", "rpm-local"); res.Code != http.StatusOK {
		t.Errorf("member reader's package answered %d %s, want 200", res.Code, res.Body)
	}
	wantError(t, w.readAs("rpm-virtual", "repodata/repomd.xml", "nobody"), http.StatusForbidden, "Forbidden")
	wantError(t, w.readAs("rpm-virtual", "repodata/repomd.xml", ""), http.StatusUnauthorized,
		"Authentication is required")
	for _, path := range []string{"repodata/repomd.xml", rpmAlphaPath} {
		wantError(t, w.readAs("rpm-hidden", path, "nobody"), http.StatusNotFound,
			"Resource not found; Path: 'rpm-hidden:"+path+"'")
	}
}

// cachedWorld is an item world whose merged metadata cache reads clock,
// which advance moves on.
func cachedWorld(t *testing.T) (w *itemWorld, advance func(time.Duration)) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	w = newItemWorld(t, func(c *Config) {
		c.Cache = virtual.NewCache(virtual.CacheOptions{Now: clock})
	})
	return w, func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }
}

// dropRepomd deletes the repository key's repomd.xml, so it serves no
// repodata.
func dropRepomd(t *testing.T, w *itemWorld, key string) {
	t.Helper()
	if _, err := w.db.Items().Delete(t.Context(), key, false, "repodata/repomd.xml"); err != nil {
		t.Fatal(err)
	}
}

// TestAccRPMVirtualRepodataIsKeptForThePeriod: the merged repodata is
// kept, repomd.xml and primary as one set, for the virtual repository's
// virtualRetrievalCachePeriodSecs (decision 0008, R8): a read within it
// serves the same set though a member changed; -1 builds on every read; a
// rebuild that finds no member's repodata drops the kept set rather than
// serving it; and callers who may read different members never share an
// entry.
func TestAccRPMVirtualRepodataIsKeptForThePeriod(t *testing.T) {
	w, advance := cachedWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{Key: "rpm-empty", Class: repo.Local},
		repo.Config{
			Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-local", "rpm-empty"},
			VirtualRetrievalCachePeriodSecs: new(60),
		},
		repo.Config{
			Key: "rpm-never", Class: repo.Virtual, Repositories: []string{"rpm-local"},
			VirtualRetrievalCachePeriodSecs: new(-1),
		},
	)
	w.publish("rpm-local", "")
	alpha := []string{rpmAlphaPath}
	if got := rpmListed(t, w, "rpm-virtual"); !slices.Equal(got, alpha) {
		t.Fatalf("virtual repodata lists %v, want alpha", got)
	}
	if got := rpmListed(t, w, "rpm-never"); !slices.Equal(got, alpha) {
		t.Fatalf("virtual repodata lists %v, want alpha", got)
	}
	if got := listedAs(t, w, "rpm-virtual", "a", "rpm-local"); !slices.Equal(got, alpha) {
		t.Errorf("the local member's reader got %v, want alpha", got)
	}
	if got := listedAs(t, w, "rpm-virtual", "b", "rpm-empty"); len(got) != 0 {
		t.Errorf("the empty member's reader got %v, another caller's entry", got)
	}

	dropRepomd(t, w, "rpm-local")
	if got := rpmListed(t, w, "rpm-virtual"); !slices.Equal(got, alpha) {
		t.Errorf("a read within the period built the repodata again: %v", got)
	}
	if got := rpmListed(t, w, "rpm-never"); len(got) != 0 {
		t.Errorf("a period of -1 kept the repodata: %v", got)
	}
	advance(61 * time.Second)
	if got := rpmListed(t, w, "rpm-virtual"); len(got) != 0 {
		t.Errorf("a rebuild that found no member repodata served %v, want none", got)
	}
}

// TestAccRPMYumCalculationRefreshesTheKeptRepodata: POST /api/yum on a
// virtual repository answers as before and builds the repodata again, so
// the next read serves the new set within the period: 7.x calculates a
// virtual's repodata on this call and stores it (Calculate YUM
// Repository Metadata, RTFACT-14054) [I].
func TestAccRPMYumCalculationRefreshesTheKeptRepodata(t *testing.T) {
	w, _ := cachedWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{
			Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-local"},
			VirtualRetrievalCachePeriodSecs: new(3600),
		},
	)
	w.publish("rpm-local", "")
	if got := rpmListed(t, w, "rpm-virtual"); !slices.Equal(got, []string{rpmAlphaPath}) {
		t.Fatalf("virtual repodata lists %v, want alpha", got)
	}
	dropRepomd(t, w, "rpm-local")
	if got := rpmListed(t, w, "rpm-virtual"); !slices.Equal(got, []string{rpmAlphaPath}) {
		t.Fatalf("a read within the period built the repodata again: %v", got)
	}
	ctx := accessapi.WithCaller(t.Context(), kittest.Admin, kittest.AdminAccess().Decider)
	root := ""
	res, err := RPMAPI{w.kit}.CalculateYumRepositoryMetadata(ctx,
		artgen.CalculateYumRepositoryMetadataRequestObject{
			RepoKey: "rpm-virtual", Params: artgen.CalculateYumRepositoryMetadataParams{Path: &root},
		})
	want := artgen.CalculateYumRepositoryMetadata200TextResponse(
		"YUM metadata for virtual repository rpm-virtual was calculated.")
	if err != nil || res != want {
		t.Fatalf("calculation answered %v, %v, want %q", res, err, want)
	}
	if got := rpmListed(t, w, "rpm-virtual"); len(got) != 0 {
		t.Errorf("the read after the calculation lists %v, want the new, empty set", got)
	}
}

// TestUnitRPMVirtualReadsNeedTheRouterKitsStore: a server whose item kit
// was built without the router kit's store cannot walk a virtual
// repository's members, a fault of its own: the repodata and a package
// answer 500.
func TestUnitRPMVirtualReadsNeedTheRouterKitsStore(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-local", Class: repo.Local},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-local"}},
	)
	w.kit.PackageOptions = pkgroute.Options{}
	for _, path := range []string{"repodata/repomd.xml", rpmAlphaPath} {
		wantError(t, w.do(http.MethodGet, "/artifactory/rpm-virtual/"+path, "admin", ""),
			http.StatusInternalServerError, "internal error")
	}
}
