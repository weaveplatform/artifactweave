package huggingface_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitRemoteCacheAndOffline(t *testing.T) {
	content := []byte(`{"model_type":"bert"}`)
	git := sha1.New()
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(content))
	_, _ = git.Write(content)
	blobID := hex.EncodeToString(git.Sum(nil))
	var calls atomic.Int64
	var offline atomic.Bool
	var refreshed atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if offline.Load() {
			http.Error(w, "upstream offline", 502)
			return
		}
		switch r.URL.Path {
		case "/api/models/acme/demo/revision/main":
			if r.URL.RawQuery != "blobs=true" {
				http.Error(w, "query", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			revision := sha
			if refreshed.Load() {
				revision = strings.Repeat("a", 40)
			}
			_ = json.NewEncoder(w).
				Encode(map[string]any{"id": "acme/demo", "sha": revision, "siblings": []map[string]any{{"rfilename": "config.json", "size": len(content), "blobId": blobID}}})
		case "/acme/demo/resolve/main/config.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
	prefix := "api/models/acme/demo/revision/main"
	r := f.request("GET", prefix, "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("remote info %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/main/config.json", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || string(b) != string(content) {
		t.Fatalf("remote resolve %d %s", r.StatusCode, b)
	}
	first := calls.Load()
	offline.Store(true)
	r = f.request("GET", prefix, "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("cached info %d %s", r.StatusCode, b)
	}
	r = f.request("GET", "acme/demo/resolve/main/config.json", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || string(b) != string(content) {
		t.Fatalf("cached file %d %s", r.StatusCode, b)
	}
	if calls.Load() != first {
		t.Fatalf("fresh cache still called upstream: %d -> %d", first, calls.Load())
	}
	offline.Store(false)
	refreshed.Store(true)
	c, err := f.db.Repos().Get(t.Context(), "hf")
	if err != nil {
		t.Fatal(err)
	}
	noCache := 0
	c.RetrievalCachePeriodSecs = &noCache
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	r = f.request("GET", prefix, "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || !strings.Contains(string(b), strings.Repeat("a", 40)) {
		t.Fatalf("refreshed revision %d %s", r.StatusCode, b)
	}
}

func remoteSetup(t *testing.T, upstream string, ttl int) func(*fixture) *source.Service {
	return func(f *fixture) *source.Service {
		c, e := f.db.Repos().Get(t.Context(), "hf")
		if e != nil {
			t.Fatal(e)
		}
		c.URL = upstream
		c.RetrievalCachePeriodSecs = &ttl
		if e := f.db.Repos().Update(t.Context(), c); e != nil {
			t.Fatal(e)
		}
		resolver, e := remote.NewResolver(
			f.db,
			nil,
			remote.ResolverOptions{RevisionKey: []byte(strings.Repeat("k", 32))},
		)
		if e != nil {
			t.Fatal(e)
		}
		policy := &remote.Policy{
			Store:   f.db,
			Blobs:   f.items.Blobs,
			Items:   f.items,
			Pool:    new(remote.Pool),
			Options: resolver.Options,
			Key:     []byte(strings.Repeat("k", 32)),
			Now:     time.Now,
		}
		t.Cleanup(policy.Pool.Close)
		return &source.Service{
			HTTP:  policy,
			Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return f.db.RemoteCache().Source(ctx, c.Key)
			},
		}
	}
}

func TestUnitRemoteRedirectAndIntegrity(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		name := "same-origin"
		if corrupt {
			name = "corrupt"
		}
		t.Run(name, func(t *testing.T) {
			good := []byte(`{"model_type":"bert"}`)
			git := sha1.New()
			_, _ = fmt.Fprintf(git, "blob %d\x00", len(good))
			_, _ = git.Write(good)
			blobID := hex.EncodeToString(git.Sum(nil))
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/models/acme/demo/revision/main":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).
						Encode(map[string]any{"id": "acme/demo", "sha": sha, "siblings": []map[string]any{{"rfilename": "config.json", "size": len(good), "blobId": blobID}}})
				case "/acme/demo/resolve/main/config.json":
					http.Redirect(w, r, "/cdn/config.json", http.StatusFound)
				case "/cdn/config.json":
					if corrupt {
						_, _ = w.Write([]byte(`{"model_type":"evil"}`))
					} else {
						_, _ = w.Write(good)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer up.Close()
			f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
			r := f.request("GET", "acme/demo/resolve/main/config.json", "", nil)
			b := readResponse(t, r)
			if corrupt {
				if r.StatusCode == 200 {
					t.Fatalf("corrupt redirect published: %s", b)
				}
				_, e := f.db.Items().
					Get(t.Context(), "hf", true, "models--acme--demo/snapshots/"+sha+"/config.json")
				if !errors.Is(e, store.ErrNotFound) {
					t.Fatalf("corrupt asset cached: %v", e)
				}
			} else if r.StatusCode != 200 || string(b) != string(good) {
				t.Fatalf("same-origin redirect %d %s", r.StatusCode, b)
			}
		})
	}
}

func TestUnitRemoteRejectsCrossOriginRedirect(t *testing.T) {
	var stolen atomic.Int64
	evil := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { stolen.Add(1); _, _ = w.Write([]byte("secret")) },
		),
	)
	defer evil.Close()
	good := []byte(`{"model_type":"bert"}`)
	git := sha1.New()
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(good))
	_, _ = git.Write(good)
	blobID := hex.EncodeToString(git.Sum(nil))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/acme/demo/revision/main":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).
				Encode(map[string]any{"id": "acme/demo", "sha": sha, "siblings": []map[string]any{{"rfilename": "config.json", "size": len(good), "blobId": blobID}}})
		case "/acme/demo/resolve/main/config.json":
			http.Redirect(w, r, evil.URL+"/stolen", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
	r := f.request("GET", "acme/demo/resolve/main/config.json", "", nil)
	_ = readResponse(t, r)
	if r.StatusCode == 200 || stolen.Load() != 0 {
		t.Fatalf(
			"cross-origin redirect reached target: status %d calls %d",
			r.StatusCode,
			stolen.Load(),
		)
	}
}

type sourceBackendFunc func(context.Context, source.Request, source.Permit) (source.Result, error)

func (fn sourceBackendFunc) Read(
	ctx context.Context,
	request source.Request,
	permit source.Permit,
) (source.Result, error) {
	return fn(ctx, request, permit)
}

type failingSourceBody struct{}

func (failingSourceBody) Read([]byte) (int, error) { return 0, errors.New("source body failed") }
func (failingSourceBody) Close() error             { return nil }

func TestUnitRemoteInfoReadsValidatedStoredCacheItem(t *testing.T) {
	var upstreamCalls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "unexpected upstream request: "+r.URL.Path, 502)
	}))
	defer up.Close()
	var sources *source.Service
	f := newFixture(t, repo.Remote, func(f *fixture) *source.Service {
		sources = remoteSetup(t, up.URL, 3600)(f)
		return sources
	})
	c, err := f.db.Repos().Get(t.Context(), "hf")
	if err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(
		`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"config.json","size":2,"blobId":"%s"}]}`,
		sha,
		"0123456789012345678901234567890123456789",
	)
	stored, err := f.items.Deploy(t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Cached: true, Path: "models--acme--demo/api/main.json"},
		Body:   bytes.NewBufferString(metadata), By: "source",
	})
	if err != nil {
		t.Fatalf("persist source metadata: %v", err)
	}
	sources.HTTP = sourceBackendFunc(
		func(_ context.Context, request source.Request, _ source.Permit) (source.Result, error) {
			if request.Resource.Path != stored.FullPath() {
				t.Fatalf("unexpected cache path %q", request.Resource.Path)
			}
			return source.Result{Item: &stored}, nil
		},
	)
	r := f.request(http.MethodGet, "api/models/acme/demo/revision/main", "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"rfilename":"config.json"`) ||
		upstreamCalls.Load() != 0 {
		t.Fatalf(
			"stored metadata projection: %d %s calls=%d",
			r.StatusCode,
			b,
			upstreamCalls.Load(),
		)
	}
	// A source revision that changes after retrieval must fence both metadata
	// and tree disclosure, even if its cached bytes were once valid.
	for _, failAfter := range []int{1, 2} {
		sources.HTTP = sourceBackendFunc(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				checks := 0
				return source.Result{Item: &stored, Revalidate: func(context.Context) error {
					checks++
					if checks > failAfter {
						return source.ErrSuperseded
					}
					return nil
				}}, nil
			},
		)
		for _, path := range []string{"api/models/acme/demo/revision/main", "api/models/acme/demo/tree/main"} {
			r = f.request(http.MethodGet, path, "", nil)
			b = readResponse(t, r)
			if r.StatusCode == 200 || strings.Contains(string(b), "config.json") {
				t.Fatalf("superseded source disclosed %q after %d checks: %d %s",
					path, failAfter, r.StatusCode, b)
			}
		}
	}
	broken := stored
	broken.Actual.SHA256 = strings.Repeat("f", 64)
	sources.HTTP = sourceBackendFunc(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{Item: &broken}, nil
		},
	)
	r = f.request(http.MethodGet, "api/models/acme/demo/revision/main", "", nil)
	b = readResponse(t, r)
	if r.StatusCode == 200 || upstreamCalls.Load() != 0 {
		t.Fatalf("missing cached blob disclosed as valid metadata: %d %s", r.StatusCode, b)
	}
	invalid, err := f.items.Deploy(t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Cached: true, Path: "models--acme--demo/api/main.json"},
		Body:   strings.NewReader(`{"id":"other/model","sha":"bad","siblings":[]}`), By: "source",
	})
	if err != nil {
		t.Fatal(err)
	}
	sources.HTTP = sourceBackendFunc(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{Item: &invalid}, nil
		},
	)
	r = f.request(http.MethodGet, "api/models/acme/demo/revision/main", "", nil)
	b = readResponse(t, r)
	if r.StatusCode == 200 || strings.Contains(string(b), "other/model") {
		t.Fatalf("invalid cached metadata disclosed: %d %s", r.StatusCode, b)
	}
	sources.HTTP = sourceBackendFunc(
		func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return source.Result{Stream: &source.Stream{
				Body: failingSourceBody{}, Status: http.StatusOK,
				Representation: source.Representation{Size: 1},
			}}, nil
		},
	)
	r = f.request(http.MethodGet, "api/models/acme/demo/revision/main", "", nil)
	b = readResponse(t, r)
	if r.StatusCode == 200 || strings.Contains(string(b), "source body failed") {
		t.Fatalf("failed source body disclosed: %d %s", r.StatusCode, b)
	}
}

func TestUnitRemoteMetadataFailuresNeverPublishAnIdentity(t *testing.T) {
	for _, mode := range []string{"invalid URL", "redirected metadata", "wrong model", "upstream unavailable"} {
		t.Run(mode, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirected metadata" {
					http.Redirect(w, r, "/other-model", http.StatusFound)
					return
				}
				if mode == "upstream unavailable" {
					http.Error(w, "temporary failure", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(
					w,
					`{"id":"wrong/model","sha":"%s","siblings":[{"rfilename":"config.json","size":2,"blobId":"%s"}]}`,
					sha,
					"0123456789012345678901234567890123456789",
				)
			}))
			defer up.Close()
			f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
			if mode == "invalid URL" {
				c, err := f.db.Repos().Get(t.Context(), "hf")
				if err != nil {
					t.Fatal(err)
				}
				c.URL = "://invalid"
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			r := f.request(http.MethodGet, "api/models/acme/demo/revision/main", "", nil)
			b := readResponse(t, r)
			if r.StatusCode == 200 || strings.Contains(string(b), "wrong/model") {
				t.Fatalf("invalid upstream published metadata: %d %s", r.StatusCode, b)
			}
			if _, err := f.db.Items().Get(t.Context(), "hf", true,
				"models--acme--demo/api/main.json"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("invalid metadata cached: %v", err)
			}
		})
	}
}

func TestUnitRemoteMetadataDoesNotListFilesOutsideReadGrant(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"config.json","size":2,"blobId":"%s"}]}`,
			sha,
			"0123456789012345678901234567890123456789",
		)
	}))
	defer up.Close()
	f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "metadata-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{
				Users: map[string]types.Actions{"metadata-only": types.ActionRead},
			},
			Targets: map[string]types.Target{
				"hf": {Include: []string{"models--acme--demo/api/**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	r := f.requestAs(http.MethodGet, "api/models/acme/demo/revision/main", "", nil, "metadata-only")
	b := readResponse(t, r)
	if r.StatusCode != http.StatusNotFound || strings.Contains(string(b), "config.json") {
		t.Fatalf("file outside grant disclosed: %d %s", r.StatusCode, b)
	}
}

func TestUnitRemoteRepositoryRejectsNativePublish(t *testing.T) {
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
	r := f.request(http.MethodPost, "api/models/acme/demo/preupload/main", "application/json",
		[]byte(`{"files":[{"path":"config.json","sample":"e30=","size":2}]}`))
	b := readResponse(t, r)
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("remote model write admitted: %d %s", r.StatusCode, b)
	}
}

func TestUnitRemoteDatasetUsesDatasetIdentityAndResolvePath(t *testing.T) {
	content := []byte(`{"dataset":"small"}`)
	git := sha1.New()
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(content))
	_, _ = git.Write(content)
	blobID := hex.EncodeToString(git.Sum(nil))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/datasets/acme/demo/revision/main":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(
				w,
				`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"config.json","size":%d,"blobId":"%s"}]}`,
				sha,
				len(content),
				blobID,
			)
		case "/datasets/acme/demo/resolve/main/config.json":
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
	r := f.request(http.MethodGet, "datasets/acme/demo/resolve/main/config.json", "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Equal(b, content) {
		t.Fatalf("remote dataset path: %d %s", r.StatusCode, b)
	}
	if _, err := f.db.Items().Get(t.Context(), "hf", true,
		"models--acme--demo/snapshots/"+sha+"/config.json"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dataset materialized under model identity: %v", err)
	}
}

func TestUnitRemoteLFSRequiresDeclaredSizeAndDigest(t *testing.T) {
	content := bytes.Repeat([]byte("M"), 1<<20+1)
	digest := sha256.Sum256(content)
	oid := hex.EncodeToString(digest[:])
	for _, short := range []bool{false, true} {
		name := "valid"
		if short {
			name = "short asset"
		}
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/models/acme/demo/revision/main":
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(
						w,
						`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"weights.bin","size":%d,"lfs":{"sha256":"%s","size":%d,"pointerSize":130}}]}`,
						sha,
						len(content),
						oid,
						len(content),
					)
				case "/acme/demo/resolve/main/weights.bin":
					if short {
						_, _ = w.Write(content[:len(content)-1])
					} else {
						_, _ = w.Write(content)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer up.Close()
			f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
			r := f.request(http.MethodGet, "acme/demo/resolve/main/weights.bin", "", nil)
			b := readResponse(t, r)
			if short && r.StatusCode == 200 ||
				!short && (r.StatusCode != 200 || !bytes.Equal(b, content)) {
				t.Fatalf(
					"remote LFS integrity: short=%t status=%d bytes=%d",
					short,
					r.StatusCode,
					len(b),
				)
			}
		})
	}
}

func TestUnitRemoteRedirectCannotIntroduceURLCredentials(t *testing.T) {
	content := []byte(`{"model_type":"bert"}`)
	git := sha1.New()
	_, _ = fmt.Fprintf(git, "blob %d\x00", len(content))
	_, _ = git.Write(content)
	blobID := hex.EncodeToString(git.Sum(nil))
	var redirected atomic.Bool
	var up *httptest.Server
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/acme/demo/revision/main":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(
				w,
				`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"config.json","size":%d,"blobId":"%s"}]}`,
				sha,
				len(content),
				blobID,
			)
		case "/acme/demo/resolve/main/config.json":
			http.Redirect(
				w,
				r,
				strings.Replace(up.URL, "://", "://hidden@", 1)+"/cdn/config.json",
				http.StatusFound,
			)
		case "/cdn/config.json":
			redirected.Store(true)
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, repo.Remote, remoteSetup(t, up.URL, 3600))
	r := f.request(http.MethodGet, "acme/demo/resolve/main/config.json", "", nil)
	b := readResponse(t, r)
	if r.StatusCode == 200 || redirected.Load() {
		t.Fatalf("credential-bearing redirect followed: %d %s", r.StatusCode, b)
	}
}
