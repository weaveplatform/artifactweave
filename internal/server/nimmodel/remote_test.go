package nimmodel_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
	"github.com/weaveplatform/artifactweave/internal/server/nimmodel"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	base     = "/artifactory/api/nimmodel/nim-remote/"
	metadata = "v2/nim/metadata?resourceId=nim%2Fmeta%2Fllama&tag=1.3"
)

type fixture struct {
	t        *testing.T
	up       *fakeupstream.Server
	clock    *fakeupstream.Clock
	store    *inmem.Store
	h        http.Handler
	router   *chi.Mux
	blobs    blob.Store
	items    *item.Service
	resolver *remote.Resolver
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := fakeupstream.NewClock(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	up := fakeupstream.New(
		t,
		fakeupstream.Options{Now: clock.Now, Auth: fakeupstream.Auth{Token: "ngc-personal-key"}},
	)
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{
		Key: "nim-remote", Class: repo.Remote, PackageType: repo.NIMModel,
		URL: up.URL, Password: "ngc-personal-key",
	})
	ttl := 10
	c.RetrievalCachePeriodSecs = &ttl
	if err := db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(t.Context(), types.Permission{
		Name: "nim-read", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"reader": types.ActionRead, "nim-only": types.ActionRead,
				}},
				Targets: map[string]types.Target{"nim-remote": {Include: []string{"**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs, Now: clock.Now}
	resolver, err := remote.NewResolver(db, nil, remote.ResolverOptions{
		RevisionKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store: db, Blobs: blobs, Items: items, Pool: new(remote.Pool),
		Key: []byte(strings.Repeat("k", 32)), Now: clock.Now,
		Options: resolver.Options,
		PrepareUpload: func(_ context.Context, q source.Request, _ source.Permit,
			_ remote.Options, u *item.CacheUpload,
		) error {
			nimmodel.PrepareUpload(q, u)
			return nil
		},
	}
	t.Cleanup(policy.Pool.Close)
	svc := &source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return db.RemoteCache().Source(ctx, c.Key)
		},
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			p := auth.Principal{Name: name, Admin: name == "admin"}
			if name == "anonymous" {
				p = auth.Anonymous
			}
			p, err := auth.Authorize(r.Context(), authz.Sources{
				Permissions: db.Permissions(), Groups: db.Groups(), AnonymousEnabled: true,
			}, p)
			if err != nil {
				t.Fatal(err)
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	pkgroute.Mount(router, pkgroute.Options{
		Store: db, Items: func() *item.Service { return items },
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
	}, nimmodel.Protocol(svc))
	return &fixture{
		t: t, up: up, clock: clock, store: db, h: router, router: router,
		blobs: blobs, items: items, resolver: resolver,
	}
}

func (f *fixture) get(path, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodGet, base+path, nil)
	r.Header.Set("X-User", user)
	r.Header.Set("Authorization", "Bearer downstream-secret")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) configure(change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.store.Repos().Get(f.t.Context(), "nim-remote")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.store.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func TestNIMMetadataColdWarmOfflineAndRefresh(t *testing.T) {
	f := newFixture(t)
	body := `{"profiles":[{"profile_id":"h100","tested_gpu_devices":[]}],"release_details":{"container_version":"1.3"}}`
	f.up.Set(
		"/v2/nim/metadata",
		fakeupstream.Resource{
			Body:   []byte(body),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	for range 2 {
		rec := f.get(metadata, "reader")
		if rec.Code != http.StatusOK || rec.Body.String() != body {
			t.Fatalf("metadata %d %s", rec.Code, rec.Body)
		}
	}
	if n := f.up.Count("GET", "/v2/nim/metadata"); n != 1 {
		t.Fatalf("warm cache fetched upstream %d times", n)
	}
	it, err := f.store.Items().Get(t.Context(), "nim-remote", true,
		"models/nim/meta/llama/1.3/.jfrog_nim_model_info.json")
	if err != nil {
		t.Fatal("validated NIM metadata was not indexed: ", err)
	}
	props, err := f.store.Items().Properties(t.Context(), it.ID)
	if err != nil || !props.Has("Nimmodel.name", "llama") ||
		!props.Has("Nimmodel.versionId", "1.3") {
		t.Fatalf("NIM search properties: %v, %v", props, err)
	}
	f.configure(func(c *repo.Config) { offline := true; c.Offline = &offline })
	if rec := f.get(metadata, "reader"); rec.Code != http.StatusOK {
		t.Fatalf("offline metadata %d %s", rec.Code, rec.Body)
	}
	if n := f.up.Count("GET", "/v2/nim/metadata"); n != 1 {
		t.Fatalf("offline fetched upstream %d times", n)
	}
	f.configure(func(c *repo.Config) { offline := false; c.Offline = &offline })
	f.clock.Advance(11 * time.Second)
	updated := `{"release_details":{"container_version":"1.3.1"}}`
	f.up.Set(
		"/v2/nim/metadata",
		fakeupstream.Resource{
			Body:   []byte(updated),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	if rec := f.get(metadata, "reader"); rec.Code != http.StatusOK || rec.Body.String() != updated {
		t.Fatalf("refresh metadata %d %s", rec.Code, rec.Body)
	}
	if n := f.up.Count("GET", "/v2/nim/metadata"); n != 2 {
		t.Fatalf("refresh fetched upstream %d times", n)
	}
}

func TestNIMRepositoryDiscoveryIsSeparateFromTagMetadata(t *testing.T) {
	f := newFixture(t)
	repository := `{"nim":{"name":"Llama","supported_models":["llama"]}}`
	f.up.Set("/v2/repos/nim/meta/llama", fakeupstream.Resource{
		Body: []byte(repository), Header: http.Header{"Content-Type": {"application/json"}},
	})
	rec := f.get("v2/repos/nim/meta/llama", "reader")
	if rec.Code != http.StatusOK || rec.Body.String() != repository {
		t.Fatalf("repository discovery %d %s", rec.Code, rec.Body)
	}
	if f.up.Count("GET", "/v2/repos/nim/meta/llama") != 1 ||
		f.up.Count("GET", "/v2/nim/metadata") != 0 {
		t.Fatal("repository discovery reused tag metadata")
	}
	if rec := f.get("v2/repos/nim/meta/llama", "denied"); rec.Code != http.StatusForbidden {
		t.Fatalf("repository discovery denial %d %s", rec.Code, rec.Body)
	}
}

func TestNIMDenialAndInvalidUpstreamDoNotPublish(t *testing.T) {
	f := newFixture(t)
	path := "/v2/nim/metadata"
	f.up.Set(
		path,
		fakeupstream.Resource{
			Body:   []byte(`{"profiles":[{}]}`),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	if rec := f.get(metadata, "reader"); rec.Code != http.StatusBadGateway {
		t.Fatalf("invalid metadata %d %s", rec.Code, rec.Body)
	}
	if _, err := f.store.Items().Get(t.Context(), "nim-remote", true,
		"models/nim/meta/llama/1.3/.jfrog_nim_model_info.json"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("malformed NIM metadata published an item: %v", err)
	}
	if rec := f.get(metadata, "denied"); rec.Code != http.StatusForbidden {
		t.Fatalf("denied metadata %d %s", rec.Code, rec.Body)
	}
	if n := f.up.Count("GET", path); n != 1 {
		t.Fatalf("denied caller reached upstream %d times", n)
	}
	f.up.Set(
		path,
		fakeupstream.Resource{
			Body:   []byte(`{"release_details":{"container_version":"1.3"}}`),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	if rec := f.get(metadata, "reader"); rec.Code != http.StatusOK {
		t.Fatalf("valid retry %d %s", rec.Code, rec.Body)
	}
	if rec := f.get(metadata, "anonymous"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous NIM access %d %s", rec.Code, rec.Body)
	}
	for _, request := range f.up.Requests() {
		if request.HeaderMatches("Authorization", "Bearer downstream-secret") {
			t.Fatal("downstream credential reached NGC")
		}
	}
}

func TestNIMModelFileCacheAndFaults(t *testing.T) {
	f := newFixture(t)
	path := "v2/models/nim/meta/llama/versions/1/files/weights.bin"
	upstream := "/" + path
	f.up.Set(upstream, fakeupstream.Resource{
		Body:  []byte("truncated-model"),
		Fault: fakeupstream.Fault{TruncateAfter: 4},
	})
	if rec := f.get(path, "reader"); rec.Code != http.StatusBadGateway {
		t.Fatalf("truncated model %d %s", rec.Code, rec.Body)
	}
	f.up.Set(upstream, fakeupstream.Resource{
		Body:     []byte("model-content"),
		Redirect: "http://unapproved.invalid/model-content",
	})
	if rec := f.get(path, "reader"); rec.Code != http.StatusBadGateway {
		t.Fatalf("unapproved redirect %d %s", rec.Code, rec.Body)
	}
	f.configure(func(c *repo.Config) { c.Password = "incorrect-key" })
	f.up.Set(upstream, fakeupstream.Resource{Body: []byte("model-content")})
	if rec := f.get(path, "reader"); rec.Code == http.StatusOK {
		t.Fatalf("invalid NGC credentials delivered model %d %s", rec.Code, rec.Body)
	}
	f.configure(func(c *repo.Config) { c.Password = "ngc-personal-key" })
	for range 2 {
		if rec := f.get(
			path,
			"reader",
		); rec.Code != http.StatusOK ||
			rec.Body.String() != "model-content" {
			t.Fatalf("model %d %s", rec.Code, rec.Body)
		}
	}
	if rec := f.get(path, "denied"); rec.Code != http.StatusForbidden {
		t.Fatalf("model denial %d %s", rec.Code, rec.Body)
	}
	before := f.up.Count("GET", upstream)
	f.configure(func(c *repo.Config) { offline := true; c.Offline = &offline })
	if rec := f.get(
		path,
		"reader",
	); rec.Code != http.StatusOK ||
		rec.Body.String() != "model-content" {
		t.Fatalf("offline model %d %s", rec.Code, rec.Body)
	}
	if after := f.up.Count("GET", upstream); after != before {
		t.Fatalf("offline model fetched upstream %d to %d", before, after)
	}
}

func TestNIMMetadataAndPairedDockerRegistryKeepSeparateAuthority(t *testing.T) {
	f := newFixture(t)
	container := `{"release_details":{"container_version":"1.3"}}`
	f.up.Set("/v2/nim/metadata", fakeupstream.Resource{
		Body: []byte(container), Header: http.Header{"Content-Type": {"application/json"}},
	})
	nvcr := fakeupstream.New(t, fakeupstream.Options{
		Auth: fakeupstream.Auth{Username: "$oauthtoken", Password: "nvcr-personal-key"},
	})
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`
	nvcr.Set("/v2/nim/meta/llama/manifests/1.3", fakeupstream.Resource{
		Body: []byte(manifest), Header: http.Header{
			"Content-Type": {"application/vnd.oci.image.index.v1+json"},
		},
	})
	dockerRepo := *repo.Defaults(&repo.Config{
		Key: "nvcr-remote", Class: repo.Remote, PackageType: repo.Docker,
		URL: nvcr.URL, Username: "$oauthtoken", Password: "nvcr-personal-key",
	})
	if err := f.store.Repos().Create(t.Context(), dockerRepo); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Permissions().Create(t.Context(), types.Permission{
		Name: "nvcr-read", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"reader": types.ActionRead, "docker-only": types.ActionRead,
				}},
				Targets: map[string]types.Target{
					"nvcr-remote": {Include: []string{"nim/meta/llama/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	registryPolicy := &remote.Policy{
		Store: f.store, Blobs: f.blobs, Items: f.items,
		Options: f.resolver.Options, Key: []byte(strings.Repeat("k", 32)),
	}
	pool := new(remote.RegistryPool)
	t.Cleanup(pool.Close)
	content := &remote.RegistryContent{
		Policy: registryPolicy, Pool: pool, Options: f.resolver.RegistryOptions,
	}
	content.Install()
	registrySvc := &source.Service{
		HTTP: content, Cache: content,
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return f.store.RemoteCache().Source(ctx, c.Key)
		},
	}
	docker.New(docker.Options{
		Store: f.store, Blobs: f.blobs, Sources: registrySvc,
	}).Register(f.router)
	imagePath := "/artifactory/api/docker/nvcr-remote/v2/nim/meta/llama/manifests/1.3"
	pull := func(user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, imagePath, nil)
		r.Header.Set("X-User", user)
		r.Header.Set("Authorization", "Bearer downstream-secret")
		r.Header.Set("Accept", "application/vnd.oci.image.index.v1+json")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w
	}
	if rec := f.get(
		metadata,
		"reader",
	); rec.Code != http.StatusOK ||
		rec.Body.String() != container {
		t.Fatalf("NIM metadata %d %s", rec.Code, rec.Body)
	}
	if rec := pull("reader"); rec.Code != http.StatusOK || rec.Body.String() != manifest ||
		rec.Header().Get("Docker-Content-Digest") == "" {
		t.Fatalf("paired registry manifest %d %s", rec.Code, rec.Body)
	}
	if rec := pull("nim-only"); rec.Code != http.StatusForbidden {
		t.Fatalf("NIM grant opened Docker %d %s", rec.Code, rec.Body)
	}
	if rec := f.get(metadata, "docker-only"); rec.Code != http.StatusForbidden {
		t.Fatalf("Docker grant opened NIM %d %s", rec.Code, rec.Body)
	}
	for _, request := range f.up.Requests() {
		if !request.HeaderMatches("Authorization", "Bearer ngc-personal-key") ||
			request.HeaderMatches("Authorization", "Basic ") {
			t.Fatal("NIM request used wrong upstream credentials")
		}
	}
	for _, request := range nvcr.Requests() {
		if !request.Authorized ||
			request.HeaderMatches("Authorization", "Bearer ngc-personal-key") ||
			request.HeaderMatches("Authorization", "Bearer downstream-secret") {
			t.Fatal("Docker request used NIM or caller credentials")
		}
	}
	if f.up.Count("GET", "/v2/nim/metadata") != 1 ||
		nvcr.Count("GET", "/v2/nim/meta/llama/manifests/1.3") != 1 {
		t.Fatal("matching NIM and Docker resources were not fetched independently")
	}
}
