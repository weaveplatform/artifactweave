package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// pushTags uploads imageConfig and layer to the image name of docker-local
// and pushes image(layer) to each tag.
func (w *blobWorld) pushTags(name, layer string, tags ...string) string {
	w.t.Helper()
	w.uploadBlobs(name, imageConfig, layer)
	body := image(layer)
	for _, tag := range tags {
		expect(w.t, w.do(http.MethodPut, "/v2/docker-local/"+name+"/manifests/"+tag, body,
			ociManifest), http.StatusCreated, "")
	}
	return body
}

// listing decodes a tags list or catalog response.
type listing struct {
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	Repositories []string `json:"repositories"`
}

func list(t *testing.T, rec *httptest.ResponseRecorder) listing {
	t.Helper()
	expect(t, rec, http.StatusOK, "")
	var l listing
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return l
}

// Tags are the tag folders holding a manifest, ordered case-insensitively
// with byte order between names differing only in case; digest folders
// and _uploads are not tags. The name is the image as the client named it.
func TestTagsList(t *testing.T) {
	w := newBlobWorld(t)
	body := w.pushTags("team/app", "layer", "v1", "beta", "alpha", "Alpha", "1.0")
	d := digestOf(body)
	expect(t, w.do(http.MethodPut, "/v2/docker-local/team/app/manifests/"+d, body, ociManifest),
		http.StatusCreated, "")
	// An image below this one contributes nothing.
	w.pushTags("team/app/sub", "other", "nested")
	want := []string{"1.0", "Alpha", "alpha", "beta", "v1"}
	for target, name := range map[string]string{
		"/v2/docker-local/team/app/tags/list":                        "docker-local/team/app",
		"/artifactory/api/docker/docker-local/v2/team/app/tags/list": "team/app",
	} {
		l := list(t, w.do(http.MethodGet, target, "", nil))
		if diff := cmp.Diff(listing{Name: name, Tags: want}, l); diff != "" {
			t.Errorf("%s: %s", target, diff)
		}
	}
	l := list(t, w.do(http.MethodGet, "/v2/team/app/tags/list", "",
		map[string]string{"Host": "docker-local.reg.example.com"}))
	if diff := cmp.Diff(listing{Name: "team/app", Tags: want}, l); diff != "" {
		t.Errorf("subdomain: %s", diff)
	}
}

// n limits a page and last continues after a name; a Link names the next
// page while more follow (end-8b).
func TestTagsPagination(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("app", "layer", "a", "b", "c", "d", "e")
	const base = "/v2/docker-local/app/tags/list"
	for _, c := range []struct {
		query string
		tags  []string
		link  string
	}{
		{"", []string{"a", "b", "c", "d", "e"}, ""},
		{"?n=2", []string{"a", "b"}, "<" + base + `?last=b&n=2>; rel="next"`},
		{"?n=2&last=b", []string{"c", "d"}, "<" + base + `?last=d&n=2>; rel="next"`},
		// The exact last page has no Link.
		{"?n=1&last=d", []string{"e"}, ""},
		{"?n=5", []string{"a", "b", "c", "d", "e"}, ""},
		{"?n=9", []string{"a", "b", "c", "d", "e"}, ""},
		// n of zero is an empty page without a Link.
		{"?n=0", []string{}, ""},
		{"?last=e", []string{}, ""},
		{"?last=zzz", []string{}, ""},
		// last need not be a tag of the image.
		{"?last=bb", []string{"c", "d", "e"}, ""},
		// b follows B: names differing only in case order by their bytes.
		{"?last=B&n=1", []string{"b"}, "<" + base + `?last=b&n=1>; rel="next"`},
		// An n that is no count is ignored.
		{"?n=many", []string{"a", "b", "c", "d", "e"}, ""},
		{"?n=-1", []string{"a", "b", "c", "d", "e"}, ""},
	} {
		rec := w.do(http.MethodGet, base+c.query, "", nil)
		l := list(t, rec)
		if diff := cmp.Diff(c.tags, l.Tags); diff != "" {
			t.Errorf("%s: %s", c.query, diff)
		}
		if got := rec.Header().Get("Link"); got != c.link {
			t.Errorf("%s: Link %q, want %q", c.query, got, c.link)
		}
	}
	// The Link is in the form the client addressed.
	rec := w.do(
		http.MethodGet,
		"/artifactory/api/docker/docker-local/v2/app/tags/list?n=4",
		"",
		nil,
	)
	if got := rec.Header().Get("Link"); got !=
		`</artifactory/api/docker/docker-local/v2/app/tags/list?last=d&n=4>; rel="next"` {
		t.Errorf("prefix Link %q", got)
	}
}

// An image the registry stores no manifest of is unknown; one whose
// manifests are all pushed by digest has no tags.
func TestTagsOfUnknownAndUntaggedImages(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("app", imageConfig, "layer")
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil),
		http.StatusNotFound, CodeNameUnknown)
	expect(t, w.do(http.MethodGet, "/v2/docker-local/none/tags/list", "", nil),
		http.StatusNotFound, CodeNameUnknown)
	body := image("layer")
	expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/"+digestOf(body), body,
		ociManifest), http.StatusCreated, "")
	rec := w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil)
	if l := list(t, rec); l.Tags == nil || len(l.Tags) != 0 ||
		!strings.Contains(rec.Body.String(), `"tags":[]`) {
		t.Fatalf("%s", rec.Body)
	}
	expect(t, w.do(http.MethodGet, "/v2/oci-local/app/tags/list", "", nil),
		http.StatusNotFound, CodeNameUnknown)
	expect(t, w.do(http.MethodGet, "/v2/docker-remote/app/tags/list", "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
}

// The registry-wide catalog lists every served repository's images as
// <repo>/<image>; a repository's catalog lists its own by name. Folders no
// client could pull, and repositories the registry does not serve, are
// left out.
func TestCatalogs(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("team/app", "layer", "v1")
	w.pushTags("base", "other", "v1")
	manifest := image("x")
	for _, c := range []struct{ key, path string }{
		{"oci-local", "tools/v1/manifest.json"},
		{"helm-oci", "charts/web/1.0.0/manifest.json"},
		{"fed-docker", "mirror/latest/list.manifest.json"},
		{"generic-local", "plain/v1/manifest.json"},
		// Not images: a name no client sends, a manifest in no tag folder,
		// one in _uploads and one at the top.
		{"docker-local", "Bad_Name/v1/manifest.json"},
		{"docker-local", "loose/manifest.json"},
		{"docker-local", "app/_uploads/manifest.json"},
		{"docker-local", "top/manifest.json"},
	} {
		w.deploy(c.key, c.path, manifest)
	}
	all := []string{
		"docker-local/base", "docker-local/team/app", "fed-docker/mirror", "helm-oci/charts/web",
		"oci-local/tools",
	}
	if diff := cmp.Diff(
		all,
		list(t, w.do(http.MethodGet, "/v2/_catalog", "", nil)).Repositories,
	); diff != "" {
		t.Errorf("registry-wide: %s", diff)
	}
	local := []string{"base", "team/app"}
	for _, c := range []struct{ target, host string }{
		{"/v2/docker-local/_catalog", ""},
		{"/artifactory/api/docker/docker-local/v2/_catalog", ""},
		{"/artifactory/api/docker/DOCKER-LOCAL/v2/_catalog", ""},
		{"/v2/_catalog", "docker-local.reg.example.com"},
	} {
		got := list(t, w.do(http.MethodGet, c.target, "", map[string]string{"Host": c.host}))
		if diff := cmp.Diff(local, got.Repositories); diff != "" {
			t.Errorf("%s %s: %s", c.target, c.host, diff)
		}
	}
	rec := w.do(http.MethodGet, "/v2/_catalog?n=2", "", nil)
	if diff := cmp.Diff(all[:2], list(t, rec).Repositories); diff != "" {
		t.Error(diff)
	}
	if got := rec.Header().Get("Link"); got !=
		`</v2/_catalog?last=docker-local%2Fteam%2Fapp&n=2>; rel="next"` {
		t.Errorf("Link %q", got)
	}
	rec = w.do(http.MethodGet, "/v2/_catalog?n=2&last=docker-local%2Fteam%2Fapp", "", nil)
	if diff := cmp.Diff(all[2:4], list(t, rec).Repositories); diff != "" {
		t.Error(diff)
	}
	// An empty repository's catalog is an empty list.
	rec = w.do(http.MethodGet, "/v2/Mixed-Case/_catalog", "", nil)
	if !strings.Contains(rec.Body.String(), `"repositories":[]`) {
		t.Fatalf("%s", rec.Body)
	}
	expect(t, w.do(http.MethodGet, "/v2/generic-local/_catalog", "", nil),
		http.StatusNotFound, CodeNameUnknown)
	expect(t, w.do(http.MethodGet, "/v2/docker-virtual/_catalog", "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
	expect(t, w.do(http.MethodPost, "/artifactory/api/docker/docker-local/v2/_catalog", "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
}

// A catalog lists every image of a repository the caller can read anything
// in, a reader scoped to one path included, as 7.x's "Repository Metadata
// and Permissions" states; a repository it can read nothing in is left out
// rather than refused. A tags list, like a pull, still needs Read on the
// image.
func TestListingPermissions(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("team/app", "layer", "v1")
	w.pushTags("other", "other", "v1")
	w.deploy("oci-local", "tools/v1/manifest.json", image("x"))
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "team",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"team/**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	as := func(name string) http.Handler { return principalAs(t, w.db, w.h, name) }
	for target, want := range map[string][]string{
		"/v2/_catalog":              {"docker-local/other", "docker-local/team/app"},
		"/v2/docker-local/_catalog": {"other", "team/app"},
		"/v2/oci-local/_catalog":    {},
	} {
		if diff := cmp.Diff(want, list(t, send(as("reader"), http.MethodGet, target, "", nil)).
			Repositories); diff != "" {
			t.Errorf("reader %s: %s", target, diff)
		}
		if got := list(t, send(as("nobody"), http.MethodGet, target, "", nil)); len(
			got.Repositories,
		) != 0 {
			t.Errorf("nobody %s: %v", target, got)
		}
	}
	list(t, send(as("reader"), http.MethodGet, "/v2/docker-local/team/app/tags/list", "", nil))
	expect(t, send(as("reader"), http.MethodGet, "/v2/docker-local/other/tags/list", "", nil),
		http.StatusForbidden, CodeDenied)
	expect(t, send(as(auth.AnonymousUser), http.MethodGet, "/v2/docker-local/other/tags/list",
		"", nil), http.StatusUnauthorized, CodeUnauthorized)
}

// A tag delete removes the tag folder; the manifest stays readable by
// digest from any other folder holding it. A digest delete removes every
// folder holding the manifest, its tags with it (end-9).
func TestDeleteManifests(t *testing.T) {
	w := newBlobWorld(t)
	body := w.pushTags("app", "layer", "v1", "v2")
	d := digestOf(body)
	const base = "/v2/docker-local/app/manifests/"
	expect(t, w.do(http.MethodPut, base+d, body, ociManifest), http.StatusCreated, "")
	rec := w.do(http.MethodDelete, base+"v1", "", nil)
	expect(t, rec, http.StatusAccepted, "")
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q", rec.Body)
	}
	expect(t, w.do(http.MethodGet, base+"v1", "", nil), http.StatusNotFound, CodeManifestUnknown)
	expect(t, w.do(http.MethodGet, base+d, "", nil), http.StatusOK, "")
	expect(t, w.do(http.MethodGet, base+"v2", "", nil), http.StatusOK, "")
	if l := list(t, w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil)); !cmp.Equal(
		l.Tags, []string{"v2"},
	) {
		t.Fatalf("tags %v", l.Tags)
	}
	expect(t, w.do(http.MethodDelete, "/artifactory/api/docker/docker-local/v2/app/manifests/"+d,
		"", nil), http.StatusAccepted, "")
	for _, ref := range []string{d, "v2"} {
		expect(t, w.do(http.MethodGet, base+ref, "", nil), http.StatusNotFound, CodeManifestUnknown)
	}
	for _, folder := range []string{"app/v2", "app/" + d} {
		if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
			folder); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v", folder, err)
		}
	}
	// The uploads stay; the image has no manifest left.
	if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
		"app/_uploads"); err != nil {
		t.Fatal(err)
	}
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil),
		http.StatusNotFound, CodeNameUnknown)
}

// What names no stored manifest answers MANIFEST_UNKNOWN, and a reserved
// name never reaches the folder it names.
func TestDeleteUnknownManifests(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("app", "layer", "v1")
	const base = "/v2/docker-local/app/manifests/"
	for _, ref := range []string{
		"v9", digestOf("absent"), "sha256:totallywrong", "sha512:" + strings.Repeat("a", 128),
		"-v1", "_uploads", "sha256__" + digestOf("layer")[7:],
	} {
		expect(t, w.do(http.MethodDelete, base+ref, "", nil), http.StatusNotFound,
			CodeManifestUnknown)
	}
	expect(t, w.do(http.MethodDelete, "/v2/docker-local/none/manifests/v1", "", nil),
		http.StatusNotFound, CodeManifestUnknown)
	if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
		"app/_uploads"); err != nil {
		t.Fatal(err)
	}
	expect(t, w.do(http.MethodGet, base+"v1", "", nil), http.StatusOK, "")
}

// A delete needs Delete on the image.
func TestDeletePermissions(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("app", "layer", "v1", "v2")
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "docker",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"reader": types.ActionRead | types.ActionWrite,
				"owner":  types.ActionRead | types.ActionDelete,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	as := func(name string) http.Handler { return principalAs(t, w.db, w.h, name) }
	const v1 = "/v2/docker-local/app/manifests/v1"
	expect(t, send(as("reader"), http.MethodDelete, v1, "", nil), http.StatusForbidden, CodeDenied)
	expect(t, send(as(auth.AnonymousUser), http.MethodDelete, v1, "", nil),
		http.StatusUnauthorized, CodeUnauthorized)
	// Unknown or not, a caller without Delete learns nothing.
	expect(t, send(as("reader"), http.MethodDelete, "/v2/docker-local/app/manifests/v9", "", nil),
		http.StatusForbidden, CodeDenied)
	expect(t, send(as("owner"), http.MethodDelete, v1, "", nil), http.StatusAccepted, "")
}

func TestListingAndDeleteStoreFailures(t *testing.T) {
	for name, c := range map[string]struct {
		tune           func(*blobWorld, *Options)
		method, target string
	}{
		"tags": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		}, http.MethodGet, "/v2/docker-local/app/tags/list"},
		"catalog": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		}, http.MethodGet, "/v2/docker-local/_catalog"},
		"repositories": {func(w *blobWorld, o *Options) {
			o.Store = faultyStore{Store: w.db, repos: faultyRepos{RepoStore: w.db.Repos(), list: true}}
		}, http.MethodGet, "/v2/_catalog"},
		"delete a tag": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingGets{w.db.Items()}}
		}, http.MethodDelete, "/v2/docker-local/app/manifests/v1"},
		"delete a digest": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		}, http.MethodDelete, "/v2/docker-local/app/manifests/" + digestOf(image("layer"))},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			w.pushTags("app", "layer", "v1")
			var noted error
			o := Options{
				Store: w.db, Blobs: w.blobs,
				Fault: func(_ *http.Request, err error) { noted = err },
			}
			c.tune(w, &o)
			rec := send(router(New(o)), c.method, c.target, "", nil)
			if rec.Code != http.StatusInternalServerError || !errors.Is(noted, errBoom) {
				t.Fatalf("%d %s, noted %v", rec.Code, rec.Body, noted)
			}
		})
	}
}

// Without a blob store the registry still lists, from the layout alone.
func TestListingsWithoutBlobs(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("app", "layer", "v1")
	h := router(New(Options{Store: w.db}))
	if l := list(t, send(h, http.MethodGet, "/v2/docker-local/app/tags/list", "", nil)); !cmp.Equal(
		l.Tags, []string{"v1"},
	) {
		t.Fatalf("%v", l)
	}
	expect(t, send(h, http.MethodDelete, "/v2/docker-local/app/manifests/v1", "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
}

// A registry token lists a catalog only when it grants registry:catalog:*;
// without it the answer is 401, which the challenge turns into a request
// for that scope.
func TestCatalogNeedsTheCatalogGrant(t *testing.T) {
	w := newBlobWorld(t)
	w.pushTags("team/app", "layer", "v1")
	catalog := types.RegistryGrant{Type: scopeRegistry, Name: "catalog", Actions: []string{"*"}}
	for name, c := range map[string]struct {
		grants []types.RegistryGrant
		want   int
	}{
		"not granted": {nil, http.StatusUnauthorized},
		"granted":     {[]types.RegistryGrant{catalog}, http.StatusOK},
	} {
		for _, target := range []string{"/v2/_catalog", "/v2/docker-local/_catalog"} {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req = req.WithContext(auth.WithPrincipal(req.Context(),
				auth.Principal{Registry: &types.RegistryAccess{Grants: c.grants}}))
			rec := httptest.NewRecorder()
			w.h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("%s %s: %d %s", name, target, rec.Code, rec.Body)
			}
		}
	}
}
