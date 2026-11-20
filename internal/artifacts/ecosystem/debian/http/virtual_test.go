package debianhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
func (w *overlayWorld) readAs(key, path, name string, keys ...string) *httptest.ResponseRecorder {
	w.t.Helper()
	p := kernel.Principal{Name: name, Anonymous: name == ""}
	r := w.request(key, path, kittest.Admin.Name)
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, readsOnly(keys...)))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
}

// listedAs is the Filename of each row of the virtual repository key's
// Packages index at index, as the user name, who may read only keys,
// reads it; the test fails unless it answers 200.
func (w *overlayWorld) listedAs(key, index, name string, keys ...string) []string {
	w.t.Helper()
	rec := w.readAs(key, index, name, keys...)
	if rec.Code != http.StatusOK {
		w.t.Fatalf("%s %s answered %d %s, want 200", key, index, rec.Code, rec.Body)
	}
	return filenames(w.t, rec.Body.Bytes())
}

// listed is listedAs for the administrator at the amd64 Packages index.
func (w *overlayWorld) listed(key string) []string {
	w.t.Helper()
	rec := w.get(key, debPackages, kittest.Admin.Name)
	if rec.Code != http.StatusOK {
		w.t.Fatalf("%s Packages answered %d %s, want 200", key, rec.Code, rec.Body)
	}
	return filenames(w.t, rec.Body.Bytes())
}

// releaseOf is the stable distribution's Release, declaring each index by
// its path under dists/stable/.
func releaseOf(indexes map[string][]byte) []byte {
	var out bytes.Buffer
	out.WriteString("Suite: stable\nArchitectures: amd64 arm64\nComponents: main\nSHA256:\n")
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for p := range indexes {
			if !yield(p) {
				return
			}
		}
	}) {
		fmt.Fprintf(&out, " %x %d %s\n", sha256.Sum256(indexes[p]), len(indexes[p]),
			strings.TrimPrefix(p, "dists/stable/"))
	}
	return out.Bytes()
}

// TestAccDebianVirtualWalksNestedMembersOnce: a nested virtual member's
// packages are listed and served, and a virtual repository reached again,
// itself or through its member, is skipped rather than failing the
// metadata (decision 0008, R2). A member of another type (R11) and one
// with the Trivial layout, which JFrog's Debian guide says a virtual
// repository cannot include, are passed over, not answered 500.
func TestAccDebianVirtualWalksNestedMembersOnce(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-first", Class: repo.Local},
		repo.Config{Key: "deb-trivial", Class: repo.Local, DebianTrivialLayout: new(true)},
		repo.Config{Key: "deb-second", Class: repo.Local},
		repo.Config{
			Key: "deb-inner", Class: repo.Virtual, Repositories: []string{"deb-first", "deb-virtual"},
		},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{
			"deb-virtual", "deb-inner", kittest.LocalKey, "deb-trivial", "deb-second",
		}},
	)
	w.publish("deb-first", packagesIndex("alpha 1.0 "+debAlpha))
	w.publish("deb-trivial", packagesIndex("trivial 1.0 pool/trivial_1.0_amd64.deb"))
	w.publish("deb-second", packagesIndex("beta 1.0 "+debBeta))
	w.put("deb-first", debAlpha, false, debBody)
	w.put("deb-trivial", "pool/trivial_1.0_amd64.deb", false, debBody)
	for _, key := range []string{"deb-virtual", "deb-inner"} {
		if rec := w.get(key, debRelease, "admin"); rec.Code != http.StatusOK {
			t.Errorf("%s Release answered %d %s, want 200", key, rec.Code, rec.Body)
		}
	}
	if got := w.listed("deb-virtual"); !slices.Equal(got, []string{debAlpha, debBeta}) {
		t.Errorf("virtual Packages lists %v, want alpha and beta", got)
	}
	if rec := w.get("deb-virtual", debAlpha, "admin"); rec.Code != http.StatusOK ||
		!bytes.Equal(rec.Body.Bytes(), debBody) {
		t.Errorf("nested member's package answered %d %q, want 200", rec.Code, rec.Body)
	}
	wantError(t, w.get("deb-virtual", "pool/trivial_1.0_amd64.deb", "admin"),
		http.StatusNotFound, "Could not find resource")
}

// TestAccDebianVirtualMetadataPassesOverMembers: a member whose metadata
// the virtual repository cannot use is passed over and the others' rows
// are listed: a remote whose upstream misses or fails, which 7.x passes
// over as any unfound resource (R3) [I], and a remote the caller may not
// read, whose upstream is never asked.
func TestAccDebianVirtualMetadataPassesOverMembers(t *testing.T) {
	index := packagesIndex("beta 1.0 " + debBeta)
	asked := map[string]bool{}
	var mu sync.Mutex
	w := newOverlayWorld(t, withSources(func(
		_ context.Context, q source.Request, _ source.Permit,
	) (source.Result, error) {
		mu.Lock()
		asked[q.Repo.Key] = true
		mu.Unlock()
		switch q.Repo.Key {
		case "deb-failing":
			return source.Result{}, source.ErrUnavailable
		case "deb-hidden":
			return upstream(map[string]func() (source.Result, error){
				debRelease: streamed(releaseFor(index), nil), debPackages: streamed(index, nil),
			})(t.Context(), q, source.Permit{})
		}
		return source.Result{}, kernel.ErrNotFound
	}))
	w.debRepos(
		repo.Config{Key: "deb-missing", Class: repo.Remote},
		repo.Config{Key: "deb-failing", Class: repo.Remote},
		repo.Config{Key: "deb-hidden", Class: repo.Remote},
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{
			"deb-missing", "deb-failing", "deb-hidden", "deb-local",
		}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	got := w.listedAs("deb-virtual", debPackages, "reader", "deb-missing", "deb-failing", "deb-local")
	if !slices.Equal(got, []string{debAlpha}) {
		t.Errorf("virtual Packages lists %v, want the local member's alpha", got)
	}
	if !asked["deb-missing"] || !asked["deb-failing"] || asked["deb-hidden"] {
		t.Errorf("upstreams asked %v, want deb-missing and deb-failing only", asked)
	}
}

// TestAccDebianVirtualReadsThroughItsMembers: a caller who may read only
// the members reads the metadata, a package and a member's Sources index
// through the virtual repository (decision 0008, R3); a caller who may
// read no member is refused, 403 or the 401 challenge, or 404 "Resource
// not found" while the virtual repository hides unauthorized resources
// (R4).
func TestAccDebianVirtualReadsThroughItsMembers(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
		repo.Config{
			Key: "deb-hidden", Class: repo.Virtual, Repositories: []string{"deb-local"},
			HideUnauthorizedResources: new(true),
		},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	w.put("deb-local", debAlpha, false, debBody)
	w.put("deb-local", debSources, false, sourcesIndex("1.0-1"))
	if got := w.listedAs("deb-virtual", debPackages, "member", "deb-local"); !slices.Equal(got, []string{debAlpha}) {
		t.Errorf("member reader's Packages lists %v, want alpha", got)
	}
	for _, path := range []string{debRelease, debAlpha, debSources} {
		if rec := w.readAs("deb-virtual", path, "member", "deb-local"); rec.Code != http.StatusOK {
			t.Errorf("member reader's %s answered %d %s, want 200", path, rec.Code, rec.Body)
		}
	}
	wantError(t, w.readAs("deb-virtual", debRelease, "nobody"), http.StatusForbidden, "Forbidden")
	wantError(t, w.readAs("deb-virtual", debAlpha, ""), http.StatusUnauthorized,
		"Authentication is required")
	for _, path := range []string{debRelease, debAlpha} {
		wantError(t, w.readAs("deb-hidden", path, "nobody"), http.StatusNotFound,
			"Resource not found; Path: 'deb-hidden:"+path+"'")
	}
}

// cachedOverlayWorld is an overlay world whose merged metadata cache reads
// a clock that advance moves on.
func cachedOverlayWorld(t *testing.T, tune ...func(*overlayConfig)) (*overlayWorld, func(time.Duration)) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	w := newOverlayWorld(t, append(tune, func(c *overlayConfig) {
		c.Cache = virtual.NewCache(virtual.CacheOptions{Now: clock})
	})...)
	return w, func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }
}

// TestAccDebianVirtualMetadataIsKeptForThePeriod: the merged metadata is
// kept, Release and indexes as one set, for the virtual repository's
// virtualRetrievalCachePeriodSecs (decision 0008, R8): a read within it
// serves the same bytes though a member changed; -1 builds on every read;
// a rebuild that finds no member's metadata drops the kept set rather
// than serving it; and callers who may read different members never share
// an entry. A snapshot of the virtual repository builds its own copy.
func TestAccDebianVirtualMetadataIsKeptForThePeriod(t *testing.T) {
	w, advance := cachedOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-other", Class: repo.Local},
		repo.Config{
			Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local", "deb-other"},
			VirtualRetrievalCachePeriodSecs: new(60),
		},
		repo.Config{
			Key: "deb-never", Class: repo.Virtual, Repositories: []string{"deb-local"},
			VirtualRetrievalCachePeriodSecs: new(-1),
		},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	alpha, beta := []string{debAlpha}, []string{debBeta}
	release := w.get("deb-virtual", debRelease, "admin").Body.String()
	if got := w.listed("deb-virtual"); !slices.Equal(got, alpha) {
		t.Fatalf("virtual Packages lists %v, want alpha", got)
	}
	if got := w.listed("deb-never"); !slices.Equal(got, alpha) {
		t.Fatalf("virtual Packages lists %v, want alpha", got)
	}
	if got := w.listedAs("deb-virtual", debPackages, "a", "deb-local"); !slices.Equal(got, alpha) {
		t.Errorf("the local member's reader got %v, want alpha", got)
	}
	w.publish("deb-other", packagesIndex("gamma 1.0 pool/gamma_1.0_amd64.deb"))
	if got := w.listedAs("deb-virtual", debPackages, "b", "deb-other"); slices.Contains(got, debAlpha) {
		t.Errorf("the other member's reader got %v, another caller's entry", got)
	}

	w.publish("deb-local", packagesIndex("beta 1.0 "+debBeta))
	if got := w.get("deb-virtual", debRelease, "admin").Body.String(); got != release {
		t.Errorf("a read within the period built the Release again:\n%s\nwant\n%s", got, release)
	}
	if got := w.listed("deb-never"); !slices.Equal(got, beta) {
		t.Errorf("a period of -1 kept the metadata: %v", got)
	}
	files, err := DebianAPI{w.kit}.debianSnapshotFiles(t.Context(), w.db, w.debRepo("deb-virtual"),
		"stable", kittest.Admin, kittest.AdminAccess(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := filenames(t, files[debPackages]); !slices.Contains(got, debBeta) {
		t.Errorf("the snapshot copied %v, the kept metadata", got)
	}
	advance(61 * time.Second)
	if got := w.listed("deb-virtual"); !slices.Contains(got, debBeta) {
		t.Errorf("after the period the virtual Packages lists %v, want beta", got)
	}
	for _, key := range []string{"deb-local", "deb-other"} {
		if _, err := w.db.Items().Delete(t.Context(), key, false, debRelease); err != nil {
			t.Fatal(err)
		}
	}
	advance(61 * time.Second)
	wantError(t, w.get("deb-virtual", debRelease, "admin"), http.StatusNotFound,
		"Could not find resource")
}

// TestAccDebianVirtualIndexesRemoteMembersForItsArchitectures: a virtual
// repository reads a remote member's Packages indexes only for its Indexed
// Remote Architectures (debianDefaultArchitectures), amd64 when it names
// none, and never asks the upstream for the others; a local member's
// indexes are merged whatever their architecture (JFrog's Debian guide,
// "Indexed Remote Architectures") [I].
func TestAccDebianVirtualIndexesRemoteMembersForItsArchitectures(t *testing.T) {
	const arm = "dists/stable/main/binary-arm64/Packages"
	remoteAMD := packagesIndex("beta 1.0 " + debBeta)
	remoteARM := packagesIndex("gamma 1.0 pool/gamma_1.0_arm64.deb")
	var mu sync.Mutex
	asked := map[string]bool{}
	w := newOverlayWorld(t, withSources(func(
		ctx context.Context, q source.Request, p source.Permit,
	) (source.Result, error) {
		mu.Lock()
		asked[q.Resource.Path] = true
		mu.Unlock()
		return upstream(map[string]func() (source.Result, error){
			debRelease:  streamed(releaseOf(map[string][]byte{debPackages: remoteAMD, arm: remoteARM}), nil),
			debPackages: streamed(remoteAMD, nil),
			arm:         streamed(remoteARM, nil),
		})(ctx, q, p)
	}))
	w.debRepos(
		repo.Config{Key: "deb-remote", Class: repo.Remote},
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{
			Key: "deb-default", Class: repo.Virtual, Repositories: []string{"deb-local", "deb-remote"},
		},
		repo.Config{
			Key: "deb-arm", Class: repo.Virtual, Repositories: []string{"deb-local", "deb-remote"},
			DebianDefaultArchitectures: "i386, arm64",
		},
	)
	local := map[string][]byte{arm: packagesIndex("delta 1.0 pool/delta_1.0_arm64.deb")}
	w.put("deb-local", debRelease, false, releaseOf(local))
	w.put("deb-local", arm, false, local[arm])

	if got := w.listedAs("deb-default", arm, "admin", "deb-local", "deb-remote"); !slices.Equal(got, []string{"pool/delta_1.0_arm64.deb"}) {
		t.Errorf("default arm64 Packages lists %v, want the local member's delta", got)
	}
	if got := w.listed("deb-default"); !slices.Equal(got, []string{debBeta}) {
		t.Errorf("default amd64 Packages lists %v, want the remote member's beta", got)
	}
	if asked[arm] {
		t.Error("the upstream was asked for an architecture the virtual repository does not index")
	}
	wantError(t, w.get("deb-arm", debPackages, "admin"), http.StatusNotFound, "Could not find resource")
	got := w.listedAs("deb-arm", arm, "admin", "deb-local", "deb-remote")
	if want := []string{"pool/delta_1.0_arm64.deb", "pool/gamma_1.0_arm64.deb"}; !slices.Equal(got, want) {
		t.Errorf("arm64 Packages lists %v, want %v", got, want)
	}
}

// TestUnitDebianVirtualReadsNeedTheRouterKitsStore: a server whose item
// kit was built without the router kit's store cannot walk a virtual
// repository's members, a fault of its own: the metadata, a package and a
// snapshot answer 500.
func TestUnitDebianVirtualReadsNeedTheRouterKitsStore(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	w.put("deb-local", debAlpha, false, debBody)
	w.kit.PackageOptions = pkgroute.Options{}
	for _, path := range []string{debRelease, debAlpha} {
		wantError(t, w.get("deb-virtual", path, "admin"), http.StatusInternalServerError, "internal error")
	}
	if _, err := (DebianAPI{w.kit}).debianSnapshotFiles(t.Context(), w.db, w.debRepo("deb-virtual"),
		"stable", kittest.Admin, kittest.AdminAccess(), nil); err == nil {
		t.Error("a snapshot walked the virtual repository without the router kit's store")
	}
}
