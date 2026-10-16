package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// imageConfig is an image's config with a label, as Docker writes it.
const imageConfig = `{"architecture":"amd64","os":"linux","config":{"Labels":{"team":"platform"}}}`

// image returns an OCI image manifest of imageConfig and layers.
func image(layers ...string) string {
	var ls []string
	for _, l := range layers {
		ls = append(ls, `{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"`+
			digestOf(l)+`","size":`+strconv.Itoa(len(l))+`}`)
	}
	return `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIManifest + `",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` +
		digestOf(imageConfig) + `","size":` + strconv.Itoa(len(imageConfig)) + `},` +
		`"layers":[` + strings.Join(ls, ",") + `]}`
}

var ociManifest = map[string]string{"Content-Type": layout.MediaTypeOCIManifest}

// uploadBlobs uploads each content to the image name of docker-local.
func (w *blobWorld) uploadBlobs(name string, contents ...string) {
	w.t.Helper()
	for _, c := range contents {
		expect(w.t, w.do(http.MethodPost,
			"/v2/docker-local/"+name+"/blobs/uploads/?digest="+digestOf(c), c, nil),
			http.StatusCreated, "")
	}
}

func TestPushAndPullByTagAndDigest(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("team/app", imageConfig, "layer")
	body := image("layer")
	d := digestOf(body)
	rec := w.do(http.MethodPut, "/v2/docker-local/team/app/manifests/v1", body, ociManifest)
	expect(t, rec, http.StatusCreated, "")
	if rec.Header().Get("Location") != "/v2/docker-local/team/app/manifests/"+d ||
		rec.Header().Get(contentDigestHeader) != d || rec.Header().Get(tagHeader) != "" {
		t.Fatalf("headers %v", rec.Header())
	}
	for _, target := range []string{
		"/v2/docker-local/team/app/manifests/v1",
		"/v2/docker-local/team/app/manifests/" + d,
		"/artifactory/api/docker/docker-local/v2/team/app/manifests/v1",
		"/artifactory/api/docker/docker-local/v2/team/app/manifests/" + d,
	} {
		get := w.do(http.MethodGet, target, "", map[string]string{
			"Accept": layout.MediaTypeDockerManifest + ", " + layout.MediaTypeOCIManifest,
		})
		expect(t, get, http.StatusOK, "")
		h := get.Header()
		if get.Body.String() != body || h.Get("Content-Type") != layout.MediaTypeOCIManifest ||
			h.Get(contentDigestHeader) != d || h.Get("ETag") != `"`+d+`"` {
			t.Fatalf("%s: %v %q", target, h, get.Body)
		}
		head := w.do(http.MethodHead, target, "", nil)
		expect(t, head, http.StatusOK, "")
		if head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(body)) ||
			head.Header().Get(contentDigestHeader) != d {
			t.Fatalf("HEAD %s: %v", target, head.Header())
		}
	}
	// A conditional GET with the digest as ETag is answered from the cache.
	expect(t, w.do(http.MethodGet, "/v2/docker-local/team/app/manifests/v1", "",
		map[string]string{"If-None-Match": `"` + d + `"`}), http.StatusNotModified, "")
}

// A push by digest stores the digest folder and each ?tag= (end-7b), and
// names the tags it stored.
func TestPushByDigestWithTags(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("app", imageConfig, "layer")
	body := image("layer")
	d := digestOf(body)
	rec := w.do(http.MethodPut,
		"/v2/docker-local/app/manifests/"+d+"?tag=a&tag=b&tag=a", body, ociManifest)
	expect(t, rec, http.StatusCreated, "")
	if diff := cmp.Diff([]string{"a", "b"}, rec.Header().Values(tagHeader)); diff != "" {
		t.Fatal(diff)
	}
	for _, ref := range []string{"a", "b", d} {
		expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/"+ref, "", nil),
			http.StatusOK, "")
	}
	// The digest folder is not a tag.
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/sha256__"+d[7:], "", nil),
		http.StatusNotFound, CodeManifestUnknown)
	if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
		"app/sha256__"+d[7:]+"/manifest.json"); err != nil {
		t.Fatal(err)
	}
}

func TestManifestPushErrors(t *testing.T) {
	body := image("layer")
	d := digestOf(body)
	schema1 := map[string]string{
		"Content-Type": "application/vnd.docker.distribution.manifest.v1+prettyjws",
	}
	index := `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIIndex + `","manifests":[]}`
	huge := strings.Repeat(" ", layout.MaxManifestSize+1)
	const base = "/v2/docker-local/app/manifests/"
	for name, c := range map[string]struct {
		ref, body string
		headers   map[string]string
		status    int
		code      string
	}{
		"unknown blob":       {"v1", image("absent"), ociManifest, 400, CodeManifestBlobUnknown},
		"wrong media type":   {"v1", body, map[string]string{"Content-Type": "text/plain"}, 400, CodeManifestInvalid},
		"schema 1":           {"v1", body, schema1, 400, CodeManifestInvalid},
		"not JSON":           {"v1", "nope", ociManifest, 400, CodeManifestInvalid},
		"index":              {"v1", index, map[string]string{"Content-Type": layout.MediaTypeOCIIndex}, 400, CodeManifestInvalid},
		"too large":          {"v1", huge, ociManifest, 413, CodeSizeInvalid},
		"invalid tag":        {"-v1", body, ociManifest, 400, CodeManifestInvalid},
		"uploads tag":        {"_uploads", body, ociManifest, 400, CodeTagInvalid},
		"digest folder tag":  {"sha256__" + d[7:], body, ociManifest, 400, CodeTagInvalid},
		"invalid digest":     {"sha256:totallywrong", body, ociManifest, 400, CodeManifestInvalid},
		"other algorithm":    {"sha512:" + strings.Repeat("a", 128), body, ociManifest, 400, CodeDigestInvalid},
		"digest mismatch":    {digestOf("other"), body, ociManifest, 400, CodeDigestInvalid},
		"invalid extra tag":  {d + "?tag=-x", body, ociManifest, 400, CodeManifestInvalid},
		"reserved extra tag": {d + "?tag=_uploads", body, ociManifest, 400, CodeTagInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			w.uploadBlobs("app", imageConfig, "layer")
			expect(t, w.do(http.MethodPut, base+c.ref, c.body, c.headers), c.status, c.code)
		})
	}
}

// Each blob a manifest needs and the image lacks is its own error, with
// the digest as detail.
func TestEveryUnknownBlobIsNamed(t *testing.T) {
	w := newBlobWorld(t)
	rec := w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", image("a", "b"), ociManifest)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Errors []struct{ Code, Message, Detail string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, e := range got.Errors {
		if e.Code != CodeManifestBlobUnknown || e.Message == "" {
			t.Fatalf("%+v", e)
		}
		details = append(details, e.Detail)
	}
	want := []string{digestOf(imageConfig), digestOf("a"), digestOf("b")}
	if diff := cmp.Diff(want, details); diff != "" {
		t.Fatal(diff)
	}
}

// A body of unknown length is cut at the limit while it is read, and one
// that cannot be read is refused.
func TestManifestBodyLimits(t *testing.T) {
	w := newBlobWorld(t)
	req := httptest.NewRequest(http.MethodPut, "/v2/docker-local/app/manifests/v1",
		strings.NewReader(strings.Repeat(" ", layout.MaxManifestSize+1)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusRequestEntityTooLarge, CodeSizeInvalid)
	req = httptest.NewRequest(http.MethodPut, "/v2/docker-local/app/manifests/v1",
		iotest.ErrReader(errBoom))
	rec = httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	expect(t, rec, http.StatusBadRequest, CodeManifestInvalid)
}

func TestManifestReadErrors(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("app", imageConfig, "layer")
	expect(
		t,
		w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", image("layer"), ociManifest),
		http.StatusCreated,
		"",
	)
	const base = "/v2/docker-local/app/manifests/"
	for _, ref := range []string{
		"v2", digestOf("absent"), "sha256:totallywrong", "sha512:" + strings.Repeat("a", 128),
		"-v1", "_uploads",
	} {
		expect(t, w.do(http.MethodGet, base+ref, "", nil), http.StatusNotFound, CodeManifestUnknown)
	}
	// A client that lists manifest types but not this one cannot read it.
	expect(t, w.do(http.MethodGet, base+"v1", "", map[string]string{
		"Accept": layout.MediaTypeDockerManifest,
	}), http.StatusNotFound, CodeManifestUnknown)
	for _, accept := range []string{"*/*", "application/json", "application/*;q=0.5", "bad;=", ""} {
		expect(t, w.do(http.MethodGet, base+"v1", "", map[string]string{"Accept": accept}),
			http.StatusOK, "")
	}
	// Delete arrives with the tag and catalog work.
	expect(t, w.do(http.MethodDelete, base+"v1", "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
}

// A manifest deployed through the REST API has no type property; its own
// mediaType, or its file name, names its type.
func TestManifestTypeWithoutProperty(t *testing.T) {
	w := newBlobWorld(t)
	docker := `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeDockerManifest + `"}`
	w.deploy("docker-local", "app/a/manifest.json", docker)
	w.deploy("docker-local", "app/b/manifest.json", `{"schemaVersion":2}`)
	w.deploy("docker-local", "app/c/list.manifest.json", `{"schemaVersion":2}`)
	for tag, want := range map[string]string{
		"a": layout.MediaTypeDockerManifest,
		"b": layout.MediaTypeOCIManifest,
		"c": layout.MediaTypeOCIIndex,
	} {
		rec := w.do(http.MethodGet, "/v2/docker-local/app/manifests/"+tag, "", nil)
		expect(t, rec, http.StatusOK, "")
		if rec.Header().Get("Content-Type") != want || !strings.HasPrefix(rec.Body.String(), "{") {
			t.Errorf("%s: %q %q", tag, rec.Header().Get("Content-Type"), rec.Body)
		}
	}
}

func TestAFileWhereTheTagGoes(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("app", imageConfig, "layer")
	w.deploy("docker-local", "app/v1", "a file")
	expect(
		t,
		w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", image("layer"), ociManifest),
		http.StatusBadRequest,
		CodeNameInvalid,
	)
}

// Pulls need Read and pushes Deploy; pushing over a tag that holds another
// manifest needs Delete too (DV-035), and repeating a push does not.
func TestManifestPermissions(t *testing.T) {
	w := newBlobWorld(t)
	ctx := context.Background()
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "docker",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"reader": types.ActionRead,
				"writer": types.ActionRead | types.ActionWrite,
				"owner":  types.ActionRead | types.ActionWrite | types.ActionDelete,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	w.uploadBlobs("app", imageConfig, "one", "two")
	first, second := image("one"), image("two")
	as := func(name string) http.Handler { return principalAs(t, w.db, w.h, name) }
	const v1 = "/v2/docker-local/app/manifests/v1"
	for _, c := range []struct {
		who, method, target, body string
		status                    int
		code                      string
	}{
		{"writer", "PUT", v1, first, 201, ""},
		{"reader", "GET", v1, "", 200, ""},
		{"nobody", "GET", v1, "", 403, CodeDenied},
		{auth.AnonymousUser, "GET", v1, "", 401, CodeUnauthorized},
		{"reader", "PUT", "/v2/docker-local/app/manifests/v2", first, 403, CodeDenied},
		{auth.AnonymousUser, "PUT", "/v2/docker-local/app/manifests/v2", first, 401, CodeUnauthorized},
		// Repeating the push changes nothing, so Deploy is enough.
		{"writer", "PUT", v1, first, 201, ""},
		{"writer", "PUT", v1, second, 403, CodeDenied},
		{"writer", "PUT", "/v2/docker-local/app/manifests/" + digestOf(second) + "?tag=v1", second, 403, CodeDenied},
		{"owner", "PUT", v1, second, 201, ""},
	} {
		rec := send(as(c.who), c.method, c.target, c.body, ociManifest)
		if rec.Code != c.status || code(t, rec) != c.code {
			t.Errorf("%s %s %s: %d %s", c.who, c.method, c.target, rec.Code, rec.Body)
		}
	}
	// The refused push by digest stored nothing.
	if _, err := w.db.Items().Get(ctx, "docker-local", false,
		"app/sha256__"+digestOf(second)[7:]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stored: %v", err)
	}
}

// failingProperties fails reading properties.
type failingProperties struct{ store.ItemStore }

func (failingProperties) Properties(context.Context, int64) (types.Properties, error) {
	return nil, errBoom
}

// failingGets fails Get for every path below an image's tag folders.
type failingGets struct{ store.ItemStore }

func (f failingGets) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if strings.Count(p, "/") >= 1 {
		return types.Item{}, errBoom
	}
	return f.ItemStore.Get(ctx, key, cached, p) //nolint:wrapcheck // test double
}

func TestManifestStoreFailures(t *testing.T) {
	body := image("layer")
	d := digestOf(body)
	for name, c := range map[string]struct {
		tune           func(*blobWorld, *Options)
		method, target string
	}{
		"find by digest": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		}, http.MethodGet, "/v2/docker-local/app/manifests/" + d},
		"find by tag": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingGets{w.db.Items()}}
		}, http.MethodGet, "/v2/docker-local/app/manifests/v1"},
		"properties": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingProperties{w.db.Items()}}
		}, http.MethodGet, "/v2/docker-local/app/manifests/v1"},
		"open": {func(w *blobWorld, o *Options) {
			o.Blobs = failingBlobs{w.blobs}
		}, http.MethodGet, "/v2/docker-local/app/manifests/v1"},
		"look at the tag": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingGets{w.db.Items()}}
		}, http.MethodPut, "/v2/docker-local/app/manifests/v2"},
		"store": {func(w *blobWorld, o *Options) {
			o.Blobs = failingBlobs{w.blobs}
		}, http.MethodPut, "/v2/docker-local/app/manifests/v2"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			w.uploadBlobs("app", imageConfig, "layer")
			expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", body, ociManifest),
				http.StatusCreated, "")
			var noted error
			o := Options{
				Store: w.db, Blobs: w.blobs,
				Fault: func(_ *http.Request, err error) { noted = err },
			}
			c.tune(w, &o)
			rec := send(router(New(o)), c.method, c.target, body, ociManifest)
			if rec.Code != http.StatusInternalServerError || noted == nil {
				t.Fatalf("%d %s, noted %v", rec.Code, rec.Body, noted)
			}
		})
	}
}

func TestAccepts(t *testing.T) {
	const oci = layout.MediaTypeOCIManifest
	for _, c := range []struct {
		accept []string
		want   bool
	}{
		{nil, true},
		{[]string{oci}, true},
		{[]string{layout.MediaTypeDockerManifest, oci + ";q=0.9"}, true},
		{[]string{layout.MediaTypeDockerManifest + ", " + layout.MediaTypeOCIIndex}, false},
		{[]string{layout.MediaTypeDockerManifest, "*/*"}, true},
		{[]string{"application/json"}, true},
	} {
		if got := accepts(c.accept, oci); got != c.want {
			t.Errorf("%v: %v", c.accept, got)
		}
	}
}
