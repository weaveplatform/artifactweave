package nix_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format"
	nixfmt "github.com/weaveplatform/artifactweave/internal/format/nix"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/nix"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	infoPath = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
	prefix   = "/artifactory/api/nix/local/"
)

type fixture struct {
	t          *testing.T
	db         *inmem.Store
	st         store.Store
	items      *item.Service
	c          repo.Config
	h          http.Handler
	info       nixfmt.Info
	data, meta []byte
	public     ed25519.PublicKey
	private    ed25519.PrivateKey
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "local", PackageType: repo.Nix, Class: repo.Local, PrimaryKeyPairRef: "signer"})
	if err = db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile("../../format/nix/testdata/cache/" + infoPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := nixfmt.Parse(meta, infoPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../format/nix/testdata/cache/" + info.URL)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Keys().
		CreateKeypair(t.Context(), types.Keypair{Name: "signer", Type: types.KeypairEd25519, Alias: "cache-1", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})), PublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t:       t,
		db:      db,
		st:      db,
		items:   &item.Service{Store: db, Blobs: blobs},
		c:       c,
		info:    info,
		data:    data,
		meta:    meta,
		public:  public,
		private: private,
	}
	f.mount(nil)
	return f
}

func (f *fixture) mount(remote *source.Service, tune ...func(*pkgroute.Protocol)) {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := auth.Principal{Name: "admin", Admin: true}
			if user := r.Header.Get("X-User"); user != "" {
				p = auth.Principal{Name: user}
			}
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{Permissions: f.db.Permissions(), Groups: f.db.Groups()},
				p,
			)
			if err != nil {
				f.t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	p := nix.Protocol(remote)
	for _, apply := range tune {
		apply(&p)
	}
	pkgroute.Mount(
		router,
		pkgroute.Options{
			Store: f.st,
			Items: func() *item.Service { return f.items },
			ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
				rc, _, err := f.items.Blobs.Open(r.Context(), it.Actual.SHA256)
				if err != nil {
					f.t.Fatal(err)
				}
				defer rc.Close()
				http.ServeContent(w, r, "", it.Modified, rc)
			},
			ItemError: func(_ context.Context, _ string, err error) error {
				var rejected *format.RejectionError
				if errors.As(err, &rejected) {
					return apierror.New(rejected.Status, rejected.Message)
				}
				return err
			},
		},
		p,
	)
	f.h = router
}

func (f *fixture) do(method, path string, body []byte, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if user != "" {
		r.Header.Set("X-User", user)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body)
	}
}

func (f *fixture) publish() {
	f.t.Helper()
	status(f.t, f.do("PUT", prefix+f.info.URL, f.data, ""), 201)
	status(f.t, f.do("PUT", prefix+infoPath, f.meta, ""), 201)
}

func downloadURL(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		if value, ok := strings.CutPrefix(line, "URL: "); ok {
			return value
		}
	}
	return ""
}

func TestAccNixNativePublishSignAndLifecycle(t *testing.T) {
	f := newFixture(t)
	status(t, f.do("GET", prefix+"nix-cache-info", nil, ""), 200)
	status(t, f.do("PUT", prefix+infoPath, f.meta, ""), 409)
	f.publish()
	meta := f.do("GET", prefix+infoPath, nil, "")
	status(t, meta, 200)
	transport := downloadURL(meta.Body.Bytes())
	original := bytes.Replace(meta.Body.Bytes(), []byte(transport), []byte(f.info.URL), 1)
	signed, err := nixfmt.Parse(original, infoPath)
	if err != nil || !signed.Verify("cache-1", f.public) {
		t.Fatalf("invalid signature: %v", err)
	}
	if bytes.Contains(meta.Body.Bytes(), []byte("CA:")) {
		t.Fatal("unverified content address escaped")
	}
	data := f.do("GET", prefix+transport, nil, "")
	status(t, data, 200)
	if !bytes.Equal(data.Body.Bytes(), f.data) {
		t.Fatal("archive changed")
	}
	status(t, f.do("HEAD", prefix+transport, nil, ""), 200)
	status(t, f.do("POST", prefix+"reindex", nil, ""), 200)
	status(t, f.do("DELETE", prefix+f.info.URL, nil, ""), 204)
	status(t, f.do("GET", prefix+infoPath, nil, ""), 404)
	status(t, f.do("PUT", prefix+f.info.URL, f.data, ""), 201)
	status(t, f.do("GET", prefix+infoPath, nil, ""), 200)
}

func TestAccNixDenialsAndMalformedPublication(t *testing.T) {
	f := newFixture(t)
	f.publish()
	for _, path := range []string{"nix-cache-info", infoPath, "nar/" + infoPath[:32] + "/" + strings.TrimPrefix(f.info.URL, "nar/")} {
		status(t, f.do("GET", prefix+path, nil, "denied"), 403)
	}
	for _, tc := range []struct {
		method, path string
		body         []byte
		code         int
	}{
		{"GET", infoPath + "?q=x", nil, 400},
		{"GET", "unknown", nil, 404},
		{"PUT", "unknown", []byte("x"), 400},
		{"PUT", f.info.URL, []byte("bad"), 400},
		{"PUT", infoPath, []byte("bad"), 400},
		{"PUT", "nix-cache-info", []byte("StoreDir: /evil\n"), 400},
		{"POST", "reindex?q=x", nil, 400},
		{"POST", "reindex", nil, 200},
		{"GET", "nar/" + infoPath[:32] + "/" + strings.Repeat("0", 52) + ".nar", nil, 404},
	} {
		t.Run(
			tc.method+tc.path,
			func(t *testing.T) { status(t, f.do(tc.method, prefix+tc.path, tc.body, ""), tc.code) },
		)
	}
	status(t, f.do("PUT", prefix+f.info.URL, f.data, "denied"), 403)
	status(t, f.do("DELETE", prefix+infoPath, nil, "denied"), 403)
	status(t, f.do("POST", prefix+"reindex", nil, "denied"), 403)
}

func TestAccNixFederatedAndOrderedVirtual(t *testing.T) {
	f := newFixture(t)
	f.publish()
	for _, c := range []repo.Config{{Key: "fed", Class: repo.Federated, PackageType: repo.Nix, PrimaryKeyPairRef: "signer"}, {Key: "empty", Class: repo.Local, PackageType: repo.Nix}, {Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix, Repositories: []string{"missing", "empty", "local"}}} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fed := "/artifactory/api/nix/fed/"
	status(t, f.do("PUT", fed+f.info.URL, f.data, ""), 201)
	status(t, f.do("PUT", fed+infoPath, f.meta, ""), 201)
	for _, key := range []string{"fed", "virtual"} {
		base := "/artifactory/api/nix/" + key + "/"
		w := f.do("GET", base+infoPath, nil, "")
		status(t, w, 200)
		status(t, f.do("GET", base+downloadURL(w.Body.Bytes()), nil, ""), 200)
	}
	status(t, f.do("PUT", "/artifactory/api/nix/virtual/"+infoPath, f.meta, ""), 405)
}

func remoteFixture(t *testing.T) (*fixture, *fakeupstream.Server, repo.Config, string) {
	t.Helper()
	f := newFixture(t)
	up := fakeupstream.New(t, fakeupstream.Options{})
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Nix, URL: up.URL, PrimaryKeyPairRef: "signer"})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store:   f.db,
		Blobs:   f.items.Blobs,
		Items:   f.items,
		Pool:    new(remote.Pool),
		Options: resolver.Options,
		Key:     []byte(strings.Repeat("k", 32)),
	}
	t.Cleanup(policy.Pool.Close)
	svc := &source.Service{
		HTTP:  policy,
		Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.db.RemoteCache().Source(ctx, c.Key)
		},
	}
	f.mount(svc)
	info := f.info
	if err := info.Sign("cache-1", f.private); err != nil {
		t.Fatal(err)
	}
	up.Set(
		"/"+infoPath,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"text/x-nix-narinfo"}},
			Body:   info.Encode(info.URL),
		},
	)
	up.Set(
		"/"+info.URL,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/x-nix-nar"}},
			Body:   f.data,
		},
	)
	return f, up, c, "/artifactory/api/nix/remote/"
}

func TestAccNixRemoteIntegrityCacheAndOffline(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	w := f.do("GET", base+infoPath, nil, "")
	status(t, w, 200)
	url := downloadURL(w.Body.Bytes())
	status(t, f.do("GET", base+url, nil, ""), 200)
	before := up.Count("", "")
	status(t, f.do("GET", base+infoPath, nil, ""), 200)
	status(t, f.do("GET", base+url, nil, ""), 200)
	if up.Count("", "") != before {
		t.Fatal("warm cache fetched upstream")
	}
	c.Offline = new(true)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	up.Close()
	status(t, f.do("GET", base+infoPath, nil, ""), 200)
	status(t, f.do("GET", base+url, nil, ""), 200)
	status(t, f.do("PUT", base+infoPath, f.meta, ""), 405)
}

func TestAccNixRemoteRejectsUntrustedMetadataAndBadArchive(t *testing.T) {
	for _, mode := range []string{"metadata", "signature", "archive", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			f, up, _, base := remoteFixture(t)
			switch mode {
			case "metadata":
				up.Set("/"+infoPath, fakeupstream.Resource{Body: []byte("bad")})
			case "signature":
				up.Set("/"+infoPath, fakeupstream.Resource{Body: f.meta})
			case "archive":
				up.Set("/"+f.info.URL, fakeupstream.Resource{Body: []byte("bad")})
			case "redirect":
				up.Set(
					"/"+f.info.URL,
					fakeupstream.Resource{
						Status: 302,
						Header: http.Header{"Location": {"https://invalid.example/archive"}},
					},
				)
			}
			w := f.do("GET", base+infoPath, nil, "")
			if mode == "metadata" || mode == "signature" {
				status(t, w, 502)
				return
			}
			status(t, w, 200)
			status(t, f.do("GET", base+downloadURL(w.Body.Bytes()), nil, ""), 502)
		})
	}
}
