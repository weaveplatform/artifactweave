package artifacts_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// virtualStorageBase is the URL the remote world's router answers under.
const virtualStorageBase = "http://example.com/artifactory/"

// upstreamIndex is a directory index of links, as an upstream server
// serves a folder.
func upstreamIndex(links ...string) fakeupstream.Resource {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><body><pre>")
	for _, l := range links {
		fmt.Fprintf(&b, "<a href=%q>%s</a>\n", l, l)
	}
	b.WriteString("</pre></body></html>")
	return fakeupstream.Resource{
		Body: []byte(b.String()), Header: http.Header{"Content-Type": {"text/html"}},
	}
}

// virtualStorageWorld is a remote world, anonymous access on, whose remote
// lists remote folder items and whose upstream serves x.bin, dir/c.bin and
// the indexes of "/", dir and only; the locals a-local and b-local; vb,
// a virtual repository over remote, a-local and b-local that excludes
// **/*.secret; outer, a virtual repository over vb; empty, one without
// members; nostore, a remote that keeps no cache; and breader, who reads
// b-local alone. Each file holds "<repo>/<path>":
//
//	a-local: x.bin, p.bin, q.bin (m=1), dir/a.bin, dir/.index/i.bin, dir/dup
//	b-local: p.bin (k=v), q.bin (k=v), dir/b.bin, dir/x.secret, dir/dup/f.bin
//	remote's cache: x.bin, dir/c.bin
//	upstream only: live-root.bin, only/o.bin, dir/live.bin, dir/y.secret, dir/sub/
func virtualStorageWorld(t *testing.T) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	ctx := t.Context()
	w.Upstream.Set("/x.bin", fakeupstream.Resource{Body: []byte("remote/x.bin")})
	w.Upstream.Set("/dir/c.bin", fakeupstream.Resource{Body: []byte("remote/dir/c.bin")})
	w.Upstream.Set("/", upstreamIndex("x.bin", "dir/", "only/", "live-root.bin"))
	w.Upstream.Set("/dir/", upstreamIndex("c.bin", "live.bin", "y.secret", "sub/"))
	w.Upstream.Set("/only/", upstreamIndex("o.bin"))
	w.Repo.ListRemoteFolderItems = repo.Flex(true)
	w.Update()
	for _, c := range []repo.Config{
		{Key: "a-local", Class: repo.Local},
		{Key: "b-local", Class: repo.Local},
		{
			Key: "nostore", Class: repo.Remote, URL: w.Upstream.URL,
			StoreArtifactsLocally: ptr(false),
		},
		{
			Key: "vb", Class: repo.Virtual, Repositories: []string{"remote", "a-local", "b-local"},
			ExcludesPattern: ptr("**/*.secret"),
		},
		{Key: "outer", Class: repo.Virtual, Repositories: []string{"vb"}},
		{Key: "empty", Class: repo.Virtual},
	} {
		if err := w.Runtime.Store.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Runtime.Store.Users().
		Create(ctx, accesstest.User{Name: "breader", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().Create(ctx, accesstest.Permission{
		Name: "b-read", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"breader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"b-local": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Settings().
		Put(ctx, settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"a-local/x.bin", "a-local/p.bin", "a-local/q.bin;m=1",
		"a-local/dir/a.bin", "a-local/dir/.index/i.bin", "a-local/dir/dup",
		"b-local/p.bin;k=v", "b-local/q.bin;k=v", "b-local/dir/b.bin", "b-local/dir/x.secret",
		"b-local/dir/dup/f.bin",
	} {
		file, _, _ := strings.Cut(p, ";")
		if rec := w.Request(http.MethodPut, "/artifactory/"+p, "admin", file); rec.Code !=
			http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"x.bin", "dir/c.bin"} {
		if rec := w.Request(http.MethodGet, "/artifactory/remote/"+p, "admin", ""); rec.Code !=
			http.StatusOK {
			t.Fatalf("cache %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	return w
}

// virtualStorageJSON is the JSON object w answers GET target with as as,
// failing t unless it answers 200.
func virtualStorageJSON(t *testing.T, w *harness.RemoteWorld, as, target string) map[string]any {
	t.Helper()
	rec := w.Request(http.MethodGet, "/artifactory/"+target, as, "")
	var body map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("%s as %q: %d %s", target, as, rec.Code, rec.Body)
	}
	return body
}

// wantVirtualStorageError fails t unless each target answers as as with
// status and message in the error envelope.
func wantVirtualStorageError(
	t *testing.T,
	w *harness.RemoteWorld,
	status int,
	message string,
	requests ...[2]string,
) {
	t.Helper()
	for _, rq := range requests {
		rec := w.Request(http.MethodGet, "/artifactory/"+rq[1], rq[0], "")
		if rec.Code != status || rec.Body.String() != errorBody(t, status, message) {
			t.Errorf("%s as %q: %d %s, want %d %q", rq[1], rq[0], rec.Code, rec.Body, status, message)
		}
	}
}

// entryURIs are the "uri" fields of the objects in list.
func entryURIs(list any) []string {
	var out []string
	for _, e := range list.([]any) {
		out = append(out, e.(map[string]any)["uri"].(string))
	}
	return out
}

// TestAccVirtualStorageItemInfo (#1537): the item info of a virtual
// repository's path describes the first item, locals before caches
// (VirtualRepo.getResolvedLocalFederatedAndCachedRepos), that a member the
// caller may read holds, under the virtual key, as 7.161.26's
// prepareStorageInfoResponse does. Nothing found, or nothing the caller may
// read, answers getVirtualRepoItem's null, an empty 404 that
// RestResponseFilter fills with "Not Found": the root included, which asks
// no Read of the virtual repository, and the root of one without members.
// A folder lists the union of its members' local and cache children, never
// a live upstream's, and never ".index" (getChildrenNamesDeeply). The file
// list keeps working through a nested virtual repository (RTDEV-62248).
// Before, a miss answered "Could not find resource", a root the caller
// could read in no member 403 or 401, and a folder listed ".index".
func TestAccVirtualStorageItemInfo(t *testing.T) {
	w := virtualStorageWorld(t)
	info := virtualStorageJSON(t, w, "admin", "api/storage/vb/x.bin")
	if info["repo"] != "vb" || info["path"] != "/x.bin" ||
		info["uri"] != virtualStorageBase+"api/storage/vb/x.bin" ||
		info["downloadUri"] != virtualStorageBase+"vb/x.bin" ||
		info["checksums"].(map[string]any)["sha1"] != sha1Hex("a-local/x.bin") {
		t.Errorf("vb/x.bin: %v, want a-local's file under vb", info)
	}
	wantVirtualStorageError(t, w, http.StatusNotFound, "Not Found",
		[2]string{"breader", "api/storage/vb/x.bin"},
		[2]string{"", "api/storage/vb/dir/a.bin"},
		[2]string{"admin", "api/storage/vb/absent.bin"},
		[2]string{"admin", "api/storage/vb/only"},
		[2]string{"denied", "api/storage/vb"},
		[2]string{"", "api/storage/vb"},
		[2]string{"admin", "api/storage/empty"},
	)
	for as, want := range map[string][]string{
		"admin":   {"/a.bin", "/dup", "/b.bin", "/x.secret", "/c.bin"},
		"breader": {"/b.bin", "/dup", "/x.secret"},
	} {
		folder := virtualStorageJSON(t, w, as, "api/storage/vb/dir")
		if got := entryURIs(folder["children"]); folder["repo"] != "vb" || !slices.Equal(got, want) {
			t.Errorf("vb/dir as %s: %v, want children %v", as, folder, want)
		}
	}
	root := virtualStorageJSON(t, w, "breader", "api/storage/vb")
	if got, want := entryURIs(root["children"]), []string{"/dir", "/p.bin", "/q.bin"}; !slices.Equal(
		got,
		want,
	) {
		t.Errorf("vb as breader: %v, want %v", got, want)
	}
	list := virtualStorageJSON(t, w, "admin", "api/storage/outer/?list&deep=1")
	want := []string{
		"/dir/.index/i.bin", "/dir/a.bin", "/dir/b.bin", "/dir/c.bin", "/dir/dup",
		"/dir/dup/f.bin", "/dir/x.secret", "/p.bin", "/q.bin", "/x.bin",
	}
	if got := entryURIs(list["files"]); !slices.Equal(got, want) {
		t.Errorf("outer's deep list: %v, want %v", got, want)
	}
}

// TestAccVirtualStorageNotLocalViews (#1537): the last modified and
// permissions views take only a local repository or a cache
// (ArtifactResource.isLocalRepo), so a virtual repository's path is
// answered 400 whoever asks, before any right is weighed, as is a remote
// that keeps no cache; a remote that keeps one answers as its cache, by
// either key (fixPathParamterIfNeededForRemoteCached). Before, a virtual
// repository's described its first member's item, and refused a caller
// without the right.
func TestAccVirtualStorageNotLocalViews(t *testing.T) {
	w := virtualStorageWorld(t)
	const notLocal = "This method can only be invoked on local/cached repositories."
	for _, view := range []string{"lastModified", "permissions"} {
		wantVirtualStorageError(t, w, http.StatusBadRequest, notLocal,
			[2]string{"admin", "api/storage/vb/x.bin?" + view},
			[2]string{"admin", "api/storage/vb?" + view},
			[2]string{"denied", "api/storage/vb/x.bin?" + view},
			[2]string{"", "api/storage/vb/x.bin?" + view},
			[2]string{"admin", "api/storage/outer/absent?" + view},
			[2]string{"admin", "api/storage/nostore/x.bin?" + view},
		)
		for _, key := range []string{"remote", "remote-cache"} {
			body := virtualStorageJSON(t, w, "admin", "api/storage/"+key+"/x.bin?"+view)
			if body["uri"] != virtualStorageBase+"api/storage/remote-cache/x.bin" {
				t.Errorf("%s/x.bin?%s: %v, want the cache's", key, view, body)
			}
		}
	}
}

// TestAccVirtualStorageProperties (#1537): the properties view of a
// virtual repository's path reads the first member's item, in the item
// info's order, whose properties are not empty, then keeps the keys the
// request names (ArtifactResource.resolveProperties, getPropertiesResponse),
// so a key only a later member's item has is not found. A member the caller
// may not read gives no properties (PropertiesServiceImpl's canRead).
// Before, the first member that held the item answered, empty or not.
func TestAccVirtualStorageProperties(t *testing.T) {
	w := virtualStorageWorld(t)
	for _, c := range []struct {
		as, target, key, value string
	}{
		{"admin", "vb/p.bin", "k", "v"},
		{"admin", "vb/p.bin?properties=k", "k", "v"},
		{"admin", "vb/q.bin", "m", "1"},
		{"breader", "vb/q.bin", "k", "v"},
		{"breader", "outer/q.bin", "k", "v"},
	} {
		target := c.target
		if !strings.Contains(target, "?") {
			target += "?properties"
		}
		body := virtualStorageJSON(t, w, c.as, "api/storage/"+target)
		props := body["properties"].(map[string]any)
		name, _, _ := strings.Cut(c.target, "?")
		if len(props) != 1 || !slices.Equal(props[c.key].([]any), []any{c.value}) ||
			body["uri"] != virtualStorageBase+"api/storage/"+name {
			t.Errorf("%s as %s: %v, want %s=%s", c.target, c.as, body, c.key, c.value)
		}
	}
	wantVirtualStorageError(t, w, http.StatusNotFound, "No properties could be found.",
		[2]string{"admin", "api/storage/vb/q.bin?properties=k"},
		[2]string{"admin", "api/storage/vb/x.bin?properties"},
		[2]string{"admin", "api/storage/vb/absent?properties"},
		[2]string{"denied", "api/storage/vb/p.bin?properties"},
		[2]string{"", "api/storage/vb/p.bin?properties"},
	)
}

// indexLinks are the link texts of an HTML folder index.
var indexLinks = regexp.MustCompile(`<a href="[^"]*">([^<]*)</a>`)

// TestAccVirtualBrowseListsEveryMember (#1537): a folder GET on a virtual
// repository, its root included, lists every member's children that the
// caller may read and the member's patterns accept, a remote member's
// cache and, as it lists remote folder items, its upstream's, as 7.161.26's
// getVirtualRepoBrowsableChildrenData merges them; a child the virtual
// repository's patterns reject is left out (virtualRepoAccepts), a name
// several members hold shows once, a folder when any member's is one, and
// the index names the virtual repository. A folder only the upstream lists
// is listed too; one no member holds, and the root of a virtual repository
// without members, are not found (isItemNotFoundOnVirtualRepo). The gate
// before the index keeps its refusals. Before, the index listed the first
// member that held the folder under its own key, the root nothing, and a
// remote member's upstream never. AQL keeps to the members' stored items.
func TestAccVirtualBrowseListsEveryMember(t *testing.T) {
	w := virtualStorageWorld(t)
	for _, c := range []struct {
		as, folder string
		want       []string
	}{
		{"admin", "vb/", []string{"dir/", "live-root.bin", "only/", "p.bin", "q.bin", "x.bin"}},
		{"admin", "vb/dir/", []string{".index/", "a.bin", "b.bin", "c.bin", "dup/", "live.bin", "sub/"}},
		{"admin", "vb/dir", []string{".index/", "a.bin", "b.bin", "c.bin", "dup/", "live.bin", "sub/"}},
		{"admin", "outer/dir/", []string{".index/", "a.bin", "b.bin", "c.bin", "dup/", "live.bin", "sub/"}},
		{"breader", "vb/dir/", []string{"b.bin", "dup/"}},
		{"reader", "vb/dir/", []string{"c.bin", "live.bin", "sub/"}},
		{"admin", "vb/only/", []string{"o.bin"}},
	} {
		rec := w.Request(http.MethodGet, "/artifactory/"+c.folder, c.as, "")
		var got []string
		for _, m := range indexLinks.FindAllStringSubmatch(rec.Body.String(), -1) {
			got = append(got, m[1])
		}
		title := "<title>Index of " + strings.TrimSuffix(c.folder, "/") + "</title>"
		if rec.Code != http.StatusOK || !slices.Equal(got, c.want) ||
			!strings.Contains(rec.Body.String(), title) ||
			rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s as %s: %d %v\n%s, want %v", c.folder, c.as, rec.Code, got, rec.Body, c.want)
		}
	}
	if rec := w.Request(http.MethodHead, "/artifactory/vb/dir/", "admin", ""); rec.Code !=
		http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD vb/dir/: %d %q", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		as, folder string
		status     int
		message    string
	}{
		{"admin", "vb/none/", http.StatusNotFound, "Could not find resource"},
		{"admin", "empty/", http.StatusNotFound, "Could not find resource"},
		{"denied", "vb/dir/", http.StatusForbidden, "Forbidden"},
		{"denied", "vb/", http.StatusForbidden, "Forbidden"},
	} {
		rec := w.Request(http.MethodGet, "/artifactory/"+c.folder, c.as, "")
		if rec.Code != c.status || rec.Body.String() != errorBody(t, c.status, c.message) {
			t.Errorf("%s as %s: %d %s, want %d %q", c.folder, c.as, rec.Code, rec.Body,
				c.status, c.message)
		}
	}
	rec := w.Request(http.MethodPost, "/artifactory/api/search/aql", "admin",
		`items.find({"repo":"vb","path":"dir"}).include("name")`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"c.bin"`) ||
		strings.Contains(rec.Body.String(), "live.bin") {
		t.Errorf("AQL over vb/dir: %d %s, want the cache's c.bin and no live.bin", rec.Code, rec.Body)
	}
}

// TestAccVirtualBrowseUpstreamFailure (#1537): a remote member whose
// upstream fails to list a folder leaves only its own live children out
// of a virtual repository's index; the other members still list [I], as
// 7.x's RemoteRepo.listRemoteResources answers a failure with no items
// (RTFACT-6372). The remote's own index still answers the failure.
func TestAccVirtualBrowseUpstreamFailure(t *testing.T) {
	w := virtualStorageWorld(t)
	w.Upstream.Set("/dir/", fakeupstream.Resource{Status: http.StatusBadGateway})
	rec := w.Request(http.MethodGet, "/artifactory/vb/dir/", "admin", "")
	var got []string
	for _, m := range indexLinks.FindAllStringSubmatch(rec.Body.String(), -1) {
		got = append(got, m[1])
	}
	if want := []string{".index/", "a.bin", "b.bin", "c.bin", "dup/"}; rec.Code != http.StatusOK ||
		!slices.Equal(got, want) {
		t.Errorf("vb/dir/ with its upstream failing: %d %v, want %v", rec.Code, got, want)
	}
	if rec := w.Request(http.MethodGet, "/artifactory/remote/dir/", "admin", ""); rec.Code <
		http.StatusInternalServerError {
		t.Errorf("remote/dir/ with its upstream failing: %d %s", rec.Code, rec.Body)
	}
}
