package alpinehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
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
func (w *itemWorld) readAs(key, path, name string, keys ...string) *httptest.ResponseRecorder {
	w.t.Helper()
	p := kernel.Principal{Name: name, Anonymous: name == ""}
	r := httptest.NewRequestWithContext(w.t.Context(), http.MethodGet, "/artifactory/"+key+"/"+path, nil)
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, readsOnly(keys...)))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
}

// listed is the names the index rec served lists, failing the test
// unless it answered 200.
func listed(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("index answered %d %s, want 200", rec.Code, rec.Body)
	}
	return indexNames(t, rec.Body.Bytes())
}

// TestAccAlpineVirtualWalksNestedMembersOnce: a nested virtual member's
// packages are listed and served, and a virtual repository reached again,
// itself or through its member, is skipped rather than failing the index
// (decision 0008, R2).
func TestAccAlpineVirtualWalksNestedMembersOnce(t *testing.T) {
	w := newItemWorld(t)
	w.repos(
		alpineRepo(alpineLocal, repo.Local),
		alpineRepo("alpine-inner", repo.Virtual, alpineLocal, alpineVirtual),
		alpineRepo(alpineVirtual, repo.Virtual, alpineVirtual, "alpine-inner"),
	)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	w.put(alpineLocal, apkHelloPath, apkBody)
	if got := listed(t, w.get(alpineVirtual, apkIndexPath)); !slices.Equal(got, []string{"hello"}) {
		t.Errorf("index lists %v, want [hello]", got)
	}
	if rec := w.get(alpineVirtual, apkHelloPath); rec.Code != http.StatusOK || rec.Body.String() != string(apkBody) {
		t.Errorf("package answered %d %q, want the local member's", rec.Code, rec.Body)
	}
}

// TestAccAlpineVirtualIndexPassesOverMembers: a member that cannot serve
// the index is passed over and the index lists the others' packages: one
// of another package type (R11), a remote whose upstream misses, fails or
// cannot check a listed package's authority, which 7.x passes over as any
// unfound resource (R3, RTFACT-30422) [I], and a remote the caller may not
// read.
func TestAccAlpineVirtualIndexPassesOverMembers(t *testing.T) {
	index := apkIndex(t, "remote")
	for _, tc := range []struct {
		name    string
		tune    []func(*Config)
		members []string
		as      string
	}{
		{name: "member of another package type", members: []string{"generic-local", alpineLocal}},
		{
			name: "remote whose upstream misses", tune: []func(*Config){withSource(upstream(nil))},
			members: []string{alpineRemote, alpineLocal},
		},
		{
			name: "remote whose upstream fails",
			tune: []func(*Config){withSource(upstream(map[string]func() (source.Result, error){
				apkIndexPath: func() (source.Result, error) { return source.Result{}, source.ErrUnavailable },
			}))},
			members: []string{alpineRemote, alpineLocal},
		},
		{
			name: "remote whose listed package cannot be checked",
			tune: []func(*Config){withSource(upstream(map[string]func() (source.Result, error){
				apkIndexPath: streamed(index, func() func(context.Context) error {
					return failsOnCall(3, source.ErrUnavailable)
				}),
			}))},
			members: []string{alpineRemote, alpineLocal},
		},
		{
			name: "remote the caller may not read",
			tune: []func(*Config){withSource(upstream(map[string]func() (source.Result, error){
				apkIndexPath: streamed(index, nil),
			}))},
			members: []string{alpineRemote, alpineLocal}, as: "reader",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, tc.tune...)
			w.repos(
				alpineRepo(alpineLocal, repo.Local), alpineRepo(alpineRemote, repo.Remote),
				repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic},
				alpineRepo(alpineVirtual, repo.Virtual, tc.members...),
			)
			w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
			w.put("generic-local", apkIndexPath, index)
			rec := w.get(alpineVirtual, apkIndexPath)
			if tc.as != "" {
				rec = w.readAs(alpineVirtual, apkIndexPath, tc.as, alpineLocal)
			}
			if got := listed(t, rec); !slices.Equal(got, []string{"hello"}) {
				t.Errorf("index lists %v, want the local member's [hello]", got)
			}
		})
	}
}

// TestAccAlpineVirtualReadsThroughItsMembers: a caller who may read only
// the members reads the index and a package through the virtual
// repository, as 7.x decides a read through one by its members (decision
// 0008, R3); a caller who may read no member is refused, 403 or the 401
// challenge, or 404 "Resource not found" while the virtual repository
// hides unauthorized resources (R4).
func TestAccAlpineVirtualReadsThroughItsMembers(t *testing.T) {
	w := newItemWorld(t)
	hidden := alpineRepo("alpine-hidden", repo.Virtual, alpineLocal)
	hidden.HideUnauthorizedResources = new(true)
	w.repos(alpineRepo(alpineLocal, repo.Local), alpineRepo(alpineVirtual, repo.Virtual, alpineLocal), hidden)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	w.put(alpineLocal, apkHelloPath, apkBody)
	if got := listed(t, w.readAs(alpineVirtual, apkIndexPath, "member", alpineLocal)); !slices.Equal(got, []string{"hello"}) {
		t.Errorf("member reader's index lists %v, want [hello]", got)
	}
	if rec := w.readAs(alpineVirtual, apkHelloPath, "member", alpineLocal); rec.Code != http.StatusOK {
		t.Errorf("member reader's package answered %d %s, want 200", rec.Code, rec.Body)
	}
	wantError(t, w.readAs(alpineVirtual, apkIndexPath, "nobody"), http.StatusForbidden, "Forbidden")
	wantError(t, w.readAs(alpineVirtual, apkIndexPath, ""), http.StatusUnauthorized,
		"Authentication is required")
	for _, path := range []string{apkIndexPath, apkHelloPath} {
		wantError(t, w.readAs("alpine-hidden", path, "nobody"), http.StatusNotFound,
			"Resource not found; Path: 'alpine-hidden:"+path+"'")
	}
}

// TestAccAlpineVirtualIndexIsKeptForThePeriod: the merged index is kept
// for the virtual repository's virtualRetrievalCachePeriodSecs (decision
// 0008, R8): a second read within it serves the same bytes though a
// member changed, one after it the new merge; -1 builds on every read; a
// rebuild that finds no member's index drops the kept one rather than
// serving it; and callers who may read different members never share an
// entry.
func TestAccAlpineVirtualIndexIsKeptForThePeriod(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }
	w := newItemWorld(t, func(c *Config) {
		c.Cache = virtual.NewCache(virtual.CacheOptions{Now: clock})
	})
	kept := alpineRepo(alpineVirtual, repo.Virtual, alpineLocal, "alpine-other")
	kept.VirtualRetrievalCachePeriodSecs = new(60)
	never := alpineRepo("alpine-never", repo.Virtual, alpineLocal)
	never.VirtualRetrievalCachePeriodSecs = new(-1)
	w.repos(alpineRepo(alpineLocal, repo.Local), alpineRepo("alpine-other", repo.Local), kept, never)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	w.put("alpine-other", apkIndexPath, apkIndex(t, "other"))

	first := w.get(alpineVirtual, apkIndexPath).Body.String()
	if got := listed(t, w.get("alpine-never", apkIndexPath)); !slices.Equal(got, []string{"hello"}) {
		t.Fatalf("index lists %v, want [hello]", got)
	}
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello", "later"))
	if second := w.get(alpineVirtual, apkIndexPath).Body.String(); second != first {
		t.Error("a read within the period built the index again")
	}
	if got := listed(t, w.get("alpine-never", apkIndexPath)); !slices.Equal(got, []string{"hello", "later"}) {
		t.Errorf("a period of -1 kept the index: %v", got)
	}
	advance(61 * time.Second)
	if got := listed(t, w.get(alpineVirtual, apkIndexPath)); !slices.Equal(got, []string{"hello", "later", "other"}) {
		t.Errorf("after the period the index lists %v, want the new merge", got)
	}

	// The members' readers: each keeps an entry of its own.
	if got := listed(t, w.readAs(alpineVirtual, apkIndexPath, "a", alpineLocal)); !slices.Equal(got, []string{"hello", "later"}) {
		t.Errorf("the local member's reader got %v", got)
	}
	if got := listed(t, w.readAs(alpineVirtual, apkIndexPath, "b", "alpine-other")); !slices.Equal(got, []string{"other"}) {
		t.Errorf("the other member's reader got %v, another caller's entry", got)
	}

	for _, key := range []string{alpineLocal, "alpine-other"} {
		if _, err := w.db.Items().Delete(t.Context(), key, false, apkIndexPath); err != nil {
			t.Fatal(err)
		}
	}
	advance(61 * time.Second)
	if got := listed(t, w.get(alpineVirtual, apkIndexPath)); len(got) != 0 {
		t.Errorf("a rebuild that found no member index served %v, want an empty index", got)
	}
}

// TestUnitAlpineVirtualReadsNeedTheRouterKitsStore: a server whose item
// kit was built without the router kit's store cannot walk a virtual
// repository's members, a fault of its own: the index and a package answer
// 500.
func TestUnitAlpineVirtualReadsNeedTheRouterKitsStore(t *testing.T) {
	w := newItemWorld(t)
	w.repos(alpineRepo(alpineLocal, repo.Local), alpineRepo(alpineVirtual, repo.Virtual, alpineLocal))
	w.kit.PackageOptions = pkgroute.Options{}
	for _, path := range []string{apkIndexPath, apkHelloPath} {
		wantError(t, w.get(alpineVirtual, path), http.StatusInternalServerError, "internal error")
	}
}
