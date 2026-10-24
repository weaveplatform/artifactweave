package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteWorld struct {
	t  *testing.T
	rt *runtime
	h  http.Handler
	up *fakeupstream.Server
	c  repo.Config
}

func newRemoteWorld(t *testing.T) *remoteWorld {
	t.Helper()
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	rt, err := openRuntime(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rt.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: up.URL})
	if err = rt.store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPasswordWith(authHash, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reader", "denied"} {
		if err = rt.store.Users().
			Create(t.Context(), types.User{Name: name, PasswordHash: hash}); err != nil {
			t.Fatal(err)
		}
	}
	if err = rt.store.Permissions().
		Create(t.Context(), types.Permission{Name: "remote-read", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{"remote": {}},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	h := server.New(
		server.Config{
			Store:         rt.store,
			Blobs:         rt.blobs,
			Auth:          rt.auth,
			Tokens:        rt.tokens,
			Keys:          rt.keys,
			RemoteSources: rt.remoteSources,
			RemoteHTTP:    &rt.remoteHTTP,
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	return &remoteWorld{t: t, rt: rt, h: h, up: up, c: c}
}

func (w *remoteWorld) request(
	method, path, user, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	for i := 0; i < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

func remoteStatus(t *testing.T, r *httptest.ResponseRecorder, code int) {
	t.Helper()
	if r.Code != code {
		t.Fatalf("status %d want %d: %s", r.Code, code, r.Body)
	}
}

func (w *remoteWorld) update() {
	w.t.Helper()
	if err := w.rt.store.Repos().Update(w.t.Context(), w.c); err != nil {
		w.t.Fatal(err)
	}
}

func TestAccGenericRemoteDownloadLifecycle(t *testing.T) {
	w := newRemoteWorld(t)
	body := "0123456789 artifact bytes"
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	modified := time.Unix(1700000000, 0).UTC()
	w.c.PropagateQueryParams = new(true)
	w.update()
	w.up.Set(
		"/a/file.bin",
		fakeupstream.Resource{
			Body:     []byte(body),
			Modified: modified,
			Header:   http.Header{"X-Checksum-Sha256": {digest}},
		},
	)
	for _, method := range []string{"GET", "HEAD"} {
		remoteStatus(
			t,
			w.request(method, "/artifactory/remote-cache/a/file.bin", "reader", ""),
			404,
		)
	}
	if len(w.up.Requests()) != 0 {
		t.Fatal("cache miss fetched")
	}
	head := w.request("HEAD", "/artifactory/remote/a/file.bin", "reader", "")
	remoteStatus(t, head, 200)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "25" ||
		head.Header().Get("X-Checksum-Sha256") != digest {
		t.Fatal(head.Header(), head.Body)
	}
	if _, err := w.rt.store.Items().
		Get(t.Context(), "remote", true, "a/file.bin"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("HEAD cached: %v", err)
	}
	part := w.request("GET", "/artifactory/remote/a/file.bin", "reader", "", "Range", "bytes=3-8")
	remoteStatus(t, part, 206)
	if part.Body.String() != body[3:9] {
		t.Fatal(part.Body)
	}
	it, err := w.rt.store.Items().Get(t.Context(), "remote", true, "a/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	if it.Size != int64(len(body)) || it.Actual.SHA256 != digest ||
		it.RemoteURL != w.up.URL+"/a/file.bin" ||
		!it.Modified.Equal(modified) {
		t.Fatalf("item %+v", it)
	}
	count := len(w.up.Requests())
	etag := part.Header().Get("ETag")
	for _, tc := range []struct {
		method, path, header, value string
		status                      int
		body                        string
	}{
		{"GET", "remote/a/file.bin", "", "", 200, body},
		{"GET", "remote-cache/a/file.bin", "", "", 200, body},
		{"GET", "remote/a/file.bin", "If-None-Match", etag, 304, ""},
		{"HEAD", "remote/a/file.bin", "If-Modified-Since", modified.Format(http.TimeFormat), 304, ""},
		{"GET", "remote/a/file.bin.sha256", "", "", 200, digest},
		{"GET", "remote/a/file.bin;env=prod", "", "", 200, body},
		{"GET", "remote/a/file.bin;env+=prod", "", "", 404, ""},
	} {
		var h []string
		if tc.header != "" {
			h = []string{tc.header, tc.value}
		}
		got := w.request(tc.method, "/artifactory/"+tc.path, "reader", "", h...)
		remoteStatus(t, got, tc.status)
		if tc.status != 404 && got.Body.String() != tc.body {
			t.Fatalf("%s body %q", tc.path, got.Body)
		}
	}
	if len(w.up.Requests()) != count {
		t.Fatal("warm request fetched")
	}
	stats, err := w.rt.store.Items().Stats(t.Context(), it.ID)
	if err != nil || stats.DownloadCount != 3 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	for _, path := range []string{"/artifactory/api/storage/remote-cache/a/file.bin", "/artifactory/api/storage/remote-cache/a?list&deep=1", "/artifactory/remote/a"} {
		remoteStatus(t, w.request("GET", path, "reader", ""), 200)
	}
	aql := w.request(
		"POST",
		"/artifactory/api/search/aql",
		"reader",
		`items.find({"repo":"remote-cache"}).include("repo","path","name")`,
	)
	remoteStatus(t, aql, 200)
	if !strings.Contains(aql.Body.String(), `"repo": "remote-cache"`) &&
		!strings.Contains(aql.Body.String(), `"repo":"remote-cache"`) {
		t.Fatal(aql.Body)
	}
	w.up.Close()
	got := w.request("GET", "/artifactory/remote/a/file.bin", "reader", "")
	remoteStatus(t, got, 200)
	if got.Body.String() != body {
		t.Fatal(got.Body)
	}
	remoteStatus(t, w.request("DELETE", "/artifactory/api/repositories/remote", "admin", ""), 200)
	if _, err = w.rt.store.RemoteCache().
		Source(t.Context(), "remote"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("source retained %v", err)
	}
}

func TestAccGenericRemoteTransientSidecarsAndFailures(t *testing.T) {
	w := newRemoteWorld(t)
	w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("0123456789")})
	w.c.StoreArtifactsLocally = new(false)
	w.update()
	got := w.request("GET", "/artifactory/remote/file.bin", "reader", "", "Range", "bytes=4-6")
	remoteStatus(t, got, 206)
	if got.Body.String() != "456" {
		t.Fatal(got.Body)
	}
	etag := got.Header().Get("ETag")
	remoteStatus(
		t,
		w.request("GET", "/artifactory/remote/file.bin", "reader", "", "If-None-Match", etag),
		304,
	)
	remoteStatus(t, w.request("GET", "/artifactory/remote-cache/file.bin", "reader", ""), 404)
	if entries, _, err := w.rt.store.RemoteCache().
		List(t.Context(), "remote", "", store.Page{}); err != nil ||
		len(entries) != 0 {
		t.Fatalf("transient entries %v %v", entries, err)
	}
	before := len(w.up.Requests())
	remoteStatus(t, w.request("GET", "/artifactory/remote/cold.bin.sha256", "reader", ""), 404)
	if before != len(w.up.Requests()) {
		t.Fatal("disabled cold checksum fetched")
	}
	w.c.RetrieveSha256FromServer = new(true)
	w.c.StoreArtifactsLocally = new(true)
	w.update()
	checksum := strings.Repeat("a", 64)
	w.up.Set("/cold.bin.sha256", fakeupstream.Resource{Body: []byte(checksum)})
	got = w.request("GET", "/artifactory/remote/cold.bin.sha256", "reader", "")
	remoteStatus(t, got, 200)
	if got.Body.String() != checksum || w.up.Count("GET", "/cold.bin") != 0 {
		t.Fatal("sidecar fetched artifact", got.Body)
	}
	remoteStatus(
		t,
		w.request("GET", "/artifactory/remote-cache/cold.bin.sha256", "reader", ""),
		404,
	)
	w.up.Set("/large.bin.sha256", fakeupstream.Resource{Body: []byte(strings.Repeat("a", 5000))})
	remoteStatus(t, w.request("GET", "/artifactory/remote/large.bin.sha256", "reader", ""), 502)
	for _, status := range []int{401, 403, 500, 503, 429} {
		path := "/error.bin"
		w.up.Set(
			path,
			fakeupstream.Resource{
				Status: status,
				Body:   []byte("upstream-secret"),
				Header: http.Header{"WWW-Authenticate": {"secret"}, "Retry-After": {"30"}},
			},
		)
		got = w.request("GET", "/artifactory/remote"+path, "reader", "")
		remoteStatus(t, got, status)
		if strings.Contains(got.Body.String(), "secret") ||
			got.Header().Get("WWW-Authenticate") != "" {
			t.Fatal(got.Body, got.Header())
		}

	}
}

func TestAccGenericRemoteAuthorizationAndRevocation(t *testing.T) {
	for _, mutation := range []string{"permission", "user", "password", "source", "proxy"} {
		t.Run(mutation, func(t *testing.T) {
			w := newRemoteWorld(t)
			barrier := &fakeupstream.Barrier{}
			t.Cleanup(barrier.Release)
			w.up.Set(
				"/held.bin",
				fakeupstream.Resource{
					Body:  []byte("held"),
					Fault: fakeupstream.Fault{BeforeBody: barrier},
				},
			)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- w.request("GET", "/artifactory/remote/held.bin", "reader", "") }()
			select {
			case <-barrier.Reached():
			case <-time.After(10 * time.Second):
				t.Fatal("fetch did not start")
			}
			var err error
			switch mutation {
			case "permission":
				err = w.rt.store.Permissions().Delete(t.Context(), "remote-read")
			case "user", "password":
				u, e := w.rt.store.Users().Get(t.Context(), "reader")
				if e != nil {
					t.Fatal(e)
				}
				if mutation == "user" {
					u.Status = types.UserDisabled
				} else {
					u.PasswordHash = []byte("replacement")
				}
				err = w.rt.store.Users().Update(t.Context(), u)
			case "source":
				w.c.URL = w.up.URL + "/changed"
				w.update()
			case "proxy":
				w.c.Password = "new-upstream-password"
				w.update()
			}
			if err != nil {
				t.Fatal(err)
			}
			barrier.Release()
			var got *httptest.ResponseRecorder
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("request did not stop")
			}
			if got.Code == 200 {
				t.Fatal("revoked read succeeded")
			}
			if _, err = w.rt.store.Items().
				Get(t.Context(), "remote", true, "held.bin"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("revoked candidate published %v", err)
			}
		})
	}
	w := newRemoteWorld(t)
	w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("file")})
	for _, method := range []string{"GET", "HEAD"} {
		remoteStatus(t, w.request(method, "/artifactory/remote/file.bin", "denied", ""), 403)
	}
	remoteStatus(t, w.request("PUT", "/artifactory/remote/file.bin", "admin", "upload"), 405)
	w.c.ExcludesPattern = new("**")
	w.update()
	remoteStatus(t, w.request("GET", "/artifactory/remote/file.bin", "reader", ""), 404)
	w.c.ExcludesPattern = nil
	w.c.BlackedOut = new(true)
	w.update()
	remoteStatus(t, w.request("GET", "/artifactory/remote/file.bin", "reader", ""), 404)
	if len(w.up.Requests()) != 0 {
		t.Fatal("denial fetched")
	}
}

func TestAccGenericRemoteTraceFetchesWithoutDownload(t *testing.T) {
	w := newRemoteWorld(t)
	w.up.Set("/trace.bin", fakeupstream.Resource{Body: []byte("trace bytes")})
	got := w.request("GET", "/artifactory/remote/trace.bin?trace", "reader", "")
	remoteStatus(t, got, 200)
	if !strings.Contains(got.Body.String(), "remote-cache:trace.bin") ||
		!strings.Contains(got.Body.String(), "Request succeeded") {
		t.Fatal(got.Body)
	}
	it, err := w.rt.store.Items().Get(t.Context(), "remote", true, "trace.bin")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := w.rt.store.Items().Stats(t.Context(), it.ID)
	if err != nil || stats.DownloadCount != 0 {
		t.Fatalf("trace counted %v %v", stats, err)
	}
	if w.up.Count("GET", "/trace.bin") != 1 {
		t.Fatal(w.up.Requests())
	}
}

func TestAccGenericRemoteDefaultRestartKeepsCacheIdentity(t *testing.T) {
	cfg := testConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := openRuntime(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	up := fakeupstream.New(t, fakeupstream.Options{})
	up.Set("/restart.bin", fakeupstream.Resource{Body: []byte("survives restart")})
	c := *repo.Defaults(&repo.Config{Key: "restart", Class: repo.Remote, PackageType: repo.Generic, URL: up.URL})
	if err = rt.store.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	get := func() *httptest.ResponseRecorder {
		h := server.New(
			server.Config{
				Store:         rt.store,
				Blobs:         rt.blobs,
				Auth:          rt.auth,
				RemoteSources: rt.remoteSources,
				Logger:        log,
			},
		)
		r := httptest.NewRequest("GET", "/artifactory/restart/restart.bin", nil)
		r.SetBasicAuth("admin", "pw")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	remoteStatus(t, get(), 200)
	before, err := rt.store.RemoteCache().Source(t.Context(), c.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	rt, err = openRuntime(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	after, err := rt.store.RemoteCache().Source(t.Context(), c.Key)
	if err != nil || after != before {
		t.Fatalf("identity %v %v %v", before, after, err)
	}
	up.Close()
	got := get()
	remoteStatus(t, got, 200)
	if got.Body.String() != "survives restart" {
		t.Fatal(got.Body)
	}
	// No public configuration descriptor exposes the private internal setting.
	r := httptest.NewRequest("GET", "/artifactory/api/system/configuration", nil)
	r.SetBasicAuth("admin", "pw")
	h := server.New(server.Config{Store: rt.store, Auth: rt.auth, Logger: log})
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if strings.Contains(out.Body.String(), remotePolicyKeySetting) {
		t.Fatal("internal key exposed")
	}
}

type remoteSignedBlobs struct {
	blob.Store
	edge string
}

func (b remoteSignedBlobs) Signs(via blob.Via) bool { return via == blob.ViaCDN }

func (b remoteSignedBlobs) SignURL(
	_ context.Context,
	sha string,
	_ blob.SignOptions,
) (string, error) {
	return b.edge + "/" + sha, nil
}

func TestAccGenericRemoteColdAndWarmSignedDelivery(t *testing.T) {
	w := newRemoteWorld(t)
	w.up.Set("/signed.bin", fakeupstream.Resource{Body: []byte("signed identical bytes")})
	w.c.DownloadRedirect = repo.Flex(true)
	w.update()
	router := chi.NewRouter()
	router.Get("/{sha}", func(out http.ResponseWriter, r *http.Request) {
		body, _, err := w.rt.blobs.Open(r.Context(), chi.URLParam(r, "sha"))
		if err != nil {
			http.Error(out, "missing", 404)
			return
		}
		defer func() { _ = body.Close() }()
		_, _ = io.Copy(out, body)
	})
	edge := httptest.NewServer(router)
	defer edge.Close()
	w.h = server.New(
		server.Config{
			Store:         w.rt.store,
			Blobs:         remoteSignedBlobs{Store: w.rt.blobs, edge: edge.URL},
			Auth:          w.rt.auth,
			RemoteSources: w.rt.remoteSources,
			Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	)
	for range 2 {
		got := w.request("GET", "/artifactory/remote/signed.bin", "reader", "")
		remoteStatus(t, got, 302)
		req, err := http.NewRequestWithContext(
			t.Context(),
			"GET",
			got.Header().Get("Location"),
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		res, err := edge.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil || string(b) != "signed identical bytes" {
			t.Fatalf("signed body %q %v", b, err)
		}
	}
	if w.up.Count("GET", "/signed.bin") != 1 {
		t.Fatal(w.up.Requests())
	}
	it, err := w.rt.store.Items().Get(t.Context(), "remote", true, "signed.bin")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := w.rt.store.Items().Stats(t.Context(), it.ID)
	if err != nil || stats.DownloadCount != 2 {
		t.Fatalf("signed stats %+v %v", stats, err)
	}
}

func TestAccGenericRemoteOriginOmitsSignedQuery(t *testing.T) {
	w := newRemoteWorld(t)
	w.c.PropagateQueryParams = new(true)
	w.update()
	w.up.Set("/signed-query.bin", fakeupstream.Resource{Body: []byte("query body")})
	got := w.request(
		"GET",
		"/artifactory/remote/signed-query.bin?token=secret%20value&token=second",
		"reader",
		"",
	)
	remoteStatus(t, got, 200)
	it, err := w.rt.store.Items().Get(t.Context(), "remote", true, "signed-query.bin")
	if err != nil {
		t.Fatal(err)
	}
	if it.RemoteURL != w.up.URL+"/signed-query.bin" {
		t.Fatalf("origin leaked query: %q", it.RemoteURL)
	}
}

func TestAccGenericRemoteRejectsMalformedColdChecksum(t *testing.T) {
	for _, data := range []string{"not-a-digest", "<html>login</html>", "", strings.Repeat("a", 63)} {
		t.Run(fmt.Sprintf("size%d", len(data)), func(t *testing.T) {
			w := newRemoteWorld(t)
			w.c.RetrieveSha256FromServer = new(true)
			w.update()
			w.up.Set("/invalid.bin.sha256", fakeupstream.Resource{Body: []byte(data)})
			remoteStatus(
				t,
				w.request("GET", "/artifactory/remote/invalid.bin.sha256", "reader", ""),
				502,
			)
			if _, err := w.rt.store.Items().
				Get(t.Context(), "remote", true, "invalid.bin.sha256"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("sidecar persisted %v", err)
			}
		})
	}
}

func TestAccRemotePolicyKeyInitialization(t *testing.T) {
	rt := &runtime{store: inmem.New()}
	const count = 8
	keys := make(chan []byte, count)
	errs := make(chan error, count)
	for range count {
		go func() { key, err := rt.remotePolicyKey(t.Context()); keys <- key; errs <- err }()
	}
	var first []byte
	for range count {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		key := <-keys
		if first == nil {
			first = key
		}
		if len(key) != 32 || !bytes.Equal(first, key) {
			t.Fatal("initialization disagreed")
		}
	}
	if err := rt.store.Settings().
		Put(t.Context(), remotePolicyKeySetting, []byte("short")); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.remotePolicyKey(t.Context()); err == nil {
		t.Fatal("corrupt stored key accepted")
	}
}

func TestAccGenericRemoteColdChecksumHeadDescribesNormalizedGet(t *testing.T) {
	w := newRemoteWorld(t)
	w.c.RetrieveSha256FromServer = new(true)
	w.update()
	digest := strings.Repeat("b", 64)
	w.up.Set(
		"/named.bin.sha256",
		fakeupstream.Resource{
			Body:   []byte(digest + "  named.bin\n"),
			Header: http.Header{"Content-Type": {"text/plain"}},
		},
	)
	head := w.request("HEAD", "/artifactory/remote/named.bin.sha256", "reader", "")
	remoteStatus(t, head, 200)
	if head.Header().Get("Content-Length") != "64" ||
		head.Header().Get("Content-Type") != "text/plain" ||
		head.Header().Get("ETag") != "" ||
		head.Header().Get("X-Checksum-Sha256") != "" ||
		head.Body.Len() != 0 {
		t.Fatal(head.Header(), head.Body)
	}
	if w.up.Count("GET", "/named.bin.sha256") != 0 {
		t.Fatal("HEAD requested a checksum body")
	}
	got := w.request("GET", "/artifactory/remote/named.bin.sha256", "reader", "")
	remoteStatus(t, got, 200)
	if got.Body.String() != digest || got.Header().Get("Content-Length") != "64" ||
		got.Header().Get("ETag") != "" {
		t.Fatal(got.Header(), got.Body)
	}
}
