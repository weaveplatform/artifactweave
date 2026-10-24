package cargo_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccRemoteSparseCacheOfflineRedirectAndIntegrity(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	clock := fakeupstream.NewClock(time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	f := newFixture(t, repo.Remote, func(items *item.Service) *source.Service {
		c, err := items.Store.Repos().Get(t.Context(), "cargo-test")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/cargo/"
		ttl := 10
		c.RetrievalCachePeriodSecs = &ttl
		if err := items.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := remote.NewResolver(
			items.Store,
			nil,
			remote.ResolverOptions{RevisionKey: key},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store:   items.Store,
			Blobs:   items.Blobs,
			Items:   items,
			Pool:    new(remote.Pool),
			Options: resolver.Options,
			Key:     key,
			Now:     clock.Now,
		}
		t.Cleanup(policy.Pool.Close)
		return &source.Service{
			HTTP:  policy,
			Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return items.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
	base := "/artifactory/api/cargo/cargo-test/"
	crate := archive(t, "hello_cargo", "1.0.0")
	sum := sha256.Sum256(crate)
	rec := map[string]any{
		"name":     "hello_cargo",
		"vers":     "1.0.0",
		"deps":     []any{},
		"cksum":    hex.EncodeToString(sum[:]),
		"features": map[string]any{},
		"yanked":   false,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	up.Set(
		"/cargo/config.json",
		fakeupstream.Resource{Body: []byte(`{"dl":"` + up.URL + `/cargo/crates"}`)},
	)
	up.Set("/cargo/he/ll/hello_cargo", fakeupstream.Resource{Body: append(line, '\n')})
	up.Set(
		"/cargo/crates/hello_cargo/1.0.0/download",
		fakeupstream.Resource{Redirect: "/cargo/mirror/hello_cargo-1.0.0.crate"},
	)
	up.Set("/cargo/mirror/hello_cargo-1.0.0.crate", fakeupstream.Resource{Body: crate})
	for _, path := range []string{"index/config.json", "index/he/ll/hello_cargo", "api/v1/crates/hello_cargo/1.0.0/download"} {
		got := f.do(http.MethodGet, base+path, "admin", nil)
		if got.Code != 200 {
			t.Fatalf("cold %s: %d %s", path, got.Code, got.Body.String())
		}
	}
	if got := up.Count(http.MethodGet, "/cargo/mirror/hello_cargo-1.0.0.crate"); got != 1 {
		t.Fatalf("redirected fetch count %d", got)
	}
	c, err := f.db.Repos().Get(t.Context(), "cargo-test")
	if err != nil {
		t.Fatal(err)
	}
	offline := true
	c.Offline = &offline
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	up.Delete("/cargo/config.json")
	up.Delete("/cargo/he/ll/hello_cargo")
	up.Delete("/cargo/mirror/hello_cargo-1.0.0.crate")
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo/1.0.0/download",
		"admin",
		nil,
	); got.Code != 200 ||
		string(got.Body.Bytes()) != string(crate) {
		t.Fatalf("offline download: %d %s", got.Code, got.Body.String())
	}
}

func TestAccRemoteRejectsCorruptIndexAndCrate(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixture(t, repo.Remote, func(items *item.Service) *source.Service {
		c, err := items.Store.Repos().Get(t.Context(), "cargo-test")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/cargo/"
		if err := items.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := remote.NewResolver(
			items.Store,
			nil,
			remote.ResolverOptions{RevisionKey: key},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store:   items.Store,
			Blobs:   items.Blobs,
			Items:   items,
			Pool:    new(remote.Pool),
			Options: resolver.Options,
			Key:     key,
		}
		t.Cleanup(policy.Pool.Close)
		return &source.Service{
			HTTP:  policy,
			Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return items.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
	base := "/artifactory/api/cargo/cargo-test/"
	up.Set(
		"/cargo/config.json",
		fakeupstream.Resource{Body: []byte(`{"dl":"` + up.URL + `/cargo/crates"}`)},
	)
	up.Set(
		"/cargo/he/ll/hello_cargo",
		fakeupstream.Resource{
			Body: []byte(
				`{"name":"wrong","vers":"1.0.0","deps":[],"features":{},"cksum":"` + strings.Repeat(
					"a",
					64,
				) + `"}` + "\n",
			),
		},
	)
	if got := f.do(
		http.MethodGet,
		base+"index/he/ll/hello_cargo",
		"admin",
		nil,
	); got.Code != http.StatusBadGateway {
		t.Fatalf("invalid index: %d %s", got.Code, got.Body.String())
	}
	if _, err := f.db.Items().
		Get(t.Context(), "cargo-test", true, "index/he/ll/hello_cargo"); err == nil {
		t.Fatal("invalid index entered cache")
	}
	crate := archive(t, "hello_cargo", "1.0.0")
	sum := sha256.Sum256(crate)
	up.Set(
		"/cargo/he/ll/hello_cargo",
		fakeupstream.Resource{
			Body: []byte(
				`{"name":"hello_cargo","vers":"1.0.0","deps":[],"features":{},"cksum":"` + hex.EncodeToString(
					sum[:],
				) + `"}` + "\n",
			),
		},
	)
	up.Set(
		"/cargo/crates/hello_cargo/1.0.0/download",
		fakeupstream.Resource{Body: []byte("tampered")},
	)
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo/1.0.0/download",
		"admin",
		nil,
	); got.Code != http.StatusBadGateway {
		t.Fatalf("invalid crate: %d %s", got.Code, got.Body.String())
	}
	if _, err := f.db.Items().
		Get(t.Context(), "cargo-test", true, "crates/hello_cargo/hello_cargo-1.0.0.crate"); err == nil {
		t.Fatal("invalid crate entered cache")
	}
}

func TestAccRemoteTemplateAndSparseValidation(t *testing.T) {
	up := fakeupstream.New(t, fakeupstream.Options{})
	f := newFixture(t, repo.Remote, func(items *item.Service) *source.Service {
		c, err := items.Store.Repos().Get(t.Context(), "cargo-test")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/cargo/"
		if err := items.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := remote.NewResolver(
			items.Store,
			nil,
			remote.ResolverOptions{RevisionKey: key},
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := &remote.Policy{
			Store:   items.Store,
			Blobs:   items.Blobs,
			Items:   items,
			Pool:    new(remote.Pool),
			Options: resolver.Options,
			Key:     key,
		}
		t.Cleanup(policy.Pool.Close)
		return &source.Service{
			HTTP:  policy,
			Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
				return items.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
	base := "/artifactory/api/cargo/cargo-test/"
	for _, raw := range []string{"", `{}`, `{"dl":"file:///etc/passwd"}`, `{"dl":"http://user:pass@up.example/crates"}`} {
		up.Set("/cargo/config.json", fakeupstream.Resource{Body: []byte(raw)})
		if got := f.do(
			http.MethodGet,
			base+"index/config.json",
			"admin",
			nil,
		); got.Code != http.StatusBadGateway {
			t.Fatalf("invalid sparse config %s: %d %s", raw, got.Code, got.Body.String())
		}
		if got := f.do(
			http.MethodGet,
			base+"api/v1/crates/hello_cargo/1.0.0/download",
			"admin",
			nil,
		); got.Code != http.StatusBadGateway {
			t.Fatalf(
				"download with invalid sparse config %s: %d %s",
				raw,
				got.Code,
				got.Body.String(),
			)
		}
	}
	config := `{"dl":"` + up.URL + `/cargo/crates/{crate}/{version}"}`
	up.Set("/cargo/config.json", fakeupstream.Resource{Body: []byte(config)})
	crate := archive(t, "hello_cargo", "1.0.0")
	sum := sha256.Sum256(crate)
	record := `{"name":"hello_cargo","vers":"1.0.0","deps":[],"features":{},"cksum":"` + hex.EncodeToString(
		sum[:],
	) + `","yanked":false}`
	for _, raw := range []string{record, record + "\n" + record + "\n"} {
		up.Set("/cargo/he/ll/hello_cargo", fakeupstream.Resource{Body: []byte(raw)})
		if got := f.do(
			http.MethodGet,
			base+"index/he/ll/hello_cargo",
			"admin",
			nil,
		); got.Code != http.StatusBadGateway {
			t.Fatalf("invalid index %q: %d %s", raw, got.Code, got.Body.String())
		}
		if got := f.do(
			http.MethodGet,
			base+"api/v1/crates/hello_cargo/1.0.0/download",
			"admin",
			nil,
		); got.Code != http.StatusBadGateway {
			t.Fatalf("download with invalid index %q: %d %s", raw, got.Code, got.Body.String())
		}
	}
	up.Set("/cargo/he/ll/hello_cargo", fakeupstream.Resource{Body: []byte(record + "\n")})
	up.Set("/cargo/crates/hello_cargo/1.0.0", fakeupstream.Resource{Body: crate})
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo/1.0.0/download",
		"admin",
		nil,
	); got.Code != 200 ||
		!bytes.Equal(got.Body.Bytes(), crate) {
		t.Fatalf("templated download: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo/2.0.0/download",
		"admin",
		nil,
	); got.Code != 404 {
		t.Fatalf("missing remote version: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo/invalid/download",
		"admin",
		nil,
	); got.Code != 404 {
		t.Fatalf("invalid remote version: %d %s", got.Code, got.Body.String())
	}
	if got := f.do(
		http.MethodGet,
		base+"api/v1/crates/hello_cargo/1.0.0/download",
		"reader",
		nil,
	); got.Code != 403 {
		t.Fatalf("denied remote crate: %d %s", got.Code, got.Body.String())
	}
}
