package itemkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// links are the link texts of an HTML folder index.
var links = regexp.MustCompile(`<a href="[^"]*">([^<]*)</a>`)

// indexOf sends r to k.GetItem and returns the status and the index's
// link texts.
func indexOf(t *testing.T, k *itemkit.Kit, r *http.Request) (int, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	k.GetItem(rec, r)
	var got []string
	for _, m := range links.FindAllStringSubmatch(rec.Body.String(), -1) {
		got = append(got, m[1])
	}
	return rec.Code, got
}

// asCaller is a request for target by a user whose decider allows what
// allows does, for files and folders alike.
func asCaller(
	t *testing.T,
	target string,
	allows func(accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions) bool,
) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	d := &accessapitest.Decider{AllowsFunc: allows, AllowsFolderFunc: allows}
	return r.WithContext(accessapi.WithCaller(r.Context(), kernel.Principal{Name: "user"}, d))
}

// browseWorld is a kit world with b-local, a local repository that
// excludes **/*.tmp, and pv, a virtual repository over libs-local, b-local
// and libs-remote that excludes **/*.secret, holding:
//
//	libs-local: dir/a.bin, dir/dup (a file), dir/x.secret
//	b-local: dir/a.bin, dir/b.bin, dir/t.tmp, dir/dup/f.bin
//	libs-remote's cache: dir/c.bin
func browseWorld(t *testing.T, st *inmem.Store) *kittest.World[*inmem.Store] {
	t.Helper()
	w := kittest.New(t, st)
	for _, c := range []repo.Config{
		{Key: "b-local", Class: repo.Local, ExcludesPattern: ptrTo("**/*.tmp")},
		{
			Key: "pv", Class: repo.Virtual, ExcludesPattern: ptrTo("**/*.secret"),
			Repositories: []string{kittest.LocalKey, "b-local", kittest.RemoteKey},
		},
	} {
		if err := st.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	b, err := st.Repos().Get(t.Context(), "b-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		c      repo.Config
		cached bool
		path   string
	}{
		{w.Local, false, "dir/a.bin"},
		{w.Local, false, "dir/dup"},
		{w.Local, false, "dir/x.secret"},
		{b, false, "dir/a.bin"},
		{b, false, "dir/b.bin"},
		{b, false, "dir/t.tmp"},
		{b, false, "dir/dup/f.bin"},
		{w.Remote, true, "dir/c.bin"},
	} {
		putFolders(t, st, f.c.Key, f.cached, f.path)
		w.Put(t, f.c, f.cached, f.path, []byte(f.path))
	}
	return w
}

// putFolders stores the folders above p in key's tree.
func putFolders(t *testing.T, st *inmem.Store, key string, cached bool, p string) {
	t.Helper()
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		if _, err := st.Items().Put(t.Context(), model.Item{
			RepoKey: key, Cached: cached, Name: dir, Folder: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestUnitVirtualIndexMergesMembers (#1537): a virtual repository's folder
// index lists every member's children, a remote member's cache included,
// once by name and sorted, a folder where any member holds one; each
// member's patterns and the virtual repository's leave out what they
// reject (RepositoryBrowsingServiceImpl.getVirtualRepoBrowsableChildrenData,
// virtualRepoAccepts), and a member the caller may not read lists nothing.
// Before, the index listed libs-local's folder alone, under its own key.
func TestUnitVirtualIndexMergesMembers(t *testing.T) {
	w := browseWorld(t, inmem.New())
	k := w.Kit(itemkit.Deps{})
	rec := httptest.NewRecorder()
	k.GetItem(rec, kittest.Request(t, http.MethodGet, "/artifactory/pv/dir/"))
	if want := "<title>Index of pv/dir</title>"; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("pv/dir/: %s, want %s", rec.Body, want)
	}
	for name, c := range map[string]struct {
		r    *http.Request
		want []string
	}{
		"admin": {
			kittest.Request(t, http.MethodGet, "/artifactory/pv/dir/"),
			[]string{"a.bin", "b.bin", "c.bin", "dup/"},
		},
		"b-local unreadable": {
			asCaller(t, "/artifactory/pv/dir/",
				func(_ accessapi.ResourceType, r accessapi.Repo, _ string, _ accessapi.Actions) bool {
					return r.Key != "b-local"
				}),
			[]string{"a.bin", "c.bin", "dup"},
		},
		"root": {
			kittest.Request(t, http.MethodGet, "/artifactory/pv/"),
			[]string{"dir/"},
		},
	} {
		if code, got := indexOf(t, k, c.r); code != http.StatusOK || !slices.Equal(got, c.want) {
			t.Errorf("%s: %d %v, want %v", name, code, got, c.want)
		}
	}
}

// TestUnitVirtualIndexMavenIndex (#1537): a Maven group virtual
// repository hides Maven index paths, any holding ".index" and the
// nexus-maven-repository-index files (virtualRepoAccepts); a generic one
// lists them.
func TestUnitVirtualIndexMavenIndex(t *testing.T) {
	st := inmem.New()
	w := kittest.New(t, st)
	for _, c := range []repo.Config{
		{Key: "mvn-local", Class: repo.Local, PackageType: repo.Maven},
		{Key: "mvn", Class: repo.Virtual, PackageType: repo.Maven, Repositories: []string{"mvn-local"}},
		{Key: "gen", Class: repo.Virtual, Repositories: []string{"mvn-local"}},
	} {
		if err := st.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	m, err := st.Repos().Get(t.Context(), "mvn-local")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		".index/nexus-maven-repository-index.gz", "nexus-maven-repository-index.properties",
		"org/a.jar",
	} {
		putFolders(t, st, "mvn-local", false, p)
		w.Put(t, m, false, p, []byte(p))
	}
	k := w.Kit(itemkit.Deps{})
	for target, want := range map[string][]string{
		"/artifactory/mvn/": {"org/"},
		"/artifactory/gen/": {".index/", "nexus-maven-repository-index.properties", "org/"},
	} {
		if code, got := indexOf(t, k, kittest.Request(t, http.MethodGet, target)); code !=
			http.StatusOK || !slices.Equal(got, want) {
			t.Errorf("%s: %d %v, want %v", target, code, got, want)
		}
	}
}

// failingChildren is a store whose listing of any folder's children fails.
type failingChildren struct{ *inmem.Store }

func (s failingChildren) Items() port.ItemStore { return failedChildren{s.Store.Items()} }

type failedChildren struct{ port.ItemStore }

func (failedChildren) Children(
	context.Context,
	string,
	bool,
	string,
	kernel.Page,
) ([]model.Item, string, error) {
	return nil, "", errFault
}

// failingMemberFolder is a store whose read of any item in b-local fails.
type failingMemberFolder struct{ *inmem.Store }

func (s failingMemberFolder) Items() port.ItemStore { return failedMemberFolder{s.Store.Items()} }

type failedMemberFolder struct{ port.ItemStore }

func (r failedMemberFolder) Get(
	ctx context.Context,
	key string,
	cached bool,
	fullPath string,
) (model.Item, error) {
	if key == "b-local" {
		return model.Item{}, errFault
	}
	return r.ItemStore.Get(ctx, key, cached, fullPath) //nolint:wrapcheck // the store's own answer
}

// TestUnitVirtualIndexMissesAndFaults (#1537): a folder no member holds,
// and the root of a virtual repository without members, are not found, as
// 7.x's isItemNotFoundOnVirtualRepo answers them; a member the store
// cannot read or list fails the index with 500 rather than leaving the
// member out.
func TestUnitVirtualIndexMissesAndFaults(t *testing.T) {
	st := inmem.New()
	w := browseWorld(t, st)
	if err := st.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "none", Class: repo.Virtual,
	})); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kit    *itemkit.Kit
		target string
		want   int
	}{
		{w.Kit(itemkit.Deps{}), "/artifactory/pv/absent/", http.StatusNotFound},
		{w.Kit(itemkit.Deps{}), "/artifactory/none/", http.StatusNotFound},
		{w.Kit(itemkit.Deps{Store: failingMembers{st}}), "/artifactory/libs/", http.StatusInternalServerError},
		{w.Kit(itemkit.Deps{Store: failingChildren{st}}), "/artifactory/pv/", http.StatusInternalServerError},
		{
			w.Kit(itemkit.Deps{Store: failingMemberFolder{st}}),
			"/artifactory/pv/dir/",
			http.StatusInternalServerError,
		},
	} {
		rec := httptest.NewRecorder()
		c.kit.GetItem(rec, kittest.Request(t, http.MethodGet, c.target))
		if rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.target, rec.Code, rec.Body, c.want)
		}
	}
}
