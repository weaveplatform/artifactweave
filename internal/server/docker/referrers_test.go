package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	ociIndex   = map[string]string{"Content-Type": layout.MediaTypeOCIIndex}
	dockerList = map[string]string{"Content-Type": layout.MediaTypeDockerList}
)

// descriptor is the descriptor of a manifest body of mediaType.
func descriptor(mediaType, body string) string {
	return `{"mediaType":"` + mediaType + `","digest":"` + digestOf(body) + `","size":` +
		strconv.Itoa(len(body)) + `}`
}

// index is an index of mediaType listing the image manifests images.
func index(mediaType string, images ...string) string {
	var ds []string
	for _, m := range images {
		ds = append(ds, descriptor(layout.MediaTypeOCIManifest, m))
	}
	return `{"schemaVersion":2,"mediaType":"` + mediaType + `","manifests":[` +
		strings.Join(ds, ",") + `]}`
}

// referrer is an artifact manifest of artifactType referring to subject,
// whose blobs are imageConfig and layer.
func referrer(artifactType, subject, note string) string {
	return strings.TrimSuffix(image("layer"), "}") + `,"artifactType":"` + artifactType +
		`","subject":` + descriptor(layout.MediaTypeOCIManifest, subject) +
		`,"annotations":{"note":"` + note + `"}}`
}

// pushPlatforms uploads the blobs of two platform images of app and pushes
// them by digest, as clients do before the index; it returns their bodies.
func (w *blobWorld) pushPlatforms() (string, string) {
	w.t.Helper()
	w.uploadBlobs("app", imageConfig, "layer", "arm layer")
	amd, arm := image("layer"), image("arm layer")
	for _, m := range []string{amd, arm} {
		expect(w.t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/"+digestOf(m), m,
			ociManifest), http.StatusCreated, "")
	}
	return amd, arm
}

// A multi-architecture image is pushed as its platforms by digest, then an
// index or manifest list by tag, and pulled as the list and as each
// platform. The list is never converted to a platform's manifest.
func TestPushAndPullIndex(t *testing.T) {
	w := newBlobWorld(t)
	amd, arm := w.pushPlatforms()
	for _, c := range []struct {
		mediaType, tag string
		headers        map[string]string
	}{
		{layout.MediaTypeOCIIndex, "v1", ociIndex},
		{layout.MediaTypeDockerList, "v2", dockerList},
	} {
		body := index(c.mediaType, amd, arm)
		rec := w.do(http.MethodPut, "/v2/docker-local/app/manifests/"+c.tag, body, c.headers)
		expect(t, rec, http.StatusCreated, "")
		if rec.Header().Get(subjectHeader) != "" {
			t.Fatalf("%s: %v", c.tag, rec.Header())
		}
		for _, ref := range []string{c.tag, digestOf(body)} {
			get := w.do(
				http.MethodGet,
				"/v2/docker-local/app/manifests/"+ref,
				"",
				map[string]string{
					"Accept": layout.MediaTypeOCIManifest + ", " + c.mediaType,
				},
			)
			expect(t, get, http.StatusOK, "")
			if get.Body.String() != body || get.Header().Get("Content-Type") != c.mediaType ||
				get.Header().Get(contentDigestHeader) != digestOf(body) {
				t.Fatalf("%s: %v %q", ref, get.Header(), get.Body)
			}
		}
		// A client that reads image manifests alone cannot read the list.
		expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/"+c.tag, "",
			map[string]string{"Accept": layout.MediaTypeOCIManifest}),
			http.StatusNotFound, CodeManifestUnknown)
	}
	get := w.do(http.MethodGet, "/v2/docker-local/app/manifests/"+digestOf(arm), "", nil)
	expect(t, get, http.StatusOK, "")
	if get.Body.String() != arm {
		t.Fatal(get.Body)
	}
	if _, err := w.db.Items().Get(context.Background(), "docker-local", false,
		"app/v1/list.manifest.json"); err != nil {
		t.Fatal(err)
	}
}

// Each manifest an index lists and the image lacks is named, and nothing is
// stored.
func TestIndexUnknownManifests(t *testing.T) {
	w := newBlobWorld(t)
	amd, _ := w.pushPlatforms()
	body := index(layout.MediaTypeOCIIndex, amd, image("absent"))
	rec := w.do(http.MethodPut, "/v2/docker-local/app/manifests/v1", body, ociIndex)
	expect(t, rec, http.StatusBadRequest, CodeManifestBlobUnknown)
	if !strings.Contains(rec.Body.String(), digestOf(image("absent"))) {
		t.Fatal(rec.Body)
	}
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/manifests/v1", "", nil),
		http.StatusNotFound, CodeManifestUnknown)
}

// A manifest with a subject is acknowledged with OCI-Subject, and listed by
// the referrers API of its subject: as an image index, filtered by artifact
// type on request, and empty rather than 404 when nothing refers to it.
func TestReferrers(t *testing.T) {
	w := newBlobWorld(t)
	amd, arm := w.pushPlatforms()
	sig := referrer("application/vnd.example.sig", amd, "signed")
	sbom := referrer("application/vnd.example.sbom", amd, "listed")
	for _, p := range []struct{ target, body string }{
		{"/v2/docker-local/app/manifests/" + digestOf(sig) + "?tag=sig", sig},
		{"/v2/docker-local/app/manifests/sbom", sbom},
	} {
		rec := w.do(http.MethodPut, p.target, p.body, ociManifest)
		expect(t, rec, http.StatusCreated, "")
		if rec.Header().Get(subjectHeader) != digestOf(amd) {
			t.Fatalf("%s: %v", p.target, rec.Header())
		}
	}
	type answer struct {
		SchemaVersion int                 `json:"schemaVersion"` //nolint:tagliatelle // OCI's names
		MediaType     string              `json:"mediaType"`     //nolint:tagliatelle // OCI's names
		Manifests     []layout.Descriptor `json:"manifests"`
	}
	referrers := func(target string) (answer, http.Header) {
		t.Helper()
		rec := w.do(http.MethodGet, target, "", nil)
		expect(t, rec, http.StatusOK, "")
		var a answer
		if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get("Content-Type") != layout.MediaTypeOCIIndex ||
			a.SchemaVersion != 2 || a.MediaType != layout.MediaTypeOCIIndex {
			t.Fatalf("%s: %v %s", target, rec.Header(), rec.Body)
		}
		return a, rec.Header()
	}
	of := func(body, artifactType, note string) layout.Descriptor {
		return layout.Descriptor{
			MediaType: layout.MediaTypeOCIManifest, Digest: digestOf(body),
			Size: int64(len(body)), ArtifactType: artifactType,
			Annotations: map[string]string{"note": note},
		}
	}
	sbomRef := of(sbom, "application/vnd.example.sbom", "listed")
	want := []layout.Descriptor{of(sig, "application/vnd.example.sig", "signed"), sbomRef}
	if want[0].Digest > want[1].Digest {
		want[0], want[1] = want[1], want[0]
	}
	for _, base := range []string{
		"/v2/docker-local/app/referrers/",
		"/artifactory/api/docker/docker-local/v2/app/referrers/",
	} {
		a, h := referrers(base + digestOf(amd))
		if diff := cmp.Diff(want, a.Manifests); diff != "" || h.Get(filtersHeader) != "" {
			t.Fatalf("%s: %s %v", base, diff, h)
		}
	}
	a, h := referrers("/v2/docker-local/app/referrers/" + digestOf(amd) +
		"?artifactType=application/vnd.example.sbom")
	if diff := cmp.Diff([]layout.Descriptor{sbomRef}, a.Manifests); diff != "" ||
		h.Get(filtersHeader) != "artifactType" {
		t.Fatalf("%s %v", diff, h)
	}
	for _, target := range []string{
		"/v2/docker-local/app/referrers/" + digestOf(arm),
		"/v2/docker-local/nowhere/referrers/" + digestOf(amd),
		"/v2/docker-local/app/referrers/sha512:" + strings.Repeat("a", 128),
	} {
		rec := w.do(http.MethodGet, target, "", nil)
		expect(t, rec, http.StatusOK, "")
		if !strings.Contains(rec.Body.String(), `"manifests":[]`) {
			t.Fatalf("%s: %s", target, rec.Body)
		}
	}
	expect(t, w.do(http.MethodGet, "/v2/docker-local/app/referrers/sha256:nope", "", nil),
		http.StatusBadRequest, CodeDigestInvalid)
	expect(t, w.do(http.MethodPost, "/v2/docker-local/app/referrers/"+digestOf(amd), "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
}

// The referrers API needs Read on the image; anonymous is challenged.
func TestReferrersPermissions(t *testing.T) {
	w := newBlobWorld(t)
	ctx := context.Background()
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "docker",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
			Targets: map[string]types.Target{"docker-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	target := "/v2/docker-local/app/referrers/" + digestOf("subject")
	for who, want := range map[string]int{
		"reader": http.StatusOK, "nobody": http.StatusForbidden,
		auth.AnonymousUser: http.StatusUnauthorized,
	} {
		if rec := send(
			principalAs(t, w.db, w.h, who),
			http.MethodGet,
			target,
			"",
			nil,
		); rec.Code != want {
			t.Errorf("%s: %d %s", who, rec.Code, rec.Body)
		}
	}
}

func TestReferrersStoreFailures(t *testing.T) {
	for name, tune := range map[string]func(*blobWorld, *Options){
		"search": func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		},
		"open": func(w *blobWorld, o *Options) {
			o.Blobs = failingBlobs{w.blobs}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			amd, _ := w.pushPlatforms()
			sig := referrer("application/vnd.example.sig", amd, "signed")
			expect(t, w.do(http.MethodPut, "/v2/docker-local/app/manifests/sig", sig, ociManifest),
				http.StatusCreated, "")
			var noted error
			o := Options{
				Store: w.db, Blobs: w.blobs,
				Fault: func(_ *http.Request, err error) { noted = err },
			}
			tune(w, &o)
			rec := send(router(New(o)), http.MethodGet,
				"/v2/docker-local/app/referrers/"+digestOf(amd), "", nil)
			if rec.Code != http.StatusInternalServerError || noted == nil {
				t.Fatalf("%d %s, noted %v", rec.Code, rec.Body, noted)
			}
		})
	}
}

// Without a blob store the referrers API is not served.
func TestReferrersUnimplementedWithoutBlobs(t *testing.T) {
	h := router(New(Options{Store: fixture(t)}))
	expect(t, send(h, http.MethodGet, "/v2/docker-local/app/referrers/"+digestOf("x"), "", nil),
		http.StatusMethodNotAllowed, CodeUnsupported)
}

// A registry token that does not grant pull on the image is challenged on
// the referrers API for the pull scope, and one that does is answered; an
// index push with pull alone is challenged for pull,push (decision 0005).
func TestReferrersAndIndexesChallengeRegistryTokens(t *testing.T) {
	w := newTokenWorld(t)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := New(Options{Store: w.db, Tokens: w.tokens, Blobs: blobs})
	h := baseurl.Middleware("")(g.Challenge(router(g)))
	pull := func(name string) *types.RegistryAccess {
		return &types.RegistryAccess{Grants: []types.RegistryGrant{
			{Type: scopeRepository, Name: name, Actions: []string{actionPull}},
		}}
	}
	as := func(method, target string, grant *types.RegistryAccess) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(
			method,
			target,
			strings.NewReader(index(layout.MediaTypeOCIIndex)),
		)
		req.Header.Set("Content-Type", layout.MediaTypeOCIIndex)
		req = req.WithContext(auth.WithPrincipal(req.Context(),
			w.as(auth.Principal{Name: "writer", Registry: grant})))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	referrers := "/v2/docker-local/app/referrers/" + digestOf("subject")
	for _, c := range []struct {
		method, target, granted string
		status                  int
		scope                   string
	}{
		{
			http.MethodGet, referrers, "docker-local/other", http.StatusUnauthorized,
			`scope="repository:docker-local/app:pull"`,
		},
		{http.MethodGet, referrers, "docker-local/app", http.StatusOK, ""},
		{
			http.MethodPut, "/v2/docker-local/app/manifests/v1", "docker-local/app",
			http.StatusUnauthorized, `scope="repository:docker-local/app:pull,push"`,
		},
	} {
		rec := as(c.method, c.target, pull(c.granted))
		challenge := rec.Header().Get("WWW-Authenticate")
		if rec.Code != c.status ||
			(c.scope != "" && (!strings.HasPrefix(challenge, "Bearer realm=") ||
				!strings.Contains(challenge, c.scope))) {
			t.Errorf("%s %s with pull on %s: %d %q %s", c.method, c.target, c.granted, rec.Code,
				challenge, rec.Body)
		}
		if c.status == http.StatusUnauthorized && code(t, rec) != CodeUnauthorized {
			t.Errorf("%s %s: %s", c.method, c.target, rec.Body)
		}
	}
}
