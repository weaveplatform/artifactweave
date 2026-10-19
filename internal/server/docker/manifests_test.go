package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
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
			h.Get(contentDigestHeader) != d || h.Get("ETag") != `"`+d+`"` ||
			h.Get(RegistryHeader) != "docker-local" {
			t.Fatalf("%s: %v %q", target, h, get.Body)
		}
		head := w.do(http.MethodHead, target, "", nil)
		expect(t, head, http.StatusOK, "")
		if head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(body)) ||
			head.Header().Get(contentDigestHeader) != d ||
			head.Header().Get(RegistryHeader) != "docker-local" {
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
	// The digest folder keeps the digest's colon, as a 7.x local repository
	// does, and is not a tag.
	if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
		"app/"+d+"/manifest.json"); err != nil {
		t.Fatal(err)
	}
	if l := list(t, w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil)); !cmp.Equal(
		l.Tags, []string{"a", "b"},
	) {
		t.Fatalf("tags %v", l.Tags)
	}
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/sha256__"+d[7:], "", nil),
		http.StatusNotFound, CodeManifestUnknown)
}

// sha256__<hex> is an ordinary tag, as in a 7.x local repository: its
// folder holds the pushed manifest beside the digest folder and the tags
// list names it.
func TestSHA256TagIsATag(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("app", imageConfig, "layer")
	body := image("layer")
	d := digestOf(body)
	tag := "sha256__" + d[7:]
	expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/"+tag, body, ociManifest),
		http.StatusCreated, "")
	expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/"+d+"?tag=sha256__x", body,
		ociManifest), http.StatusCreated, "")
	for _, ref := range []string{tag, "sha256__x", d} {
		expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/"+ref, "", nil),
			http.StatusOK, "")
	}
	for _, folder := range []string{tag, "sha256__x", d} {
		if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
			"app/"+folder+"/manifest.json"); err != nil {
			t.Errorf("%s: %v", folder, err)
		}
	}
	if l := list(t, w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil)); !cmp.Equal(
		l.Tags, []string{tag, "sha256__x"},
	) {
		t.Fatalf("tags %v", l.Tags)
	}
	expect(t, w.do(http.MethodDelete, "/v2/docker-local/app/manifests/"+tag, "", nil),
		http.StatusAccepted, "")
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/"+d, "", nil),
		http.StatusOK, "")
}

func TestManifestPushErrors(t *testing.T) {
	body := image("layer")
	d := digestOf(body)
	schema1 := map[string]string{
		"Content-Type": "application/vnd.docker.distribution.manifest.v1+prettyjws",
	}
	unknownChild := index(layout.MediaTypeOCIIndex, image("absent"))
	huge := strings.Repeat(" ", layout.MaxManifestSize+1)
	const base = "/v2/docker-local/app/manifests/"
	for name, c := range map[string]struct {
		ref, body string
		headers   map[string]string
		status    int
		code      string
	}{
		"unknown blob":         {"v1", image("absent"), ociManifest, 400, CodeManifestBlobUnknown},
		"wrong media type":     {"v1", body, map[string]string{"Content-Type": "text/plain"}, 400, CodeManifestInvalid},
		"schema 1":             {"v1", body, schema1, 400, CodeManifestInvalid},
		"not JSON":             {"v1", "nope", ociManifest, 400, CodeManifestInvalid},
		"index of the unknown": {"v1", unknownChild, ociIndex, 400, CodeManifestBlobUnknown},
		"too large":            {"v1", huge, ociManifest, 413, CodeSizeInvalid},
		"invalid tag":          {"-v1", body, ociManifest, 400, CodeManifestInvalid},
		"uploads tag":          {"_uploads", body, ociManifest, 400, CodeTagInvalid},
		"invalid digest":       {"sha256:totallywrong", body, ociManifest, 400, CodeManifestInvalid},
		"other algorithm":      {"sha512:" + strings.Repeat("a", 128), body, ociManifest, 400, CodeDigestInvalid},
		"digest mismatch":      {digestOf("other"), body, ociManifest, 400, CodeDigestInvalid},
		"invalid extra tag":    {d + "?tag=-x", body, ociManifest, 400, CodeManifestInvalid},
		"reserved extra tag":   {d + "?tag=_uploads", body, ociManifest, 400, CodeTagInvalid},
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

// A descriptor whose size is not the stored content's is MANIFEST_INVALID
// in 7.x's shape, "manifest invalid" with detail.description, which names
// the digest, the size the manifest gives and the stored size.
func TestDescriptorSizeMismatch(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("app", imageConfig, "layer")
	body := strings.Replace(
		image("layer"),
		digestOf("layer")+`","size":5`,
		digestOf("layer")+`","size":6`,
		1,
	)
	rec := w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", body, ociManifest)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Errors []struct {
			Code    string
			Message string
			Detail  map[string]any
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		Code    string
		Message string
		Detail  map[string]any
	}{{CodeManifestInvalid, "manifest invalid", map[string]any{
		"description": "descriptor " + digestOf("layer") +
			" declares 6 bytes, but the stored content has 5",
	}}}
	if diff := cmp.Diff(want, got.Errors); diff != "" {
		t.Fatal(diff)
	}
}

// MANIFEST_UNKNOWN carries 7.x's message and the image as the client named
// it, without the reference, as detail.manifest: a missing tag or digest, a
// delete of one, and a manifest the Accept header leaves out alike.
func TestManifestUnknownShape(t *testing.T) {
	w := newBlobWorld(t)
	w.uploadBlobs("team/app", imageConfig, "layer")
	expect(t, w.do(http.MethodPut, "/v2/docker-local/team/app/manifests/v1", image("layer"),
		ociManifest), http.StatusCreated, "")
	for _, c := range []struct {
		method, target, image string
		headers               map[string]string
	}{
		{http.MethodGet, "/v2/docker-local/team/app/manifests/v9", "docker-local/team/app", nil},
		{http.MethodGet, "/artifactory/api/docker/docker-local/v2/team/app/manifests/" +
			digestOf("absent"), "team/app", nil},
		{http.MethodDelete, "/v2/docker-local/team/app/manifests/v9", "docker-local/team/app", nil},
		{
			http.MethodGet, "/v2/docker-local/team/app/manifests/v1", "docker-local/team/app",
			map[string]string{"Accept": layout.MediaTypeDockerManifest},
		},
		{
			http.MethodGet, "/v2/team/app/manifests/v9", "team/app",
			map[string]string{"Host": "docker-local.reg.example.com"},
		},
	} {
		rec := w.do(c.method, c.target, "", c.headers)
		want := `{"errors":[{"code":"MANIFEST_UNKNOWN",` +
			`"message":"The named manifest is not known to the registry.",` +
			`"detail":{"manifest":"` + c.image + `"}}]}`
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d", c.method, c.target, rec.Code)
		}
		var wantDoc, gotDoc any
		if err := json.Unmarshal([]byte(want), &wantDoc); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &gotDoc); err != nil {
			t.Fatalf("%s %s: %s", c.method, c.target, rec.Body)
		}
		if diff := cmp.Diff(wantDoc, gotDoc); diff != "" {
			t.Errorf("%s %s: %s", c.method, c.target, diff)
		}
	}
}

// A list push writes a descriptive tag for each platform, so replacing one
// that holds another manifest needs Delete, as replacing any tag does
// (DV-035); writing one that is absent needs Deploy alone.
func TestListPushStrictOverwriteCoversPlatformTags(t *testing.T) {
	w := newBlobWorld(t)
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "deployers",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	w.uploadBlobs("app", imageConfig, "a", "b")
	amd := image("a")
	expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/"+digestOf(amd), amd,
		ociManifest), http.StatusCreated, "")
	listBody := `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIIndex + `","manifests":[` +
		`{"mediaType":"` + layout.MediaTypeOCIManifest + `","digest":"` + digestOf(amd) +
		`","size":` + strconv.Itoa(len(amd)) +
		`,"platform":{"os":"linux","architecture":"amd64"}}]}`
	index := map[string]string{"Content-Type": layout.MediaTypeOCIIndex}
	deployer := principalAs(t, w.db, w.h, "deployer")
	expect(t, send(deployer, http.MethodPut, "/v2/docker-local/app/manifests/v1", listBody, index),
		http.StatusCreated, "")
	// v2-linux-amd64 already holds another image.
	expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/v2-linux-amd64", image("b"),
		ociManifest), http.StatusCreated, "")
	expect(t, send(deployer, http.MethodPut, "/v2/docker-local/app/manifests/v2", listBody, index),
		http.StatusForbidden, CodeDenied)
	expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/v2", listBody, index),
		http.StatusCreated, "")
	rec := w.do(http.MethodGet, "/v2/docker-local/app/tags/list", "", nil)
	if got := list(t, rec).Tags; !slices.Equal(got, []string{
		"v1", "v1-linux-amd64", "v2",
		"v2-linux-amd64",
	}) {
		t.Fatalf("tags %v", got)
	}
}

// Every MANIFEST_INVALID carries 7.x's message, "manifest invalid", and says
// what failed in detail.description (RTFACT-29656).
func TestManifestInvalidShape(t *testing.T) {
	w := newBlobWorld(t)
	for name, c := range map[string]struct{ target, body, description string }{
		"a body that is no manifest": {
			"/v2/docker-local/app/manifests/v1", "{", "the body is not a JSON manifest",
		},
		"an invalid digest reference": {
			"/v2/docker-local/app/manifests/sha256:short", image("x"),
			`invalid manifest reference "sha256:short"`,
		},
		"an invalid tag in ?tag=": {
			"/v2/docker-local/app/manifests/" + digestOf(image("x")) + "?tag=-bad", image("x"),
			`invalid tag "-bad"`,
		},
	} {
		rec := w.do(http.MethodPut, c.target, c.body, ociManifest)
		var got struct {
			Errors []struct {
				Code, Message string
				Detail        map[string]any
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 400 ||
			len(got.Errors) != 1 || got.Errors[0].Code != CodeManifestInvalid ||
			got.Errors[0].Message != "manifest invalid" ||
			got.Errors[0].Detail["description"] != c.description {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
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
		"app/"+digestOf(second)); !errors.Is(err, store.ErrNotFound) {
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

// A registry token pushes a manifest only with push on the image; a
// pull-only one is told, by the challenge, to fetch push. Replacing a tag
// is part of the push the client asked for, so push covers it, while the
// user must still hold Delete (DV-035).
func TestManifestPushWithRegistryTokens(t *testing.T) {
	w := newBlobWorld(t)
	ctx := context.Background()
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "docker",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"owner":  types.ActionRead | types.ActionWrite | types.ActionDelete,
				"writer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	w.uploadBlobs("app", imageConfig, "one", "two")
	tokenAs := func(user string, actions ...string) http.Handler {
		p, err := auth.Authorize(ctx, authz.Sources{
			Permissions: w.db.Permissions(), Groups: w.db.Groups(),
		}, auth.Principal{Name: user, Registry: &types.RegistryAccess{Grants: []types.RegistryGrant{
			{Type: scopeRepository, Name: "docker-local/app", Actions: actions},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			w.h.ServeHTTP(rw, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
	const v1 = "/v2/docker-local/app/manifests/v1"
	rec := send(tokenAs("owner", "pull"), http.MethodPut, v1, image("one"), ociManifest)
	if rec.Code != http.StatusUnauthorized || code(t, rec) != CodeUnauthorized ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"),
			`scope="repository:docker-local/app:pull,push"`) {
		t.Fatalf("pull-only push: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	push := tokenAs("owner", "pull", "push")
	expect(t, send(push, http.MethodPut, v1, image("one"), ociManifest), http.StatusCreated, "")
	expect(
		t,
		send(tokenAs("owner", "pull"), http.MethodGet, v1, "", ociManifest),
		http.StatusOK,
		"",
	)
	// Replacing v1 with push alone: the owner holds Delete.
	expect(t, send(push, http.MethodPut, v1, image("two"), ociManifest), http.StatusCreated, "")
	// The writer's push token cannot replace it: the permissions refuse.
	rec = send(tokenAs("writer", "pull", "push"), http.MethodPut, v1, image("one"), ociManifest)
	if rec.Code != http.StatusForbidden || code(t, rec) != CodeDenied {
		t.Fatalf("writer replacing: %d %s", rec.Code, rec.Body)
	}
}
