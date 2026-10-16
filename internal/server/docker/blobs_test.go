package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func digestOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// blobWorld is the router over the fixture with a blob store, and the
// stores behind it.
type blobWorld struct {
	t     *testing.T
	db    store.Store
	blobs blob.Store
	h     http.Handler
}

func newBlobWorld(t *testing.T, tune ...func(*Options)) *blobWorld {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &blobWorld{t: t, db: fixture(t), blobs: blobs}
	o := Options{
		Store: w.db, Blobs: blobs, SubdomainMatch: true, BaseURL: "https://reg.example.com",
	}
	for _, f := range tune {
		f(&o)
	}
	w.h = router(New(o))
	return w
}

// do sends a request; headers may set Host.
func (w *blobWorld) do(
	method, target, body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	return send(w.h, method, target, body, headers)
}

func send(
	h http.Handler,
	method, target, body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// deploy stores a file in the repository key.
func (w *blobWorld) deploy(key, path, body string) types.Item {
	w.t.Helper()
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, key)
	if err != nil {
		w.t.Fatal(err)
	}
	it, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

// code is the registry error code of a response, "" for none.
func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Errors []struct{ Code string } }
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil || len(body.Errors) != 1 {
		return ""
	}
	return body.Errors[0].Code
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, status int, errCode string) {
	t.Helper()
	if rec.Code != status || code(t, rec) != errCode {
		t.Fatalf("status %d (%s), want %d (%s): %s", rec.Code, code(t, rec), status, errCode,
			rec.Body)
	}
}

func TestMonolithicUploadAndPull(t *testing.T) {
	w := newBlobWorld(t)
	d := digestOf("layer bytes")
	rec := w.do(http.MethodPost, "/v2/docker-local/team/app/blobs/uploads/?digest="+d,
		"layer bytes", nil)
	expect(t, rec, http.StatusCreated, "")
	if rec.Header().Get("Location") != "/v2/docker-local/team/app/blobs/"+d ||
		rec.Header().Get(contentDigestHeader) != d {
		t.Fatalf("headers %v", rec.Header())
	}
	head := w.do(http.MethodHead, "/v2/docker-local/team/app/blobs/"+d, "", nil)
	expect(t, head, http.StatusOK, "")
	if head.Header().Get("Content-Length") != "11" || head.Header().Get(contentDigestHeader) != d ||
		head.Header().Get("Accept-Ranges") != "bytes" || head.Body.Len() != 0 {
		t.Fatalf("HEAD %v %q", head.Header(), head.Body)
	}
	for _, target := range []string{
		"/v2/docker-local/team/app/blobs/" + d,
		"/artifactory/api/docker/docker-local/v2/team/app/blobs/" + d,
	} {
		get := w.do(http.MethodGet, target, "", nil)
		expect(t, get, http.StatusOK, "")
		if get.Body.String() != "layer bytes" || get.Header().Get(contentDigestHeader) != d {
			t.Fatalf("GET %s: %v %q", target, get.Header(), get.Body)
		}
	}
	part := w.do(http.MethodGet, "/v2/docker-local/team/app/blobs/"+d, "",
		map[string]string{"Range": "bytes=6-10"})
	if part.Code != http.StatusPartialContent || part.Body.String() != "bytes" {
		t.Fatalf("range: %d %q", part.Code, part.Body)
	}
	// Under a subdomain the image is named without the repository.
	sub := w.do(http.MethodGet, "/v2/team/app/blobs/"+d, "",
		map[string]string{"Host": "docker-local.reg.example.com"})
	expect(t, sub, http.StatusOK, "")
}

func TestBlobReadErrors(t *testing.T) {
	w := newBlobWorld(t)
	base := "/v2/docker-local/app/blobs/"
	other := "sha512:" + strings.Repeat("a", 128)
	for name, c := range map[string]struct {
		method, ref string
		status      int
		code        string
	}{
		"unknown":           {http.MethodGet, digestOf("nothing"), 404, CodeBlobUnknown},
		"unknown by HEAD":   {http.MethodHead, digestOf("nothing"), 404, ""},
		"malformed":         {http.MethodGet, "sha256:xyz", 400, CodeDigestInvalid},
		"no algorithm":      {http.MethodGet, "latest", 400, CodeDigestInvalid},
		"upper-case hex":    {http.MethodGet, strings.ToUpper(digestOf("x")), 400, CodeDigestInvalid},
		"another algorithm": {http.MethodGet, other, 404, CodeBlobUnknown},
		"delete":            {http.MethodDelete, digestOf("x"), 405, CodeUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			rec := w.do(c.method, base+c.ref, "", nil)
			if c.method == http.MethodHead {
				if rec.Code != c.status {
					t.Fatalf("status %d", rec.Code)
				}
				return
			}
			expect(t, rec, c.status, c.code)
		})
	}
	if got := w.do(http.MethodDelete, base+digestOf("x"), "", nil).Header().Get(
		"Allow",
	); got != "GET, HEAD" {
		t.Fatalf("Allow %q", got)
	}
	// A file named like a blob of another image is not this image's.
	w.deploy("docker-local", "app/sub/v1/sha256__"+digestOf("deep")[7:], "deep")
	expect(t, w.do(http.MethodGet, base+digestOf("deep"), "", nil), 404, CodeBlobUnknown)
}

func TestChunkedUpload(t *testing.T) {
	w := newBlobWorld(t)
	rec := w.do(http.MethodPost, "/v2/docker-local/app/blobs/uploads/", "", nil)
	expect(t, rec, http.StatusAccepted, "")
	id := rec.Header().Get(uploadUUIDHeader)
	loc := rec.Header().Get("Location")
	if loc != "/v2/docker-local/app/blobs/uploads/"+id || rec.Header().Get("Range") != "0-0" ||
		rec.Header().Get(chunkMinHeader) != "" {
		t.Fatalf("headers %v", rec.Header())
	}
	chunk := func(body, contentRange string) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if contentRange != "" {
			headers["Content-Range"] = contentRange
		}
		return w.do(http.MethodPatch, loc, body, headers)
	}
	rec = chunk("abc", "0-2")
	expect(t, rec, http.StatusAccepted, "")
	if rec.Header().Get("Range") != "0-2" || rec.Header().Get("Location") != loc ||
		rec.Header().Get(uploadUUIDHeader) != id {
		t.Fatalf("headers %v", rec.Header())
	}
	// Out of order: refused with where the session ends.
	rec = chunk("zz", "5-6")
	expect(t, rec, http.StatusRequestedRangeNotSatisfiable, CodeBlobUploadInvalid)
	if rec.Header().Get("Range") != "0-2" || rec.Header().Get("Location") != loc {
		t.Fatalf("headers %v", rec.Header())
	}
	// Without Content-Range the body streams on.
	rec = chunk("def", "")
	expect(t, rec, http.StatusAccepted, "")
	if rec.Header().Get("Range") != "0-5" {
		t.Fatalf("Range %q", rec.Header().Get("Range"))
	}
	status := w.do(http.MethodGet, loc, "", nil)
	expect(t, status, http.StatusNoContent, "")
	if status.Header().Get("Range") != "0-5" || status.Header().Get("Location") != loc {
		t.Fatalf("status headers %v", status.Header())
	}
	d := digestOf("abcdefghi")
	done := w.do(http.MethodPut, loc+"?digest="+d, "ghi", map[string]string{
		"Content-Range": "bytes 6-8/9",
	})
	expect(t, done, http.StatusCreated, "")
	if done.Header().Get("Location") != "/v2/docker-local/app/blobs/"+d ||
		done.Header().Get(contentDigestHeader) != d {
		t.Fatalf("headers %v", done.Header())
	}
	if get := w.do(http.MethodGet, "/v2/docker-local/app/blobs/"+d, "", nil); get.Body.String() !=
		"abcdefghi" {
		t.Fatalf("pulled %q", get.Body)
	}
	expect(t, w.do(http.MethodGet, loc, "", nil), http.StatusNotFound, CodeBlobUploadUnknown)
	if _, err := w.db.Items().Get(
		context.Background(), "docker-local", false, "app/_uploads/sha256__"+d[7:],
	); err != nil {
		t.Fatalf("finished upload: %v", err)
	}
}

func TestUploadErrors(t *testing.T) {
	w := newBlobWorld(t)
	open := func() string {
		rec := w.do(http.MethodPost, "/v2/docker-local/app/blobs/uploads/", "", nil)
		expect(t, rec, http.StatusAccepted, "")
		return rec.Header().Get("Location")
	}
	loc := open()
	cases := []struct {
		name, method, target, body string
		headers                    map[string]string
		status                     int
		code                       string
	}{
		{
			"malformed range", "PATCH", loc, "x",
			map[string]string{"Content-Range": "x"},
			416,
			CodeBlobUploadInvalid,
		},
		{
			"backward range", "PATCH", loc, "",
			map[string]string{"Content-Range": "3-1"},
			416,
			CodeBlobUploadInvalid,
		},
		{"huge range", "PATCH", loc, "", map[string]string{
			"Content-Range": "0-99999999999999999999",
		}, 416, CodeBlobUploadInvalid},
		{
			"length disagrees", "PATCH", loc, "abcd",
			map[string]string{"Content-Range": "0-1"},
			400,
			CodeSizeInvalid,
		},
		{"no digest", "PUT", loc, "", nil, 400, CodeDigestInvalid},
		{"malformed digest", "PUT", loc + "?digest=sha256:zz", "", nil, 400, CodeDigestInvalid},
		{
			"another algorithm", "PUT", loc + "?digest=sha512:" + strings.Repeat("a", 128), "", nil,
			400, CodeDigestInvalid,
		},
		{
			"final chunk out of order", "PUT", loc + "?digest=" + digestOf("x"), "x",
			map[string]string{"Content-Range": "4-4"},
			416, CodeBlobUploadInvalid,
		},
		{
			"final chunk range", "PUT", loc + "?digest=" + digestOf("x"), "x",
			map[string]string{"Content-Range": "?"},
			416, CodeBlobUploadInvalid,
		},
		{"unknown session", "PATCH", "/v2/docker-local/app/blobs/uploads/" + strings.Repeat(
			"0", 32,
		), "x", nil, 404, CodeBlobUploadUnknown},
		{
			"another image's session", "GET", strings.Replace(loc, "/app/", "/other/", 1), "", nil,
			404, CodeBlobUploadUnknown,
		},
		{"unknown session put", "PUT", "/v2/docker-local/app/blobs/uploads/x?digest=" + digestOf(
			"x",
		), "", nil, 404, CodeBlobUploadUnknown},
		{
			"unknown session delete",
			"DELETE",
			"/v2/docker-local/app/blobs/uploads/x",
			"",
			nil,
			404,
			CodeBlobUploadUnknown,
		},
		{
			"digest algorithm",
			"POST",
			"/v2/docker-local/app/blobs/uploads/?digest-algorithm=sha512",
			"",
			nil,
			400,
			CodeDigestInvalid,
		},
		{
			"monolithic malformed", "POST", "/v2/docker-local/app/blobs/uploads/?digest=nope", "x",
			nil, 400, CodeDigestInvalid,
		},
		{"monolithic mismatch", "POST", "/v2/docker-local/app/blobs/uploads/?digest=" + digestOf(
			"y",
		), "x", nil, 400, CodeDigestInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, w.do(c.method, c.target, c.body, c.headers), c.status, c.code)
		})
	}
	// The session survived every refusal above; a mismatch ends it.
	expect(t, w.do(http.MethodGet, loc, "", nil), http.StatusNoContent, "")
	expect(t, w.do(http.MethodPut, loc+"?digest="+digestOf("y"), "x", nil),
		http.StatusBadRequest, CodeDigestInvalid)
	expect(t, w.do(http.MethodGet, loc, "", nil), http.StatusNotFound, CodeBlobUploadUnknown)
	// DELETE cancels.
	loc = open()
	expect(t, w.do(http.MethodDelete, loc, "", nil), http.StatusNoContent, "")
	expect(t, w.do(http.MethodPatch, loc, "x", nil), http.StatusNotFound, CodeBlobUploadUnknown)
}

// partSized is a blob store that writes in parts.
type partSized struct{ blob.Store }

func (partSized) PartSize() int64 { return 5 << 20 }

func TestChunkMinimumIsAnnounced(t *testing.T) {
	w := newBlobWorld(t, func(o *Options) { o.Blobs = partSized{o.Blobs} })
	rec := w.do(http.MethodPost, "/v2/docker-local/app/blobs/uploads/", "", nil)
	expect(t, rec, http.StatusAccepted, "")
	if got := rec.Header().Get(chunkMinHeader); got != strconv.Itoa(5<<20) {
		t.Fatalf("%s %q", chunkMinHeader, got)
	}
}

func TestMount(t *testing.T) {
	w := newBlobWorld(t)
	d := digestOf("shared")
	w.deploy("oci-local", "base/v1/sha256__"+d[7:], "shared")
	w.deploy("docker-local", "base/v1/sha256__"+d[7:], "shared")
	uploads := func(target string) string { return target + "blobs/uploads/?mount=" + d + "&from=" }
	cases := []struct {
		name, target, from, host string
		status                   int
	}{
		{"repository path", uploads("/v2/docker-local/app/"), "oci-local/base", "", 201},
		{"prefix, repository form", uploads(
			"/artifactory/api/docker/docker-local/v2/app2/",
		), "oci-local/base", "", 201},
		{"prefix, same repository", uploads(
			"/artifactory/api/docker/docker-local/v2/app3/",
		), "base", "", 201},
		{"subdomain", uploads("/v2/app4/"), "base", "docker-local.reg.example.com", 201},
		{
			"repository path without a repository",
			uploads("/v2/docker-local/app5/"),
			"base",
			"",
			202,
		},
		{
			"subdomain naming a repository", uploads("/v2/app6/"), "oci-local/base",
			"docker-local.reg.example.com", 202,
		},
		{"remote source", uploads("/v2/docker-local/app7/"), "docker-remote/base", "", 202},
		{"generic source", uploads("/v2/docker-local/app7/"), "generic-local/base", "", 202},
		{"invalid name", uploads("/v2/docker-local/app7/"), "oci-local/Base", "", 202},
		{"not there", uploads("/v2/docker-local/app7/"), "oci-local/other", "", 202},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := w.do(http.MethodPost, c.target+c.from, "", map[string]string{"Host": c.host})
			expect(t, rec, c.status, "")
			if c.status == 201 && rec.Header().Get(contentDigestHeader) != d {
				t.Fatalf("headers %v", rec.Header())
			}
		})
	}
	// mount without from, and a malformed digest, open a session.
	expect(t, w.do(http.MethodPost, "/v2/docker-local/app8/blobs/uploads/?mount="+d, "", nil),
		202, "")
	expect(t, w.do(http.MethodPost,
		"/v2/docker-local/app8/blobs/uploads/?mount=nope&from=oci-local/base", "", nil), 202, "")
	if _, err := w.db.Items().Get(
		context.Background(), "docker-local", false, "app/_uploads/sha256__"+d[7:],
	); err != nil {
		t.Fatalf("mounted upload: %v", err)
	}
}

// principalAs puts the principal name holds into each request, as the
// authentication middleware would.
func principalAs(t *testing.T, db store.Store, h http.Handler, name string) http.Handler {
	t.Helper()
	p := auth.Principal{Name: name}
	if name == auth.AnonymousUser {
		p = auth.Anonymous
	}
	p, err := auth.Authorize(context.Background(), authz.Sources{
		Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

func TestPermissions(t *testing.T) {
	w := newBlobWorld(t)
	ctx := context.Background()
	grant := func(name string, users map[string]types.Actions, key string) {
		if err := w.db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: users},
				Targets: map[string]types.Target{key: {Include: []string{"**"}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	grant("docker", map[string]types.Actions{
		"reader": types.ActionRead, "writer": types.ActionRead | types.ActionWrite,
		"other": types.ActionRead | types.ActionWrite,
	}, "docker-local")
	grant("oci", map[string]types.Actions{"reader": types.ActionRead}, "oci-local")
	d := digestOf("p")
	w.deploy("docker-local", "app/v1/sha256__"+d[7:], "p")
	w.deploy("oci-local", "base/v1/sha256__"+d[7:], "p")
	as := func(name string) http.Handler { return principalAs(t, w.db, w.h, name) }
	blobPath := "/v2/docker-local/app/blobs/" + d
	uploads := "/v2/docker-local/app/blobs/uploads/"
	for _, c := range []struct {
		who, method, target string
		status              int
		code                string
	}{
		{"reader", "GET", blobPath, 200, ""},
		{"nobody", "GET", blobPath, 403, CodeDenied},
		{auth.AnonymousUser, "HEAD", blobPath, 401, ""},
		{"reader", "POST", uploads, 403, CodeDenied},
		{auth.AnonymousUser, "POST", uploads, 401, CodeUnauthorized},
		{"writer", "POST", uploads, 202, ""},
		// writer may not read oci-local, so the mount opens a session.
		{"writer", "POST", uploads + "?mount=" + d + "&from=oci-local/base", 202, ""},
		{"reader", "PATCH", uploads + strings.Repeat("0", 32), 403, CodeDenied},
		{"reader", "PUT", uploads + strings.Repeat("0", 32) + "?digest=" + d, 403, CodeDenied},
		{"reader", "GET", uploads + strings.Repeat("0", 32), 403, CodeDenied},
		{"reader", "DELETE", uploads + strings.Repeat("0", 32), 403, CodeDenied},
	} {
		rec := send(as(c.who), c.method, c.target, "", nil)
		if c.method == http.MethodHead {
			if rec.Code != c.status {
				t.Errorf("%s %s %s: %d", c.who, c.method, c.target, rec.Code)
			}
			continue
		}
		if rec.Code != c.status || code(t, rec) != c.code {
			t.Errorf("%s %s %s: %d %s", c.who, c.method, c.target, rec.Code, rec.Body)
		}
	}
	// A session is its opener's: another writer cannot continue it.
	rec := send(as("writer"), http.MethodPost, uploads, "", nil)
	loc := rec.Header().Get("Location")
	expect(t, send(as("other"), http.MethodPatch, loc, "x", nil), 404, CodeBlobUploadUnknown)
	expect(t, send(as("writer"), http.MethodPatch, loc, "x", nil), 202, "")
}

func TestAFileInTheWay(t *testing.T) {
	w := newBlobWorld(t)
	w.deploy("docker-local", "app", "not a folder")
	expect(t, w.do(http.MethodPost,
		"/v2/docker-local/app/blobs/uploads/?digest="+digestOf("x"), "x", nil),
		http.StatusBadRequest, CodeNameInvalid)
}

func TestAbsoluteLocations(t *testing.T) {
	w := newBlobWorld(t)
	h := baseurl.Middleware("https://reg.example.com")(w.h)
	for name, c := range map[string]struct{ target, host, want string }{
		"repository path": {
			"/v2/docker-local/app/blobs/uploads/", "",
			"https://reg.example.com/v2/docker-local/app/blobs/uploads/",
		},
		"prefix": {
			"/artifactory/api/docker/docker-local/v2/app/blobs/uploads/", "",
			"https://reg.example.com/artifactory/api/docker/docker-local/v2/app/blobs/uploads/",
		},
		"subdomain": {
			"/v2/app/blobs/uploads/", "docker-local.reg.example.com:5000",
			"https://docker-local.reg.example.com:5000/v2/app/blobs/uploads/",
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := send(h, http.MethodPost, c.target, "", map[string]string{"Host": c.host})
			if got := rec.Header().Get("Location"); !strings.HasPrefix(got, c.want) {
				t.Fatalf("Location %q, want %q...", got, c.want)
			}
		})
	}
	// Behind a proxy the forwarded host names the subdomain.
	rec := send(baseurl.Middleware("https://reg.example.com")(w.h), http.MethodPost,
		"/v2/app/blobs/uploads/", "", map[string]string{
			"Host": "internal:8081", "X-Forwarded-Host": "docker-local.reg.example.com, proxy",
			"X-Forwarded-Proto": "https",
		})
	if got := rec.Header().Get("Location"); !strings.HasPrefix(
		got, "https://docker-local.reg.example.com/v2/app/blobs/uploads/",
	) {
		t.Fatalf("Location %q", got)
	}
}

func TestServeBlobHook(t *testing.T) {
	var served types.Item
	var in repo.Config
	w := newBlobWorld(t, func(o *Options) {
		o.ServeBlob = func(w http.ResponseWriter, _ *http.Request, c repo.Config, it types.Item) {
			served, in = it, c
			w.WriteHeader(http.StatusTeapot)
		}
	})
	d := digestOf("hooked")
	w.deploy("docker-local", "app/_uploads/sha256__"+d[7:], "hooked")
	rec := w.do(http.MethodGet, "/v2/docker-local/app/blobs/"+d, "", nil)
	if rec.Code != http.StatusTeapot || in.Key != "docker-local" ||
		served.FullPath() != "app/_uploads/sha256__"+d[7:] ||
		rec.Header().Get(contentDigestHeader) != d {
		t.Fatalf("%d %+v %+v", rec.Code, in, served)
	}
}

// generator is size bytes of a pattern, made as they are read.
type generator struct{ size, read int64 }

func (g *generator) Read(p []byte) (int, error) {
	if g.read >= g.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), g.size-g.read))
	for i := range n {
		p[i] = byte((g.read + int64(i)) % 251)
	}
	g.read += int64(n)
	return n, nil
}

// heapPeak tracks the largest live heap seen while a reader is read.
type heapPeak struct {
	r      io.Reader
	every  int64
	next   int64
	read   int64
	peak   uint64
	sample func() uint64
}

func (h *heapPeak) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.read += int64(n)
	if h.read >= h.next {
		h.next += h.every
		h.peak = max(h.peak, h.sample())
	}
	return n, err //nolint:wrapcheck // a pass-through reader
}

func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// A layer streams from the request into the blob store: the live heap
// while it is read stays far below its size, chunked or monolithic.
func TestLayersStreamWithoutBuffering(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 128 MiB twice")
	}
	const size = 128 << 20
	w := newBlobWorld(t)
	h := sha256.New()
	_, _ = io.Copy(h, &generator{size: size})
	d := "sha256:" + hex.EncodeToString(h.Sum(nil))
	upload := func(method, target string) (*httptest.ResponseRecorder, uint64) {
		base := liveHeap()
		body := &heapPeak{r: &generator{size: size}, every: 16 << 20, sample: liveHeap}
		body.next = body.every
		req := httptest.NewRequest(method, target, body)
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, req)
		return rec, body.peak - min(base, body.peak)
	}
	rec := w.do(http.MethodPost, "/v2/docker-local/big/blobs/uploads/", "", nil)
	loc := rec.Header().Get("Location")
	rec, grew := upload(http.MethodPatch, loc)
	t.Logf("chunked: the live heap grew %d KiB", grew>>10)
	expect(t, rec, http.StatusAccepted, "")
	if grew > 32<<20 {
		t.Fatalf("a chunked upload held %d MiB of a %d MiB layer", grew>>20, size>>20)
	}
	expect(t, w.do(http.MethodPut, loc+"?digest="+d, "", nil), http.StatusCreated, "")
	rec, grew = upload(http.MethodPost, "/v2/docker-local/big2/blobs/uploads/?digest="+d)
	t.Logf("monolithic: the live heap grew %d KiB", grew>>10)
	expect(t, rec, http.StatusCreated, "")
	if grew > 32<<20 {
		t.Fatalf("a monolithic upload held %d MiB of a %d MiB layer", grew>>20, size>>20)
	}
}

// failingItems fails searches, so finding a blob fails.
type failingItems struct{ store.ItemStore }

func (failingItems) Search(context.Context, *aql.Query, aql.Options) ([]aql.Hit, error) {
	return nil, errBoom
}

type itemsFault struct {
	store.Store
	items store.ItemStore
}

func (f itemsFault) Items() store.ItemStore { return f.items }

// failingBlobs fails to open content and to begin uploads.
type failingBlobs struct{ blob.Store }

func (failingBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errBoom
}

func (failingBlobs) BeginUpload(context.Context, ...blob.UploadOption) (blob.Upload, error) {
	return nil, errBoom
}

func TestBlobStoreAndIndexFailures(t *testing.T) {
	d := digestOf("f")
	for name, c := range map[string]struct {
		tune           func(*blobWorld, *Options)
		method, target string
	}{
		"find": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		}, http.MethodGet, "/v2/docker-local/app/blobs/" + d},
		"mount": {func(w *blobWorld, o *Options) {
			o.Store = itemsFault{Store: w.db, items: failingItems{w.db.Items()}}
		}, http.MethodPost, "/v2/docker-local/app/blobs/uploads/?mount=" + d + "&from=oci-local/b"},
		"open": {func(w *blobWorld, o *Options) {
			o.Blobs = failingBlobs{w.blobs}
		}, http.MethodGet, "/v2/docker-local/app/blobs/" + d},
		"begin": {func(w *blobWorld, o *Options) {
			o.Blobs = failingBlobs{w.blobs}
		}, http.MethodPost, "/v2/docker-local/app/blobs/uploads/"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			w.deploy("docker-local", "app/_uploads/sha256__"+d[7:], "f")
			w.deploy("oci-local", "b/v1/sha256__"+d[7:], "f")
			var noted error
			o := Options{
				Store: w.db, Blobs: w.blobs,
				Fault: func(_ *http.Request, err error) { noted = err },
			}
			c.tune(w, &o)
			h := router(New(o))
			rec := send(h, c.method, c.target, "", nil)
			if rec.Code != http.StatusInternalServerError || noted == nil {
				t.Fatalf("%d %s, noted %v", rec.Code, rec.Body, noted)
			}
		})
	}
}
