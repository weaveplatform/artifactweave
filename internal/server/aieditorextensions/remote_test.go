package aieditorextensions_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/weaveplatform/artifactweave/internal/server/aieditorextensions"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const route = "/artifactory/api/aieditorextensions/editor-remote/"

type fixture struct {
	t             *testing.T
	app           http.Handler
	upstream      *httptest.Server
	queries       atomic.Int32
	assets        atomic.Int32
	manifest      []byte
	vsix          []byte
	signature     []byte
	wrongIdentity bool
	badChecksum   bool
	badSignature  bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t}
	f.manifest = []byte(
		`{"publisher":"ms-vscode","name":"hexeditor","version":"1.11.1","engines":{"vscode":"^1.89.0"}}`,
	)
	f.vsix = zipFiles(
		t,
		map[string][]byte{
			"extension/package.json": f.manifest,
			"extension/README.md":    []byte("test"),
		},
	)
	checksum := sha256.Sum256(f.vsix)
	statement, _ := json.Marshal(
		map[string]any{
			"package": map[string]any{
				"size": len(f.vsix),
				"digests": map[string]string{
					"sha256": base64.StdEncoding.EncodeToString(checksum[:]),
				},
			},
		},
	)
	f.signature = zipFiles(
		t,
		map[string][]byte{".signature.manifest": statement, ".signature.p7s": []byte("signature")},
	)
	f.upstream = httptest.NewServer(http.HandlerFunc(f.serveUpstream))
	t.Cleanup(f.upstream.Close)
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{Key: "editor-remote", Class: repo.Remote, PackageType: repo.AIEditorExtensions, URL: f.upstream.URL + "/_apis/public/gallery"})
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().
		Create(t.Context(), types.Permission{Name: "editor-read", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead, auth.AnonymousUser: types.ActionRead}}, Targets: map[string]types.Target{"editor-remote": {Include: []string{"extensions/ms-vscode/hexeditor/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	resolver, err := remote.NewResolver(
		db,
		nil,
		remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store:   db,
		Blobs:   blobs,
		Items:   items,
		Pool:    new(remote.Pool),
		Key:     []byte(strings.Repeat("k", 32)),
		Options: resolver.Options,
	}
	t.Cleanup(policy.Pool.Close)
	svc := &source.Service{
		HTTP:  policy,
		Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return db.RemoteCache().Source(ctx, c.Key)
		},
	}
	router := chi.NewRouter()
	router.Use(baseurl.Middleware(""))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p := auth.Principal{Name: name}
			if name == "anonymous" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(
				r.Context(),
				authz.Sources{
					Permissions:      db.Permissions(),
					Groups:           db.Groups(),
					AnonymousEnabled: true,
				},
				p,
			)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(
		router,
		pkgroute.Options{
			Store: db,
			Items: func() *item.Service { return items },
			ServeItem: func(w http.ResponseWriter, r *http.Request, _ repo.Config, it types.Item) {
				body, _, err := blobs.Open(r.Context(), it.Actual.SHA256)
				if err != nil {
					t.Fatal(err)
				}
				defer body.Close()
				if r.Method == http.MethodGet {
					_, _ = io.Copy(w, body)
				}
			},
		},
		aieditorextensions.Protocol(svc),
	)
	f.app = router
	return f
}

func zipFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *fixture) serveUpstream(w http.ResponseWriter, r *http.Request) {
	const prefix = "/_apis/public/gallery/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == prefix+"extensionquery" {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("Accept") != "application/json;api-version=3.0-preview.1" ||
			r.Header.Get("Authorization") != "" {
			http.Error(w, "bad gallery request", 400)
			return
		}
		f.queries.Add(1)
		var q struct {
			Filters []struct {
				Criteria []struct {
					FilterType int    `json:"filterType"`
					Value      string `json:"value"`
				} `json:"criteria"`
			} `json:"filters"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "invalid query", 400)
			return
		}
		value := ""
		for _, c := range q.Filters[0].Criteria {
			if c.FilterType == 7 {
				value = c.Value
			}
		}
		if value != "ms-vscode.hexeditor" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = io.WriteString(w, `{"results":[{"extensions":[]}]}`)
			return
		}
		identity := "hexeditor"
		if f.wrongIdentity {
			identity = "other"
		}
		sha := sha256.Sum256(f.vsix)
		hash := hex.EncodeToString(sha[:])
		if f.badChecksum {
			hash = strings.Repeat("0", 64)
		}
		assetBase := f.upstream.URL + prefix + "extensions/ms-vscode/" + identity + "/1.11.1/123"
		v := map[string]any{
			"version":          "1.11.1",
			"assetUri":         assetBase,
			"fallbackAssetUri": assetBase,
			"files": []map[string]string{
				{
					"assetType": "Microsoft.VisualStudio.Code.Manifest",
					"source":    assetBase + "/Microsoft.VisualStudio.Code.Manifest",
				},
				{
					"assetType": "Microsoft.VisualStudio.Services.VSIXPackage",
					"source":    assetBase + "/Microsoft.VisualStudio.Services.VSIXPackage",
				},
				{
					"assetType": "Microsoft.VisualStudio.Services.VsixSignature",
					"source":    assetBase + "/Microsoft.VisualStudio.Services.VsixSignature",
				},
			},
			"properties": []map[string]string{
				{"key": "Microsoft.VisualStudio.Code.Engine", "value": "^1.89.0"},
				{"key": "Microsoft.VisualStudio.Services.VsixSha256", "value": hash},
			},
		}
		body, _ := json.Marshal(
			map[string]any{
				"results": []any{
					map[string]any{
						"extensions": []any{
							map[string]any{
								"publisher":     map[string]string{"publisherName": "ms-vscode"},
								"extensionName": "hexeditor",
								"extensionId":   "cc7d2112-5178-4472-8e0e-25dced95e7f0",
								"versions":      []any{v},
							},
						},
					},
				},
			},
		)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
		return
	}
	if !strings.Contains(r.URL.Path, "/extensions/ms-vscode/hexeditor/1.11.1/123/") {
		http.NotFound(w, r)
		return
	}
	f.assets.Add(1)
	switch {
	case strings.HasSuffix(r.URL.Path, "/Microsoft.VisualStudio.Code.Manifest"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.manifest)
	case strings.HasSuffix(r.URL.Path, "/Microsoft.VisualStudio.Services.VSIXPackage"):
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(f.vsix)
	case strings.HasSuffix(r.URL.Path, "/Microsoft.VisualStudio.Services.VsixSignature"):
		w.Header().Set("Content-Type", "application/zip")
		if f.badSignature {
			_, _ = w.Write(
				zipFiles(
					f.t,
					map[string][]byte{
						".signature.manifest": []byte(
							`{"package":{"size":1,"digests":{"sha256":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}}`,
						),
						".signature.p7s": []byte("signature"),
					},
				),
			)
		} else {
			_, _ = w.Write(f.signature)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fixture) request(method, path, user string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, route+path, bytes.NewReader(body))
	r.Header.Set("X-User", user)
	r.Header.Set("Authorization", "Bearer caller-secret")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	return w
}

func TestAccGalleryQueryAndInstallAssetsRespectAnonymousPermissions(t *testing.T) {
	f := newFixture(t)
	q := []byte(
		`{"filters":[{"criteria":[{"filterType":7,"value":"ms-vscode.hexeditor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"},{"filterType":12,"value":"4096"}],"pageNumber":1,"pageSize":1,"sortBy":0,"sortOrder":0}],"assetTypes":[],"flags":950}`,
	)
	response := f.request(http.MethodPost, "_apis/public/gallery/extensionquery", "anonymous", q)
	if response.Code != 200 ||
		!strings.Contains(
			response.Body.String(),
			"/artifactory/api/aieditorextensions/editor-remote/assets/ms-vscode/hexeditor/1.11.1/",
		) {
		t.Fatalf(
			"gallery %d %s, upstream queries=%d",
			response.Code,
			response.Body,
			f.queries.Load(),
		)
	}
	for _, tc := range []struct {
		name     string
		expected []byte
	}{{"Microsoft.VisualStudio.Code.Manifest", f.manifest}, {"Microsoft.VisualStudio.Services.VSIXPackage", f.vsix}, {"Microsoft.VisualStudio.Services.VsixSignature", f.signature}} {
		r := f.request(
			http.MethodGet,
			"assets/ms-vscode/hexeditor/1.11.1/"+tc.name,
			"anonymous",
			nil,
		)
		if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), tc.expected) {
			t.Fatalf("%s: %d %s", tc.name, r.Code, r.Body)
		}
	}
	if f.queries.Load() != 1 {
		t.Fatalf("query cache missed: %d upstream queries", f.queries.Load())
	}
	for _, path := range []string{"_apis/public/gallery/extensionquery", "assets/ms-vscode/hexeditor/1.11.1/Microsoft.VisualStudio.Services.VSIXPackage"} {
		method := http.MethodGet
		var body []byte
		if strings.HasPrefix(path, "_apis") {
			method = http.MethodPost
			body = q
		}
		r := f.request(method, path, "denied", body)
		if r.Code == 200 {
			t.Fatalf("denied caller received %s", path)
		}
	}
}

func TestAccInvalidCatalogCannotPublishAssets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture)
	}{{"identity", func(f *fixture) { f.wrongIdentity = true }}, {"checksum", func(f *fixture) { f.badChecksum = true }}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			r := f.request(
				http.MethodGet,
				"assets/ms-vscode/hexeditor/1.11.1/Microsoft.VisualStudio.Services.VSIXPackage",
				"reader",
				nil,
			)
			if r.Code == 200 {
				t.Fatal("invalid upstream metadata yielded install asset")
			}
		})
	}
}

func TestAccMismatchedSignatureCannotPublish(t *testing.T) {
	f := newFixture(t)
	f.badSignature = true
	r := f.request(
		http.MethodGet,
		"assets/ms-vscode/hexeditor/1.11.1/Microsoft.VisualStudio.Services.VsixSignature",
		"reader",
		nil,
	)
	if r.Code == 200 {
		t.Fatal("signature for another package was served")
	}
}

func TestAccGalleryRejectsMalformedClientPathsAndSeparatesQueryCache(t *testing.T) {
	f := newFixture(t)
	exact := []byte(
		`{"filters":[{"criteria":[{"filterType":7,"value":"ms-vscode.hexeditor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"}],"pageNumber":1,"pageSize":1}],"assetTypes":[],"flags":950}`,
	)
	search := []byte(
		`{"filters":[{"criteria":[{"filterType":10,"value":"hex editor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"}],"pageNumber":1,"pageSize":1}],"assetTypes":[],"flags":950}`,
	)
	for _, body := range [][]byte{exact, search, exact} {
		if r := f.request(
			http.MethodPost,
			"_apis/public/gallery/extensionquery",
			"reader",
			body,
		); r.Code != 200 {
			t.Fatalf("query: %d %s", r.Code, r.Body)
		}
	}
	if f.queries.Load() != 2 {
		t.Fatalf("different queries shared cache or warm query refetched: %d", f.queries.Load())
	}
	for _, tc := range []struct {
		method, path string
		body         []byte
		expected     int
	}{
		{http.MethodPost, "_apis/public/gallery/extensionquery?token=x", exact, 400},
		{http.MethodPost, "_apis/public/gallery/extensionquery", []byte(`{`), 400},
		{http.MethodGet, "assets/ms-vscode/hexeditor/1.11.1/Microsoft.VisualStudio.Services.VSIXPackage?token=x", nil, 400},
		{http.MethodGet, "assets/ms-vscode/hexeditor/1.11.1/unknown", nil, 400},
		{http.MethodGet, "assets/ms-vscode/hexeditor/1.11.2/Microsoft.VisualStudio.Services.VSIXPackage", nil, 404},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := f.request(tc.method, tc.path, "reader", tc.body)
			if r.Code != tc.expected {
				t.Fatalf("%d %s, want %d", r.Code, r.Body, tc.expected)
			}
		})
	}
}
