package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The registry's own routing is tested in internal/server/docker; these
// rows and tests cover what the server adds: authentication in front of
// it, the version header on the middleware's answers, the mounting under
// both prefixes and the reverse proxy read (#220).

const (
	registryProbe = "GET /v2/"
	prefixProbe   = "GET /artifactory/api/docker/{repoKey}/v2/"
	webServer     = "GET /artifactory/api/system/configuration/webServer"
)

// The blob routes (#222), on the repository path; the router tests in
// internal/server/docker cover the other addressing forms.
const (
	blobRoute    = "/v2/{repo}/{name}/blobs/{digest}"
	uploadsRoute = "/v2/{repo}/{name}/blobs/uploads/"
	uploadRoute  = "/v2/{repo}/{name}/blobs/uploads/{session}"
	headBlob     = "HEAD " + blobRoute
	getBlob      = "GET " + blobRoute
	deleteBlob   = "DELETE " + blobRoute
	startUpload  = "POST " + uploadsRoute
	appendUpload = "PATCH " + uploadRoute
	finishUpload = "PUT " + uploadRoute
	uploadStatus = "GET " + uploadRoute
	cancelUpload = "DELETE " + uploadRoute
)

// sha256Digest is the registry digest of s.
func sha256Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// registryLayer is the blob the fixtures store, and chunk what the upload
// rows send.
var (
	registryLayer = sha256Digest("registry layer")
	chunkDigest   = sha256Digest("abc")
)

// seedRegistryStatus gives the status fixture the Docker repository
// docker-blobs, admin-only like the rest, with registryLayer in the image
// app, and admin's upload sessions, one per row that needs one, as
// {upload-<row>} secrets.
func seedRegistryStatus(t *testing.T, db store.Store, blobs blob.Store, secrets statusSecrets) {
	t.Helper()
	ctx := context.Background()
	c := *repo.Defaults(&repo.Config{Key: "docker-blobs", Class: repo.Local, PackageType: repo.Docker})
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{Store: db, Blobs: blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: "app/v1/sha256__" + registryLayer[7:]},
		Body:   strings.NewReader("registry layer"),
	}); err != nil {
		t.Fatal(err)
	}
	uploads := &registry.Service{Store: db, Blobs: blobs}
	for _, row := range []string{"patch", "put", "mismatch", "get", "delete"} {
		id, err := uploads.Begin(ctx, registry.Owner{Repo: c, Name: "app", By: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		secrets["upload-"+row] = id
	}
}

// registryBlobStatusRows: reads need Read on the image and uploads Deploy,
// which only admin holds on docker-blobs; anonymous is always challenged.
// A session is its opener's, so the rows use admin's.
func registryBlobStatusRows() []statusCase {
	const (
		blobs   = "/v2/docker-blobs/app/blobs/"
		uploads = "/v2/docker-blobs/app/blobs/uploads/"
		nobody  = uploads + "00000000000000000000000000000000"
	)
	layer, other := blobs+registryLayer, blobs+sha256Digest("absent")
	return []statusCase{
		{headBlob, layer, "admin", "", 200},
		{headBlob, other, "admin", "", 404},
		{headBlob, layer, "user", "", 403},
		{headBlob, layer, "anonymous", "", 401},
		{headBlob, layer, "", "", 401},
		{getBlob, layer, "admin", "", 200},
		{getBlob, other, "admin", "", 404},
		{getBlob, blobs + "sha256:zz", "admin", "", 400},
		{getBlob, blobs + "sha512:" + strings.Repeat("a", 128), "admin", "", 404},
		{getBlob, layer, "user", "", 403},
		{getBlob, layer, "anonymous", "", 401},
		{getBlob, layer, "expired", "", 401},
		{deleteBlob, layer, "admin", "", 405},
		{deleteBlob, layer, "anonymous", "", 401},
		{startUpload, uploads, "admin", "", 202},
		{startUpload, uploads + "?digest=" + chunkDigest, "admin", "abc", 201},
		{startUpload, uploads + "?digest=" + chunkDigest, "admin", "xyz", 400},
		{startUpload, uploads + "?digest-algorithm=sha512", "admin", "", 400},
		{
			startUpload,
			uploads + "?mount=" + registryLayer + "&from=docker-blobs/app",
			"admin",
			"",
			201,
		},
		{
			startUpload,
			uploads + "?mount=" + registryLayer + "&from=docker-blobs/none",
			"admin",
			"",
			202,
		},
		{startUpload, uploads, "user", "", 403},
		{startUpload, uploads, "anonymous", "", 401},
		{startUpload, uploads, "", "", 401},
		{appendUpload, uploads + "{upload-patch}", "admin", "abc", 202},
		{appendUpload, nobody, "admin", "abc", 404},
		{appendUpload, uploads + "{upload-patch}", "user", "abc", 403},
		{appendUpload, uploads + "{upload-patch}", "anonymous", "abc", 401},
		{finishUpload, uploads + "{upload-put}?digest=" + chunkDigest, "admin", "abc", 201},
		{finishUpload, uploads + "{upload-mismatch}?digest=" + chunkDigest, "admin", "xyz", 400},
		{finishUpload, uploads + "{upload-get}", "admin", "", 400},
		{finishUpload, nobody + "?digest=" + chunkDigest, "admin", "", 404},
		{finishUpload, uploads + "{upload-get}?digest=" + chunkDigest, "user", "", 403},
		{uploadStatus, uploads + "{upload-get}", "admin", "", 204},
		{uploadStatus, nobody, "admin", "", 404},
		{uploadStatus, uploads + "{upload-get}", "user", "", 403},
		{uploadStatus, uploads + "{upload-get}", "forged", "", 401},
		{cancelUpload, uploads + "{upload-delete}", "admin", "", 204},
		{cancelUpload, uploads + "{upload-delete}", "admin", "", 404},
		{cancelUpload, uploads + "{upload-get}", "user", "", 403},
		{cancelUpload, uploads + "{upload-get}", "anonymous", "", 401},
	}
}

// The matrix's registry repositories: docker-a, which the principals hold
// as they hold libs-local, and docker-b, which only administrators read,
// as a mount's unreadable source.
const (
	dockerA = "docker-a"
	dockerB = "docker-b"
)

func matrixRegistryRepos() []repo.Config {
	return []repo.Config{
		{Key: dockerA, Class: repo.Local, PackageType: repo.Docker},
		{Key: dockerB, Class: repo.Local, PackageType: repo.Docker},
	}
}

// seedMatrixRegistry stores registryLayer in docker-a's image work/app and
// docker-b's image base.
func seedMatrixRegistry(t *testing.T, f *matrixFixture) {
	t.Helper()
	f.deploy(t, dockerA, "work/app/v1/sha256__"+registryLayer[7:], []byte("registry layer"))
	f.deploy(t, dockerB, "base/v1/sha256__"+registryLayer[7:], []byte("registry layer"))
}

// uploadSession seeds, for the principal named p, an upload session to
// docker-a's image work/{p} opened by its user, as the row's {upload}.
func uploadSession(t *testing.T, f *matrixFixture, p string) {
	t.Helper()
	ctx := context.Background()
	i := slices.IndexFunc(matrixPrincipals, func(q matrixPrincipal) bool { return q.name == p })
	c, err := f.db.Repos().Get(ctx, dockerA)
	if err != nil {
		t.Fatal(err)
	}
	id, err := (&registry.Service{Store: f.db, Blobs: f.items.Blobs}).Begin(ctx, registry.Owner{
		Repo: c, Name: "work/" + p, By: matrixPrincipals[i].user,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.uploads[p] = id
}

// mountRule: a mount needs Deploy on the target, and links the blob (the
// row's ok) only for a caller that may read the source; any other caller
// gets a session, 202.
func mountRule(srcRepo, srcPath string) rule {
	return func(p matrixPrincipal) int {
		if !p.holds(dockerA, "work/", types.ActionWrite) {
			return refuse(p)
		}
		if !p.holds(srcRepo, srcPath, types.ActionRead) {
			return http.StatusAccepted
		}
		return 0
	}
}

// registryBlobMatrix: blob reads need Read on the image, uploads Deploy, and
// a mount Read on its source too (decision 0031). Anonymous is challenged
// whatever it holds. A blob delete is refused, 405, to anyone signed in.
func registryBlobMatrix() []matrixRow {
	const (
		layer   = "/v2/" + dockerA + "/work/app/blobs/"
		uploads = "/v2/" + dockerA + "/work/{p}/blobs/uploads/"
		session = uploads + "{upload}"
	)
	read, write := types.ActionRead, types.ActionWrite
	mount := uploads + "?mount=" + registryLayer + "&from="
	return []matrixRow{
		row(headBlob, layer+registryLayer, "", holds(dockerA, "work/app/", read), 200),
		row(getBlob, layer+registryLayer, "", holds(dockerA, "work/app/", read), 200),
		row(deleteBlob, layer+registryLayer, "", signedIn, 405),
		row(startUpload, uploads, "", holds(dockerA, "work/", write), 202),
		// From docker-a, which every caller that may deploy there also reads.
		row(startUpload, mount+dockerA+"/work/app", "", mountRule(dockerA, "work/app/"), 201),
		// From docker-b, which only administrators read.
		row(startUpload, mount+dockerB+"/base", "", mountRule(dockerB, "base/"), 201),
		row(appendUpload, session, "abc", holds(dockerA, "work/", write), 202).
			seeding(uploadSession),
		row(
			finishUpload,
			session+"?digest="+chunkDigest,
			"abc",
			holds(dockerA, "work/", write),
			201,
		).
			seeding(uploadSession),
		row(uploadStatus, session, "", holds(dockerA, "work/", write), 204).seeding(uploadSession),
		row(cancelUpload, session, "", holds(dockerA, "work/", write), 204).seeding(uploadSession),
	}
}

func registryStatusRows() []statusCase {
	const prefixed = "/artifactory/api/docker/libs-local/v2/"
	return []statusCase{
		{registryProbe, "/v2/", "admin", "", 200},
		{registryProbe, "/v2/", "user", "", 200},
		// Challenged even while anonymous access is on (decision 0031).
		{registryProbe, "/v2/", "anonymous", "", 401},
		{registryProbe, "/v2/", "", "", 401},
		{registryProbe, "/v2/", "expired", "", 401},
		{registryProbe, "/v2/", "forged", "", 401},
		{prefixProbe, prefixed, "admin", "", 200},
		{prefixProbe, prefixed, "anonymous", "", 401},
		{prefixProbe, prefixed, "", "", 401},
		// libs-local is a generic repository, which the registry does not
		// serve.
		{registryProbe, "/v2/libs-local/app/manifests/latest", "admin", "", 404},
		{registryProbe, "/v2/libs-local/app/manifests/latest", "anonymous", "", 401},
		// The token endpoint alone lets an anonymous caller through, for
		// its anonymous token; until #221 it answers that it is not
		// implemented.
		{"GET /v2/token", "/v2/token", "anonymous", "", 405},
		{"GET /v2/token", "/v2/token", "", "", 401},
		{webServer, "/artifactory/api/system/configuration/webServer", "admin", "", 200},
		{webServer, "/artifactory/api/system/configuration/webServer", "user", "", 403},
		{webServer, "/artifactory/api/system/configuration/webServer", "anonymous", "", 401},
		{webServer, "/artifactory/api/system/configuration/webServer", "", "", 401},
	}
}

// registryMatrix: the probe answers any caller with credentials. An
// anonymous one is challenged even while anonymous access is on, and
// fetches an anonymous token instead (decision 0031).
func registryMatrix() []matrixRow {
	return []matrixRow{
		at(registryProbe, "", signedIn, 200),
		row(prefixProbe, "/artifactory/api/docker/libs-local/v2/", "", signedIn, 200),
		at(webServer, "", administrators, 200),
	}
}

// A registry client learns it reached a v2 registry from the probe's
// header, on the 401 the authentication middleware writes as well as on
// the 200, and reads the challenge and the OCI envelope from the 401.
func TestRegistryProbeHeaders(t *testing.T) {
	h := statusFixture(t, false)
	for _, path := range []string{"/v2/", "/artifactory/api/docker/any/v2/"} {
		rec := do(t, h, http.MethodGet, path, nil, "")
		if rec.Code != http.StatusUnauthorized ||
			rec.Header().Get(docker.APIVersionHeader) != "registry/2.0" ||
			rec.Header().Get("WWW-Authenticate") != `Basic realm="Artifactory Realm"` {
			t.Fatalf("%s: %d %v", path, rec.Code, rec.Header())
		}
		var body struct{ Errors []struct{ Code string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil ||
			len(body.Errors) != 1 || body.Errors[0].Code != docker.CodeUnauthorized {
			t.Fatalf("%s: %s", path, rec.Body)
		}
		rec = do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		if rec.Code != http.StatusOK ||
			rec.Header().Get(docker.APIVersionHeader) != "registry/2.0" ||
			rec.Body.String() != "{}" {
			t.Fatalf("%s as admin: %d %v %s", path, rec.Code, rec.Header(), rec.Body)
		}
	}
	rec := do(t, h, http.MethodGet, "/artifactory/api/system/ping", nil, "")
	if rec.Header().Get(docker.APIVersionHeader) != "" {
		t.Fatal("a non-registry path carries the registry header")
	}
}

// A manifest's media type is what the registry stores, so registry
// requests keep the client's Content-Type; other +json types still become
// application/json for the strict decoders.
func TestRegistryKeepsContentType(t *testing.T) {
	const manifest = "application/vnd.oci.image.manifest.v1+json"
	for path, want := range map[string]string{
		"/v2/r/app/manifests/v1":                        manifest,
		"/artifactory/api/docker/r/v2/app/manifests/v1": manifest,
		"/artifactory/api/repositories/r":               "application/json",
	} {
		var got string
		h := normalizeContentType(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Content-Type")
		}))
		do(t, h, http.MethodPut, path, map[string]string{"Content-Type": manifest}, "{}")
		if got != want {
			t.Errorf("%s: Content-Type %q, want %q", path, got, want)
		}
	}
}

func TestReverseProxyConfiguration(t *testing.T) {
	cases := []struct {
		name string
		tune func(*Config)
		want map[string]any
	}{
		{
			"repository path, base URL from the request", nil,
			map[string]any{
				"key": "nginx", "webServerType": "NGINX", "artifactoryAppContext": "artifactory",
				"publicAppContext": "artifactory", "serverName": "example.com",
				"dockerReverseProxyMethod": "REPOPATHPREFIX",
			},
		},
		{
			"subdomains under the configured base URL",
			func(c *Config) { c.BaseURL, c.DockerSubdomainMatch = "https://Reg.Example.com:8443/artifactory", true },
			map[string]any{
				"key":                      "nginx",
				"webServerType":            "NGINX",
				"artifactoryAppContext":    "artifactory",
				"publicAppContext":         "artifactory",
				"serverName":               "reg.example.com",
				"serverNameExpression":     "*.reg.example.com",
				"dockerReverseProxyMethod": "SUBDOMAIN",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tune []func(*Config)
			if c.tune != nil {
				tune = append(tune, c.tune)
			}
			h := statusFixture(t, false, tune...)
			rec := do(t, h, http.MethodGet, "/artifactory/api/system/configuration/webServer",
				basic("admin", "pw"), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

// The subdomain method reaches the registry through the server, with the
// repository taken from the host the proxy forwarded.
func TestRegistrySubdomainThroughServer(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) {
		c.BaseURL, c.DockerSubdomainMatch = "https://reg.example.com", true
	})
	admin := basic("admin", "pw")
	if rec := do(t, h, http.MethodPut, "/artifactory/api/repositories/docker-local", jsonAs(admin),
		`{"rclass":"local","packageType":"docker"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	// On the repository's subdomain app is the image, which has no such
	// tag; on the parent it would be a repository, which does not exist.
	for host, want := range map[string]string{
		"docker-local.reg.example.com": docker.CodeManifestUnknown,
		"reg.example.com":              docker.CodeNameUnknown,
	} {
		headers := basic("admin", "pw")
		headers["X-Forwarded-Host"] = host
		rec := do(t, h, http.MethodGet, "/v2/app/manifests/latest", headers, "")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", host, rec.Code, rec.Body)
		}
	}
}

// The JFrog REST operations under /artifactory/api/docker/<repo>/v2/ that
// the generated server owns (parentManifests, promote, and _catalog and
// tags/list until the registry serves them) must still reach
// it: chi prefers their literal and parameter segments to the registry's
// catch-all. Every such operation in the spec is checked, so one added
// later is covered without editing this test.
func TestDockerOperationsReachTheGeneratedServer(t *testing.T) {
	mux, ok := testHandler().(chi.Routes)
	if !ok {
		t.Fatal("the handler is not a chi router")
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	seen := map[string]bool{}
	for path, item := range loadSpec(t, "artifactory.openapi.yaml").Paths.Map() {
		if !strings.HasPrefix(path, "/api/docker/") {
			continue
		}
		for method, op := range item.Operations() {
			if v, ok := op.Extensions["x-handwritten"]; ok && v == true {
				continue
			}
			concrete := "/artifactory" + param.ReplaceAllString(path, "x")
			if got := mux.Find(
				chi.NewRouteContext(),
				method,
				concrete,
			); got != "/artifactory"+path {
				t.Errorf("%s %s reaches %q, not the generated %s", method, concrete, got, path)
			}
			seen[op.OperationID] = true
		}
	}
	for _, id := range []string{"findParentManifestLists", "promoteDockerImage"} {
		if !seen[id] {
			t.Errorf("%s was not checked: %v", id, seen)
		}
	}
	// Over HTTP: the generated stub answers, in the Artifactory envelope,
	// without the registry's header or its anonymous challenge.
	h := statusFixture(t, true)
	want := `{"errors":[{"status":501,"message":"not implemented by artifactweave yet"}]}`
	for op, body := range map[string]string{
		"parentManifests": `{"dockerRepository":"app","tag":"v1"}`,
		"promote":         `{"targetRepo":"libs-b","dockerRepository":"app"}`,
	} {
		rec := do(t, h, http.MethodPost, "/artifactory/api/docker/libs-local/v2/"+op,
			map[string]string{"Content-Type": "application/json"}, body)
		if rec.Code != http.StatusNotImplemented ||
			rec.Header().Get(docker.APIVersionHeader) != "" {
			t.Fatalf("%s: %d %v %s", op, rec.Code, rec.Header(), rec.Body)
		}
		if diff := cmp.Diff(decode(t, want), decode(t, rec.Body.String())); diff != "" {
			t.Fatalf("%s: %s", op, diff)
		}
	}
}

// A layer pushed through the registry downloads as a file does, through
// the server's download: a repository with downloadRedirect answers a GET
// with a 302 to a signed URL while HEAD stays local, and a streamed
// download is counted (#222, decision 0029).
func TestRegistryBlobsDownloadAsFiles(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker},
		{
			Key: "docker-redirect", Class: repo.Local, PackageType: repo.Docker,
			DownloadRedirect: repo.Flex(true),
		},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("layer"))
	hexSum := hex.EncodeToString(sum[:])
	digest := "sha256:" + hexSum
	for _, key := range []string{"docker-local", "docker-redirect"} {
		rec := w.do(http.MethodPost, "/v2/"+key+"/app/blobs/uploads/?digest="+digest, "admin",
			"layer")
		want := "https://reg.example/v2/" + key + "/app/blobs/" + digest
		if rec.Code != http.StatusCreated || rec.Header().Get("Location") != want {
			t.Fatalf("%s: %d %v %s", key, rec.Code, rec.Header(), rec.Body)
		}
	}
	rec := w.do(http.MethodGet, "/v2/docker-local/app/blobs/"+digest, "admin", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "layer" ||
		rec.Header().Get("X-Checksum-Sha256") != hexSum ||
		rec.Header().Get("Docker-Content-Digest") != digest {
		t.Fatalf("streamed: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	it := w.item("docker-local", false, "app/_uploads/sha256__"+hexSum)
	if stats, err := w.db.Items().Stats(ctx, it.ID); err != nil || stats.DownloadCount != 1 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	rec = w.do(http.MethodGet, "/v2/docker-redirect/app/blobs/"+digest, "admin", "")
	if rec.Code != http.StatusFound ||
		!strings.HasPrefix(rec.Header().Get("Location"), "https://storage.example/"+hexSum) ||
		rec.Header().Get("Docker-Content-Digest") != digest {
		t.Fatalf("redirected: %d %v", rec.Code, rec.Header())
	}
	rec = w.do(http.MethodHead, "/v2/docker-redirect/app/blobs/"+digest, "admin", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Length") != "5" {
		t.Fatalf("HEAD: %d %v", rec.Code, rec.Header())
	}
	// Uploads need Deploy on the image: deployer holds it on libs-local only.
	rec = w.do(http.MethodPost, "/v2/docker-local/app/blobs/uploads/", "deployer", "")
	if rec.Code != http.StatusForbidden ||
		rec.Header().Get(docker.APIVersionHeader) != "registry/2.0" {
		t.Fatalf("deployer: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

// The manifest routes (#223), on the repository path.
const (
	manifestRoute = "/v2/{repo}/{name}/manifests/{reference}"
	headManifest  = "HEAD " + manifestRoute
	getManifest   = "GET " + manifestRoute
	putManifest   = "PUT " + manifestRoute
)

// registryConfig is the config of the images the fixtures push.
const registryConfig = `{"architecture":"amd64","os":"linux","config":{"Labels":{"team":"matrix"}}}`

// registryManifest is an OCI image manifest of registryConfig and
// registryLayer; note varies its digest. A row sends it with the prefix
// manifest:, which sets its Content-Type.
func registryManifest(note string) string {
	return `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIManifest + `",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` +
		sha256Digest(registryConfig) + `","size":` + strconv.Itoa(len(registryConfig)) + `},` +
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"` +
		registryLayer + `","size":14}],"annotations":{"note":"` + note + `"}}`
}

// manifestBody is a row body that sends body as an OCI image manifest.
func manifestBody(body string) string { return "manifest:" + body }

// contentTypeOf takes a row body's prefix off and returns the Content-Type
// it names: a manifest's, a form's, or JSON's.
func contentTypeOf(body string) (contentType, rest string) {
	if b, ok := strings.CutPrefix(body, "manifest:"); ok {
		return layout.MediaTypeOCIManifest, b
	}
	if b, ok := strings.CutPrefix(body, "form:"); ok {
		return "application/x-www-form-urlencoded", b
	}
	return "application/json", body
}

// pushImage stores registryConfig in the image name of c and pushes body
// to tag, as a client would after uploading its blobs; registryLayer must
// already be in the image.
func pushImage(
	t *testing.T,
	db store.Store,
	blobs blob.Store,
	c repo.Config,
	name, tag, body string,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := (&item.Service{Store: db, Blobs: blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{
			Repo: c,
			Path: name + "/_uploads/sha256__" + sha256Digest(registryConfig)[7:],
		},
		Body: strings.NewReader(registryConfig),
	}); err != nil {
		t.Fatal(err)
	}
	m, err := layout.ParseManifest(
		layout.MediaTypeOCIManifest,
		[]byte(body),
		sha256Digest(body)[7:],
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&registry.Service{Store: db, Blobs: blobs}).PutManifest(ctx, registry.Push{
		Owner:    registry.Owner{Repo: c, Name: name, By: "admin"},
		Manifest: m, Body: []byte(body), Folders: []string{tag},
	}); err != nil {
		t.Fatal(err)
	}
}

// seedRegistryManifests pushes registryManifest("v1") to docker-blobs'
// image app as v1.
func seedRegistryManifests(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	c, err := db.Repos().Get(context.Background(), "docker-blobs")
	if err != nil {
		t.Fatal(err)
	}
	pushImage(t, db, blobs, c, "app", "v1", registryManifest("v1"))
}

// registryManifestStatusRows: pulls need Read on the image and pushes
// Deploy, which only admin holds on docker-blobs; anonymous is always
// challenged.
func registryManifestStatusRows() []statusCase {
	const base = "/v2/docker-blobs/app/manifests/"
	v1, digest := base+"v1", base+sha256Digest(registryManifest("v1"))
	push := manifestBody(registryManifest("v2"))
	return []statusCase{
		{headManifest, v1, "admin", "", 200},
		{headManifest, base + "v9", "admin", "", 404},
		{headManifest, v1, "user", "", 403},
		{headManifest, v1, "anonymous", "", 401},
		{getManifest, v1, "admin", "", 200},
		{getManifest, digest, "admin", "", 200},
		{getManifest, base + sha256Digest("absent"), "admin", "", 404},
		{getManifest, base + "sha256:totallywrong", "admin", "", 404},
		{getManifest, v1, "user", "", 403},
		{getManifest, v1, "anonymous", "", 401},
		{getManifest, v1, "", "", 401},
		{getManifest, v1, "expired", "", 401},
		{putManifest, base + "v2", "admin", push, 201},
		// The same push again changes nothing.
		{putManifest, base + "v2", "admin", push, 201},
		{putManifest, base + sha256Digest(registryManifest("v2")), "admin", push, 201},
		{putManifest, base + sha256Digest("other"), "admin", push, 400},
		{putManifest, base + "_uploads", "admin", push, 400},
		{putManifest, base + "v3", "admin", manifestBody("{}"), 400},
		{putManifest, base + "v3", "admin", `{"schemaVersion":2}`, 400},
		{
			putManifest,
			base + "v3",
			"admin",
			manifestBody(
				strings.Replace(registryManifest("v3"), registryLayer, sha256Digest("absent"), 1),
			),
			400,
		},
		{putManifest, base + "v3", "user", push, 403},
		{putManifest, base + "v3", "anonymous", push, 401},
		{putManifest, base + "v3", "", push, 401},
	}
}

// seedMatrixManifests pushes registryManifest("v1") to docker-a's image
// work/app as v1.
func seedMatrixManifests(t *testing.T, f *matrixFixture) {
	t.Helper()
	c, err := f.db.Repos().Get(context.Background(), dockerA)
	if err != nil {
		t.Fatal(err)
	}
	pushImage(t, f.db, f.items.Blobs, c, "work/app", "v1", registryManifest("v1"))
}

// registryManifestMatrix: pulls need Read on the image and pushes Deploy;
// a push over a tag that holds another manifest needs Delete too (DV-035),
// while repeating the push it holds needs no more than Deploy. Anonymous
// is challenged whatever it holds.
func registryManifestMatrix() []matrixRow {
	const base = "/v2/" + dockerA + "/work/app/manifests/"
	read, write, del := types.ActionRead, types.ActionWrite, types.ActionDelete
	return []matrixRow{
		row(headManifest, base+"v1", "", holds(dockerA, "work/app/", read), 200),
		row(getManifest, base+sha256Digest(registryManifest("v1")), "",
			holds(dockerA, "work/app/", read), 200),
		// A new tag of the principal's own.
		row(putManifest, base+"{p}", manifestBody(registryManifest("v1")),
			holds(dockerA, "work/app/", write), 201),
		row(putManifest, base+"v1", manifestBody(registryManifest("v1")),
			holds(dockerA, "work/app/", write), 201),
		row(putManifest, base+"v1", manifestBody(registryManifest("other")),
			holds(dockerA, "work/app/v1/", write|del), 201).alone(),
	}
}
