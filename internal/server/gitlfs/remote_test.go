package gitlfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/gitlfs"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteFixture struct {
	server           *httptest.Server
	upstream         *httptest.Server
	object           []byte
	oid              string
	batches          atomic.Int32
	objects          atomic.Int32
	badHash          bool
	badAction        bool
	redirect         bool
	redirectURL      string
	actionHeader     map[string]string
	actionAuthSeen   atomic.Bool
	leakedActionAuth atomic.Bool
	available        atomic.Bool
}

func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := &remoteFixture{object: []byte("remote Git LFS content\n")}
	sum := sha256.Sum256(f.object)
	f.oid = hex.EncodeToString(sum[:])
	f.available.Store(true)
	crossTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Action-Token") != "" {
			f.leakedActionAuth.Store(true)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(f.object)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(f.object)
		}
	}))
	t.Cleanup(crossTarget.Close)
	f.redirectURL = crossTarget.URL + "/object"
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !f.available.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		base := "/artifactory/api/lfs/origin"
		switch r.URL.Path {
		case base + "/objects/batch":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/vnd.git-lfs+json" {
				http.Error(w, "invalid batch", 400)
				return
			}
			f.batches.Add(1)
			var request struct {
				Operation string `json:"operation"`
				Objects   []struct {
					OID  string `json:"oid"`
					Size int64  `json:"size"`
				} `json:"objects"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Operation != "download" || len(request.Objects) != 1 || request.Objects[0].OID != f.oid || request.Objects[0].Size != int64(len(f.object)) {
				http.Error(w, "wrong batch", 400)
				return
			}
			href := f.upstream.URL + base + "/objects/" + f.oid
			if f.badAction {
				href = "https://untrusted.invalid/objects/" + f.oid
			}
			w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(w).Encode(map[string]any{"transfer": "basic", "objects": []any{map[string]any{"oid": f.oid, "size": len(f.object), "actions": map[string]any{"download": map[string]any{"href": href, "header": f.actionHeader}}}}})
		case base + "/objects/" + f.oid:
			if f.actionHeader != nil && r.Header.Get("Authorization") == f.actionHeader["Authorization"] &&
				r.Header.Get("X-Action-Token") == f.actionHeader["X-Action-Token"] {
				f.actionAuthSeen.Store(true)
			} else if f.actionHeader != nil {
				http.Error(w, "action auth required", http.StatusUnauthorized)
				return
			}
			if f.redirect {
				location := "/artifactory/origin/objects/" + f.oid[:2] + "/" + f.oid[2:4] + "/" + f.oid
				if f.actionHeader != nil {
					location = f.redirectURL
				}
				http.Redirect(w, r, location, http.StatusFound)
				return
			}
			fallthrough
		case "/artifactory/origin/objects/" + f.oid[:2] + "/" + f.oid[2:4] + "/" + f.oid:
			if r.Method == http.MethodGet {
				f.objects.Add(1)
			}
			body := f.object
			if f.badHash {
				body = bytes.Repeat([]byte("x"), len(body))
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.upstream.Close)
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "lfs", Class: repo.Remote, PackageType: repo.GitLFS, URL: f.upstream.URL + "/artifactory/api/lfs/origin"})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	roots := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: crossTarget.Certificate().Raw})
	resolver, err := remote.NewResolver(db, nil, remote.ResolverOptions{
		RevisionKey: []byte(strings.Repeat("k", 32)),
		RootCAsPEM:  roots, AllowedPrivateOrigins: []string{crossTarget.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{Store: db, Blobs: blobs, Items: items, Pool: new(remote.Pool), Key: []byte(strings.Repeat("k", 32)), Options: resolver.Options}
	t.Cleanup(policy.Pool.Close)
	svc := &source.Service{HTTP: policy, Cache: policy, Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
		return db.RemoteCache().Source(ctx, c.Key)
	}}
	router := chi.NewRouter()
	router.Use(baseurl.Middleware(""))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, e := auth.Authorize(r.Context(), authz.Sources{Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true}, auth.Principal{Name: "admin", Admin: true})
			if e != nil {
				t.Error(e)
				http.Error(w, e.Error(), 500)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(router, pkgroute.Options{Store: db, Items: func() *item.Service { return items }, ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
		body, _, e := blobs.Open(r.Context(), it.Actual.SHA256)
		if e != nil {
			t.Error(e)
			http.Error(w, e.Error(), 500)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Length", fmt.Sprint(it.Size))
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, body)
		}
	}}, gitlfs.Protocol(gitlfs.Options{RemoteSources: svc}))
	f.server = httptest.NewServer(router)
	t.Cleanup(f.server.Close)
	return f
}

func (f *remoteFixture) batch(t *testing.T, operation string) batchReply {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"operation": operation, "objects": []any{map[string]any{"oid": f.oid, "size": len(f.object)}}})
	req, _ := http.NewRequest(http.MethodPost, f.server.URL+"/artifactory/api/lfs/lfs/objects/batch", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	res, e := f.server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var reply batchReply
	if e = json.NewDecoder(res.Body).Decode(&reply); e != nil {
		t.Fatal(e)
	}
	if res.StatusCode != 200 {
		t.Fatalf("batch status %d: %+v", res.StatusCode, reply)
	}
	return reply
}

func TestUnitRemoteCacheAndOfflineReads(t *testing.T) {
	f := newRemoteFixture(t)
	first := f.batch(t, "download")
	if len(first.Objects) != 1 || first.Objects[0].Actions["download"].Href == "" {
		t.Fatalf("initial batch: %+v", first)
	}
	res, e := f.server.Client().Get(first.Objects[0].Actions["download"].Href)
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(res.Body)
	res.Body.Close()
	if e != nil || res.StatusCode != 200 || !bytes.Equal(got, f.object) {
		t.Fatalf("first read status=%d body=%q err=%v", res.StatusCode, got, e)
	}
	f.available.Store(false)
	second := f.batch(t, "download")
	if len(second.Objects) != 1 || second.Objects[0].Actions["download"].Href == "" {
		t.Fatalf("offline batch: %+v", second)
	}
	res, e = f.server.Client().Get(second.Objects[0].Actions["download"].Href)
	if e != nil {
		t.Fatal(e)
	}
	got, e = io.ReadAll(res.Body)
	res.Body.Close()
	if e != nil || res.StatusCode != 200 || !bytes.Equal(got, f.object) {
		t.Fatalf("offline read status=%d body=%q err=%v", res.StatusCode, got, e)
	}
	if f.objects.Load() != 1 {
		t.Fatalf("upstream object reads=%d, want 1", f.objects.Load())
	}
}

func TestUnitRemoteRejectsUntrustedActionAndTamperedBytes(t *testing.T) {
	for _, mode := range []string{"action", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			if mode == "action" {
				f.badAction = true
			} else {
				f.badHash = true
			}
			batch := f.batch(t, "download")
			if mode == "action" {
				if batch.Objects[0].Error == nil || batch.Objects[0].Actions["download"].Href != "" {
					t.Fatalf("untrusted action accepted: %+v", batch)
				}
				return
			}
			res, e := f.server.Client().Get(batch.Objects[0].Actions["download"].Href)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusBadGateway {
				t.Fatalf("tampered bytes status=%d", res.StatusCode)
			}
			f.badHash = false
			res, e = f.server.Client().Get(batch.Objects[0].Actions["download"].Href)
			if e != nil {
				t.Fatal(e)
			}
			got, e := io.ReadAll(res.Body)
			res.Body.Close()
			if e != nil || res.StatusCode != http.StatusOK || !bytes.Equal(got, f.object) {
				t.Fatalf("corrected upstream read status=%d body=%q err=%v", res.StatusCode, got, e)
			}
		})
	}
}

func TestUnitRemoteFollowsSameOriginRedirectWithDigestPin(t *testing.T) {
	f := newRemoteFixture(t)
	f.redirect = true
	batch := f.batch(t, "download")
	res, err := f.server.Client().Get(batch.Objects[0].Actions["download"].Href)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || !bytes.Equal(body, f.object) {
		t.Fatalf("redirected read status=%d body=%q err=%v", res.StatusCode, body, err)
	}
}

func TestUnitRemoteAuthenticatedActionHeadersStayOnExactURL(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint("cross-origin-redirect-", redirect), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.actionHeader = map[string]string{
				"Authorization":  "Bearer upstream-action-secret",
				"X-Action-Token": "signed-action",
			}
			f.redirect = redirect
			batch := f.batch(t, "download")
			if batch.Objects[0].Actions["download"].Href == "" {
				t.Fatalf("authenticated batch: %+v", batch)
			}
			res, err := f.server.Client().Get(batch.Objects[0].Actions["download"].Href)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK || !bytes.Equal(body, f.object) ||
				!f.actionAuthSeen.Load() || f.leakedActionAuth.Load() {
				t.Fatalf("action fetch status=%d body=%q auth=%t leaked=%t err=%v",
					res.StatusCode, body, f.actionAuthSeen.Load(), f.leakedActionAuth.Load(), err)
			}
		})
	}
}

func TestUnitRemoteRejectsUnsafeActionHeaders(t *testing.T) {
	cases := map[string]map[string]string{
		"cookie":         {"Cookie": "session=secret"},
		"proxy auth":     {"Proxy-Authorization": "Basic secret"},
		"framing":        {"Content-Length": "0"},
		"hop by hop":     {"Connection": "keep-alive"},
		"newline":        {"X-Token": "safe\r\ninjected: bad"},
		"duplicate case": {"X-Token": "one", "x-token": "two"},
		"value bound":    {"X-Token": strings.Repeat("x", 4097)},
	}
	tooMany := make(map[string]string)
	for i := range 17 {
		tooMany[fmt.Sprintf("X-Token-%d", i)] = "value"
	}
	cases["name count"] = tooMany
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.actionHeader = header
			batch := f.batch(t, "download")
			if batch.Objects[0].Error == nil || batch.Objects[0].Actions["download"].Href != "" ||
				f.objects.Load() != 0 {
				t.Fatalf("unsafe action header admitted: %+v", batch)
			}
		})
	}
}

func TestUnitRemoteRejectsClientUpload(t *testing.T) {
	f := newRemoteFixture(t)
	response := f.batch(t, "upload")
	if len(response.Objects) != 1 || response.Objects[0].Error == nil ||
		response.Objects[0].Error.Code != http.StatusMethodNotAllowed ||
		len(response.Objects[0].Actions) != 0 || f.batches.Load() != 0 {
		t.Fatalf("remote upload response=%+v upstream batches=%d", response, f.batches.Load())
	}
}
