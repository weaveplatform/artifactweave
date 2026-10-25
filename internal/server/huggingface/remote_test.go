package huggingface_test

import (
	"context"
	"crypto/sha1"
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
