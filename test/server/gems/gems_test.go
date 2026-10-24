package gems_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // The compact index uses MD5 as an info cache validator.
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	gemfmt "github.com/weaveplatform/artifactweave/internal/format/gems"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type world struct {
	t   *testing.T
	db  *inmem.Store
	h   http.Handler
	gem []byte
}

func newWorld(t *testing.T) *world {
	return newWorldWith(t, nil)
}

func newWorldWith(t *testing.T, configure func(*server.Config)) *world {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	if err := db.Users().
		Create(t.Context(), types.User{Name: "reader", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().
		Create(t.Context(), types.Permission{Name: "gem-reader", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: map[string]types.Target{"gems-local": {Include: []string{"**"}}, "gems-fed": {Include: []string{"**"}}, "gems-virtual": {Include: []string{"**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{{Key: "gems-local", Class: repo.Local, PackageType: repo.Gems}, {Key: "gems-fed", Class: repo.Federated, PackageType: repo.Gems}, {Key: "gems-virtual", Class: repo.Virtual, PackageType: repo.Gems, Repositories: []string{"gems-local", "gems-fed"}, DefaultDeploymentRepo: "gems-local"}} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: support.TestHash})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(
		filepath.Join(
			"..",
			"..",
			"..",
			"internal",
			"format",
			"gems",
			"testdata",
			"rake-12.3.1.gem",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{Store: db, Blobs: blobs, Auth: a, BaseURL: "https://reg.example"}
	if configure != nil {
		configure(&cfg)
	}
	return &world{t: t, db: db, h: server.New(cfg), gem: b}
}

func (w *world) do(
	method, path, user string,
	body []byte,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if user != "" {
		r.SetBasicAuth(user, "pw")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	return rec
}

func status(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("got %d want %d: %s", rec.Code, want, rec.Body)
	}
}

func TestAccNativeLocalFederatedVirtual(t *testing.T) {
	w := newWorld(t)
	for _, key := range []string{"gems-local", "gems-fed"} {
		status(
			t,
			w.do(http.MethodPut, "/artifactory/"+key+"/gems/rake-12.3.1.gem", "admin", w.gem),
			http.StatusCreated,
		)
	}
	for _, key := range []string{"gems-local", "gems-fed", "gems-virtual"} {
		base := "/artifactory/api/gems/" + key
		info := w.do(http.MethodGet, base+"/info/rake", "reader", nil)
		status(t, info, http.StatusOK)
		if !strings.Contains(info.Body.String(), "12.3.1 |checksum:cf37840d") {
			t.Fatalf("info: %s", info.Body)
		}
		versions := w.do(http.MethodGet, base+"/versions", "reader", nil)
		status(t, versions, http.StatusOK)
		md := md5.Sum(info.Body.Bytes())
		if !strings.Contains(versions.Body.String(), hex.EncodeToString(md[:])) {
			t.Fatalf("version MD5: %s", versions.Body)
		}
		status(t, w.do(http.MethodGet, base+"/specs.4.8.gz", "reader", nil), http.StatusOK)
		status(
			t,
			w.do(http.MethodGet, base+"/quick/Marshal.4.8/rake-12.3.1.gemspec.rz", "reader", nil),
			http.StatusOK,
		)
		versionsJSON := w.do(http.MethodGet, base+"/api/v1/versions/rake", "reader", nil)
		status(t, versionsJSON, http.StatusOK)
		if !strings.Contains(versionsJSON.Body.String(), `"number":"12.3.1"`) {
			t.Fatalf("version list: %s", versionsJSON.Body)
		}
		search := w.do(http.MethodGet, base+"/api/v1/search.json?query=rake", "reader", nil)
		status(t, search, http.StatusOK)
		if !strings.Contains(search.Body.String(), `"name":"rake"`) {
			t.Fatalf("search: %s", search.Body)
		}
		got := w.do(http.MethodGet, base+"/gems/rake-12.3.1.gem", "reader", nil)
		status(t, got, http.StatusOK)
		if !bytes.Equal(got.Body.Bytes(), w.gem) {
			t.Fatal("gem bytes changed")
		}
	}
	status(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/gems/gems-local/info/rake",
			"reader",
			nil,
			"If-None-Match",
			w.do(http.MethodGet, "/artifactory/api/gems/gems-local/info/rake", "reader", nil).
				Header().
				Get("ETag"),
		),
		http.StatusNotModified,
	)
	status(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/gems/gems-local/info/rake",
			"reader",
			nil,
			"Range",
			"bytes=0-3",
		),
		http.StatusPartialContent,
	)
	status(
		t,
		w.do(http.MethodPut, "/artifactory/gems-local/gems/rake-12.3.2.gem", "admin", w.gem),
		http.StatusBadRequest,
	)
	status(
		t,
		w.do(http.MethodPost, "/artifactory/api/gems/gems-local/reindex", "admin", nil),
		http.StatusOK,
	)
	status(
		t,
		w.do(http.MethodDelete, "/artifactory/gems-local/gems/rake-12.3.1.gem", "admin", nil),
		http.StatusNoContent,
	)
	status(
		t,
		w.do(http.MethodGet, "/artifactory/api/gems/gems-local/info/rake", "reader", nil),
		http.StatusOK,
	)
	if got := w.do(
		http.MethodGet,
		"/artifactory/api/gems/gems-local/info/rake",
		"reader",
		nil,
	); strings.Contains(
		got.Body.String(),
		"12.3.1",
	) {
		t.Fatalf("deleted gem still indexed: %s", got.Body)
	}
	status(
		t,
		w.do(
			http.MethodPost,
			"/artifactory/api/trash/restore/gems-local/gems/rake-12.3.1.gem?to=gems-local",
			"admin",
			nil,
		),
		http.StatusAccepted,
	)
	if got := w.do(
		http.MethodGet,
		"/artifactory/api/gems/gems-local/info/rake",
		"reader",
		nil,
	); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), "12.3.1") {
		t.Fatalf("restored gem missing from index: %d %s", got.Code, got.Body)
	}
	status(
		t,
		w.do(http.MethodPost, "/artifactory/api/gems/gems-local/reindex", "admin", nil),
		http.StatusOK,
	)
}

func TestAccNativePushAndPermissions(t *testing.T) {
	w := newWorld(t)
	// A native route wins over handwritten stubs, while an unrelated
	// unimplemented handwritten operation keeps its documented 501.
	status(
		t,
		w.do(http.MethodGet, "/artifactory/api/gems/gems-local/names", "admin", nil),
		http.StatusOK,
	)
	status(
		t,
		w.do(http.MethodGet, "/artifactory/api/system/configuration", "admin", nil),
		http.StatusNotImplemented,
	)
	status(
		t,
		w.do(http.MethodPost, "/artifactory/api/gems/gems-local/api/v1/gems", "reader", w.gem),
		http.StatusForbidden,
	)
	status(
		t,
		w.do(http.MethodPost, "/artifactory/api/gems/gems-local/api/v1/gems", "admin", w.gem),
		http.StatusOK,
	)
	if _, err := w.db.Items().
		Get(context.Background(), "gems-local", false, "gems/rake-12.3.1.gem"); err != nil {
		t.Fatal(err)
	}
	status(
		t,
		w.do(http.MethodPost, "/artifactory/api/gems/gems-local/api/v1/gems", "admin", w.gem),
		http.StatusConflict,
	)
	status(
		t,
		w.do(
			http.MethodPost,
			"/artifactory/api/gems/gems-local/api/v1/gems",
			"admin",
			[]byte("invalid gem"),
		),
		http.StatusBadRequest,
	)
	status(
		t,
		w.do(
			http.MethodDelete,
			"/artifactory/api/gems/gems-local/api/v1/yank",
			"reader",
			[]byte("gem_name=rake&version=12.3.1"),
			"Content-Type",
			"application/x-www-form-urlencoded",
		),
		http.StatusForbidden,
	)
	status(
		t,
		w.do(
			http.MethodDelete,
			"/artifactory/api/gems/gems-local/api/v1/yank",
			"admin",
			[]byte("gem_name=rake&version=12.3.1"),
			"Content-Type",
			"application/x-www-form-urlencoded",
		),
		http.StatusOK,
	)
	status(
		t,
		w.do(http.MethodGet, "/artifactory/api/gems/gems-local/gems/rake-12.3.1.gem", "admin", nil),
		http.StatusNotFound,
	)
}

func TestAccNativeRejectsInvalidProtocolRequests(t *testing.T) {
	w := newWorld(t)
	base := "/artifactory/api/gems/gems-local/"
	for _, path := range []string{
		"", "unknown", "info/../rake", "info/invalid..name", "quick/Marshal.4.8/missing-1.0.0.gemspec.rz",
		"api/v1/gems/missing.json", "api/v1/versions/missing", "api/v1/search.json",
		"api/v1/search.json?query=", "api/v1/search.json?query=" + strings.Repeat("a", 129),
		"versions?unexpected=value", "gems/rake-12.3.1.gem?unexpected=value", "gems/bad..name-1.0.gem",
	} {
		got := w.do(http.MethodGet, base+path, "admin", nil)
		if got.Code < 400 {
			t.Errorf("accepted invalid request %s: %d %s", path, got.Code, got.Body)
		}
	}
	for _, input := range []string{"%ZZ", strings.Repeat("x", 4097), "gem_name=../../secret&version=1.0", "gem_name=rake&version=bad..version", "gem_name=rake&version=12.3.1"} {
		got := w.do(http.MethodDelete, base+"api/v1/yank", "admin", []byte(input))
		if got.Code < 400 {
			t.Errorf(
				"accepted invalid yank %q: %d %s",
				input[:min(len(input), 100)],
				got.Code,
				got.Body,
			)
		}
	}
	for _, repoKey := range []string{"gems-virtual"} {
		status(
			t,
			w.do(http.MethodPost, "/artifactory/api/gems/"+repoKey+"/api/v1/gems", "admin", w.gem),
			http.StatusMethodNotAllowed,
		)
		status(
			t,
			w.do(http.MethodDelete, "/artifactory/api/gems/"+repoKey+"/api/v1/yank", "admin", nil),
			http.StatusMethodNotAllowed,
		)
	}
	status(t, w.do(http.MethodGet, base+"versions", "", nil), http.StatusUnauthorized)
}

func TestAccNativeYankQueryForm(t *testing.T) {
	w := newWorld(t)
	base := "/artifactory/api/gems/gems-local/"
	status(t, w.do(http.MethodPost, base+"api/v1/gems", "admin", w.gem), http.StatusOK)
	status(
		t,
		w.do(http.MethodDelete, base+"api/v1/yank?gem_name=rake&version=12.3.1", "admin", nil),
		http.StatusOK,
	)
	status(t, w.do(http.MethodGet, base+"gems/rake-12.3.1.gem", "admin", nil), http.StatusNotFound)
}

func TestAccNativeRawRubyGemsTokenAuth(t *testing.T) {
	var tokens *token.Service
	w := newWorldWith(t, func(cfg *server.Config) {
		if err := cfg.Store.Instance().Init(t.Context(), types.InstanceIdentity{
			InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
		var err error
		tokens, err = token.New(t.Context(), cfg.Store, token.Options{KeyBits: 1024})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Tokens = tokens
		cfg.Auth, err = auth.NewAuthenticator(
			cfg.Store.Users(),
			cfg.Store.Settings(),
			auth.Options{Hash: support.TestHash, Tokens: tokens},
		)
		if err != nil {
			t.Fatal(err)
		}
	})
	issued, err := tokens.Issue(
		t.Context(),
		token.Request{Username: "admin", IncludeReference: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{issued.AccessToken, issued.ReferenceToken} {
		status(
			t,
			w.do(
				http.MethodGet,
				"/artifactory/api/gems/gems-local/names",
				"",
				nil,
				"Authorization",
				raw,
			),
			http.StatusOK,
		)
	}
	status(
		t,
		w.do(
			http.MethodPost,
			"/artifactory/api/gems/gems-local/api/v1/gems",
			"",
			w.gem,
			"Authorization",
			issued.AccessToken,
		),
		http.StatusOK,
	)
	status(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/gems/gems-local/versions",
			"",
			nil,
			"Authorization",
			"invalid.token.value",
		),
		http.StatusUnauthorized,
	)
}

func TestAccVirtualFiltersUnreadableMember(t *testing.T) {
	w := newWorld(t)
	status(
		t,
		w.do(http.MethodPut, "/artifactory/gems-local/gems/rake-12.3.1.gem", "admin", w.gem),
		http.StatusCreated,
	)
	if err := w.db.Users().
		Create(t.Context(), types.User{Name: "limited", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().
		Create(t.Context(), types.Permission{Name: "virtual-only", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"limited": types.ActionRead}}, Targets: map[string]types.Target{"gems-virtual": {Include: []string{"**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	versions := w.do(http.MethodGet, "/artifactory/api/gems/gems-virtual/versions", "limited", nil)
	status(t, versions, http.StatusOK)
	if strings.Contains(versions.Body.String(), "rake") {
		t.Fatalf("member data leaked into virtual index: %s", versions.Body)
	}
	status(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/gems/gems-virtual/gems/rake-12.3.1.gem",
			"limited",
			nil,
		),
		http.StatusNotFound,
	)
}

func TestAccVirtualRejectsMemberCycleAndSkipsRemovedMember(t *testing.T) {
	w := newWorld(t)
	c, err := w.db.Repos().Get(t.Context(), "gems-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"gems-local", "gems-virtual"}
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"versions", "gems/rake-12.3.1.gem"} {
		got := w.do(http.MethodGet, "/artifactory/api/gems/gems-virtual/"+path, "admin", nil)
		if got.Code < 400 {
			t.Errorf("member cycle accepted through %s: %d %s", path, got.Code, got.Body)
		}
	}
	c.Repositories = []string{"removed-member", "gems-local"}
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	status(
		t,
		w.do(http.MethodGet, "/artifactory/api/gems/gems-virtual/versions", "admin", nil),
		http.StatusOK,
	)
}

func TestAccLocalIndexFailsClosedOnMissingArchiveBlob(t *testing.T) {
	w := newWorld(t)
	if _, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "gems-local", Path: "gems", Name: "rake-12.3.1.gem",
		Actual: types.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 123,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"versions", "info/rake", "specs.4.8.gz"} {
		got := w.do(http.MethodGet, "/artifactory/api/gems/gems-local/"+path, "admin", nil)
		if got.Code < 400 {
			t.Errorf("indexed gem with missing blob through %s: %d %s", path, got.Code, got.Body)
		}
	}
}

func TestAccRemoteRejectsMalformedMetadataAndArchive(t *testing.T) {
	for _, broken := range []string{"metadata", "archive", "unlisted version"} {
		t.Run(broken, func(t *testing.T) {
			up := fakeupstream.New(t, fakeupstream.Options{})
			w := newRemoteWorld(t, up)
			p, err := gemfmt.Read(
				t.Context(),
				bytes.NewReader(w.gem),
				int64(len(w.gem)),
				"gems/rake-12.3.1.gem",
			)
			if err != nil {
				t.Fatal(err)
			}
			packages := []gemfmt.Package{p}
			info := gemfmt.CompactInfo("rake", packages)
			if broken == "metadata" {
				info = []byte("not compact metadata\n")
			}
			versions := gemfmt.CompactVersions(packages)
			if broken == "unlisted version" {
				sum := md5.Sum(info)
				versions = []byte(
					"created_at: 1970-01-01T00:00:00Z\n---\nrake 1.0.0 " + hex.EncodeToString(
						sum[:],
					) + "\n",
				)
			}
			up.Set("/versions", fakeupstream.Resource{Body: versions})
			up.Set("/info/rake", fakeupstream.Resource{Body: info})
			archive := bytes.Clone(w.gem)
			if broken == "archive" {
				archive[len(archive)/2] ^= 1
			}
			up.Set("/gems/rake-12.3.1.gem", fakeupstream.Resource{Body: archive})
			got := w.do(
				http.MethodGet,
				"/artifactory/api/gems/gems-remote/gems/rake-12.3.1.gem",
				"admin",
				nil,
			)
			if got.Code < 400 {
				t.Fatalf("accepted broken %s: %d", broken, got.Code)
			}
			if _, err := w.db.Items().
				Get(t.Context(), "gems-remote", false, "gems/rake-12.3.1.gem"); err == nil {
				t.Fatal("invalid archive cached")
			}
		})
	}
}

func TestAccRemoteRejectsIndexDrift(t *testing.T) {
	for _, broken := range []string{"invalid versions", "invalid info", "wrong info digest", "missing info"} {
		t.Run(broken, func(t *testing.T) {
			up := fakeupstream.New(t, fakeupstream.Options{})
			w := newRemoteWorld(t, up)
			p, err := gemfmt.Read(
				t.Context(),
				bytes.NewReader(w.gem),
				int64(len(w.gem)),
				"gems/rake-12.3.1.gem",
			)
			if err != nil {
				t.Fatal(err)
			}
			packages := []gemfmt.Package{p}
			versions := gemfmt.CompactVersions(packages)
			info := gemfmt.CompactInfo("rake", packages)
			switch broken {
			case "invalid versions":
				versions = []byte("rake 12.3.1\n")
			case "invalid info":
				info = []byte("---\n12.3.1 |checksum:bad\n")
			case "wrong info digest":
				info = []byte("---\n")
			case "missing info":
				info = nil
			}
			up.Set("/versions", fakeupstream.Resource{Body: versions})
			if info != nil {
				up.Set("/info/rake", fakeupstream.Resource{Body: info})
			}
			for _, path := range []string{"names", "versions", "info/rake", "api/v1/search.json?query=rake", "gems/rake-12.3.1.gem"} {
				got := w.do(http.MethodGet, "/artifactory/api/gems/gems-remote/"+path, "admin", nil)
				if got.Code < 400 {
					t.Errorf("accepted %s through %s: %d %s", broken, path, got.Code, got.Body)
				}
			}
		})
	}
}

func newRemoteWorld(t *testing.T, up *fakeupstream.Server) *world {
	t.Helper()
	return newWorldWith(t, func(cfg *server.Config) {
		c := repo.Config{
			Key:         "gems-remote",
			Class:       repo.Remote,
			PackageType: repo.Gems,
			URL:         up.URL,
		}
		if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		v := repo.Config{
			Key:          "gems-remote-virtual",
			Class:        repo.Virtual,
			PackageType:  repo.Gems,
			Repositories: []string{"gems-remote"},
		}
		if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&v)); err != nil {
			t.Fatal(err)
		}
		resolver, err := remote.NewResolver(
			cfg.Store,
			nil,
			remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store:   cfg.Store,
			Blobs:   cfg.Blobs,
			Items:   &item.Service{Store: cfg.Store, Blobs: cfg.Blobs},
			Pool:    new(remote.Pool),
			Options: resolver.Options,
			Key:     []byte(strings.Repeat("k", 32)),
		}
		t.Cleanup(policy.Pool.Close)
		cfg.RemoteSources = &source.Service{
			HTTP:  policy,
			Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return cfg.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
}

func TestAccRemoteCacheOfflineAndRedirect(t *testing.T) {
	for _, redirect := range []string{"none", "same-origin", "cross-origin"} {
		t.Run(redirect, func(t *testing.T) {
			up := fakeupstream.New(t, fakeupstream.Options{})
			w := newRemoteWorld(t, up)
			p, err := gemfmt.Read(
				t.Context(),
				bytes.NewReader(w.gem),
				int64(len(w.gem)),
				"gems/rake-12.3.1.gem",
			)
			if err != nil {
				t.Fatal(err)
			}
			packages := []gemfmt.Package{p}
			up.Set("/versions", fakeupstream.Resource{Body: gemfmt.CompactVersions(packages)})
			up.Set("/info/rake", fakeupstream.Resource{Body: gemfmt.CompactInfo("rake", packages)})
			up.Set(
				"/api/v1/gems/rake.json",
				fakeupstream.Resource{Body: []byte(`{"name":"rake","version":"12.3.1"}`)},
			)
			archive := fakeupstream.Resource{Body: w.gem}
			switch redirect {
			case "same-origin":
				archive = fakeupstream.Resource{Redirect: up.URL + "/redirected.gem"}
				up.Set("/redirected.gem", fakeupstream.Resource{Body: w.gem})
			case "cross-origin":
				archive = fakeupstream.Resource{Redirect: "https://example.invalid/redirected.gem"}
			}
			up.Set("/gems/rake-12.3.1.gem", archive)
			path := "/artifactory/api/gems/gems-remote/gems/rake-12.3.1.gem"
			got := w.do(http.MethodGet, path, "admin", nil)
			if redirect == "cross-origin" {
				if got.Code < 400 {
					t.Fatalf("accepted cross-origin redirect: %d", got.Code)
				}
				return
			}
			status(t, got, http.StatusOK)
			status(
				t,
				w.do(
					http.MethodGet,
					"/artifactory/api/gems/gems-remote-virtual/versions",
					"admin",
					nil,
				),
				http.StatusOK,
			)
			status(
				t,
				w.do(
					http.MethodGet,
					"/artifactory/api/gems/gems-remote-virtual/gems/rake-12.3.1.gem",
					"admin",
					nil,
				),
				http.StatusOK,
			)
			status(
				t,
				w.do(
					http.MethodGet,
					"/artifactory/api/gems/gems-remote/api/v1/gems/rake.json",
					"admin",
					nil,
				),
				http.StatusOK,
			)
			c, err := w.db.Repos().Get(t.Context(), "gems-remote")
			if err != nil {
				t.Fatal(err)
			}
			c.Offline = new(bool)
			*c.Offline = true
			if err := w.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			up.Close()
			status(t, w.do(http.MethodGet, path, "admin", nil), http.StatusOK)
		})
	}
}

func TestAccRemoteMetadataFiltersDeniedGem(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	w := newRemoteWorld(t, up)
	p, err := gemfmt.Read(
		t.Context(),
		bytes.NewReader(w.gem),
		int64(len(w.gem)),
		"gems/rake-12.3.1.gem",
	)
	if err != nil {
		t.Fatal(err)
	}
	packages := []gemfmt.Package{p}
	up.Set("/versions", fakeupstream.Resource{Body: gemfmt.CompactVersions(packages)})
	up.Set("/info/rake", fakeupstream.Resource{Body: gemfmt.CompactInfo("rake", packages)})
	up.Set("/gems/rake-12.3.1.gem", fakeupstream.Resource{Body: w.gem})
	if err := w.db.Users().
		Create(t.Context(), types.User{Name: "remote-limited", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().
		Create(t.Context(), types.Permission{Name: "remote-limited", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"remote-limited": types.ActionRead}}, Targets: map[string]types.Target{"gems-remote": {Include: []string{"**"}, Exclude: []string{"gems/rake-12.3.1.gem"}}}}}}); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/gems/gems-remote/"
	for _, path := range []string{"names", "versions", "info/rake", "api/v1/search.json?query=rake"} {
		got := w.do(http.MethodGet, base+path, "remote-limited", nil)
		status(t, got, http.StatusOK)
		if strings.Contains(got.Body.String(), "rake") ||
			strings.Contains(got.Body.String(), p.SHA256) {
			t.Fatalf("denied gem leaked through %s: %s", path, got.Body)
		}
	}
	for _, path := range []string{"quick/Marshal.4.8/rake-12.3.1.gemspec.rz", "api/v1/gems/rake.json", "api/v1/versions/rake", "gems/rake-12.3.1.gem"} {
		got := w.do(http.MethodGet, base+path, "remote-limited", nil)
		if got.Code < 400 {
			t.Fatalf("denied gem leaked through %s: %d", path, got.Code)
		}
	}
	want, err := gemfmt.Specs(nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	got := w.do(http.MethodGet, base+"specs.4.8.gz", "remote-limited", nil)
	status(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), want) {
		t.Fatal("denied gem leaked into Marshal specs")
	}
}

func TestAccRubyClientsInstallPinnedRake(t *testing.T) {
	if _, err := exec.LookPath("gem"); err != nil {
		t.Skip("gem client unavailable")
	}
	if _, err := exec.LookPath("bundle"); err != nil {
		t.Skip("Bundler client unavailable")
	}
	w := newWorld(t)
	status(
		t,
		w.do(http.MethodPut, "/artifactory/gems-local/gems/rake-12.3.1.gem", "admin", w.gem),
		http.StatusCreated,
	)
	var infoRequests atomic.Int64
	upstream := httptest.NewServer(
		http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/info/rake") {
				infoRequests.Add(1)
			}
			w.h.ServeHTTP(out, req)
		}),
	)
	defer upstream.Close()
	source := strings.Replace(
		upstream.URL,
		"http://",
		"http://admin:pw@",
		1,
	) + "/artifactory/api/gems/gems-local/"
	dir := t.TempDir()
	env := append(
		os.Environ(),
		"GEM_HOME="+filepath.Join(dir, "gems"),
		"GEM_PATH="+filepath.Join(dir, "gems"),
		"BUNDLE_PATH="+filepath.Join(dir, "bundle"),
		"BUNDLE_USER_CACHE="+filepath.Join(dir, "cache"),
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"http_proxy=",
		"https_proxy=",
	)
	gem := exec.Command(
		"gem",
		"install",
		"rake",
		"-v",
		"12.3.1",
		"--source",
		source,
		"--clear-sources",
		"--install-dir",
		filepath.Join(dir, "gems"),
		"--no-document",
	)
	gem.Env = env
	if out, err := gem.CombinedOutput(); err != nil {
		t.Fatalf("gem install: %v\n%s", err, out)
	}
	gemfile := "source '" + source + "'\ngem 'rake', '12.3.1'\n"
	if err := os.WriteFile(filepath.Join(dir, "Gemfile"), []byte(gemfile), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := exec.Command("bundle", "install")
	bundle.Dir = dir
	bundle.Env = append(
		os.Environ(),
		"HOME="+dir,
		"GEM_HOME="+filepath.Join(dir, "empty-gems"),
		"GEM_PATH="+filepath.Join(dir, "empty-gems"),
		"BUNDLE_PATH="+filepath.Join(
			dir,
			"bundle",
		),
		"BUNDLE_USER_CACHE="+filepath.Join(dir, "cache"),
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"http_proxy=",
		"https_proxy=",
	)
	if out, err := bundle.CombinedOutput(); err != nil {
		t.Fatalf("bundle install: %v\n%s", err, out)
	} else {
		t.Logf("Bundler: %s", out)
	}
	lock, err := os.ReadFile(filepath.Join(dir, "Gemfile.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lock, []byte("rake (12.3.1)")) {
		t.Fatalf("Bundler did not resolve pinned rake: %s", lock)
	}
	if infoRequests.Load() == 0 {
		t.Fatal("Bundler did not consume the compact info route")
	}
}
