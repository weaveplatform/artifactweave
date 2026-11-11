package cargohttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// wantRefusal checks the download policy's 404 in Artifactory's envelope,
// with none of the success answer's headers.
func wantRefusal(t *testing.T, label string, got *httptest.ResponseRecorder, message string) {
	t.Helper()
	want := `{"errors":[{"message":"` + message + `","status":404}]}` + "\n"
	if got.Code != http.StatusNotFound || got.Body.String() != want ||
		got.Header().Get("Cache-Control") != "" || got.Header().Get("ETag") != "" {
		t.Errorf("%s: %d %q (Cache-Control %q, ETag %q), want 404 %q", label, got.Code,
			got.Body, got.Header().Get("Cache-Control"), got.Header().Get("ETag"), want)
	}
}

func wantOK(t *testing.T, label string, got *httptest.ResponseRecorder, contains ...string) {
	t.Helper()
	if got.Code != http.StatusOK {
		t.Errorf("%s: %d %s, want 200", label, got.Code, got.Body)
		return
	}
	for _, s := range contains {
		if !strings.Contains(got.Body.String(), s) {
			t.Errorf("%s: %s, want it to contain %s", label, got.Body, s)
		}
	}
}

func blackedOut(path string) string {
	return "The repository 'cargo-test' is blacked out and cannot serve artifact 'cargo-test:" +
		path + "'."
}

func rejected(path string) string {
	return "The repository 'cargo-test' rejected the artifact 'cargo-test:" + path +
		"' due to its include/exclude pattern settings."
}

func (f *fixture) patch(change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(context.Background(), "cargo-test")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(context.Background(), c); err != nil {
		f.t.Fatal(err)
	}
}

const (
	policyBase   = "/artifactory/api/cargo/cargo-test/"
	policyIndex  = "index/he/ll/hello_cargo"
	policyCrate  = "crates/hello_cargo/hello_cargo-1.0.0.crate"
	policyFirst  = "api/v1/crates/hello_cargo/1.0.0/download"
	policySecond = "api/v1/crates/hello_cargo/1.1.0/download"
)

// TestAccDownloadPolicyOfAHostedRepository: a blacked-out local or
// federated repository refuses config.json and a crate's index file at the
// paths 7.x keeps them at, and its versions API, with the blackout's 404
// after read permission; a pattern on those paths refuses them, and a
// pattern on one crate leaves its version listed (#1237).
func TestAccDownloadPolicyOfAHostedRepository(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			for _, v := range []string{"1.0.0", "1.1.0"} {
				body := publishBody(t, "hello_cargo", v, archive(t, "hello_cargo", v))
				if got := f.do(http.MethodPut, policyBase+"api/v1/crates/new", "admin", body); got.Code != 200 {
					t.Fatalf("publish %s: %d %s", v, got.Code, got.Body)
				}
			}
			get := func(p, user string) *httptest.ResponseRecorder {
				return f.do(http.MethodGet, policyBase+p, user, nil)
			}
			blackout := true
			f.patch(func(c *repo.Config) { c.BlackedOut = &blackout })
			wantRefusal(t, "blacked out config", get("index/config.json", "admin"), blackedOut("config.json"))
			wantRefusal(t, "blacked out index", get(policyIndex, "admin"), blackedOut(policyIndex))
			wantRefusal(t, "blacked out versions", get("api/v1/crates/hello_cargo", "admin"),
				blackedOut("api/v1/crates/hello_cargo"))
			wantRefusal(t, "blacked out crate", get(policyFirst, "admin"), blackedOut(policyCrate))
			if got := f.do(http.MethodHead, policyBase+policyIndex, "admin", nil); got.Code != 404 ||
				got.Header().Get("Cache-Control") != "" {
				t.Errorf("blacked out index HEAD: %d %v", got.Code, got.Header())
			}
			// Read permission is checked first.
			if got := get(policyIndex, "nobody"); got.Code != http.StatusForbidden {
				t.Errorf("blacked out index, no permission: %d %s", got.Code, got.Body)
			}
			want := `{"errors":[{"detail":"Cargo crate read denied"}]}` + "\n"
			if got := get("api/v1/crates/hello_cargo", "nobody"); got.Code != 403 || got.Body.String() != want {
				t.Errorf("blacked out versions, no permission: %d %s", got.Code, got.Body)
			}
			blackout = false
			excluded := policyCrate
			f.patch(func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &blackout, &excluded })
			wantOK(t, "excluded crate's index", get(policyIndex, "admin"), `"vers":"1.0.0"`, `"vers":"1.1.0"`)
			wantOK(t, "excluded crate's versions", get("api/v1/crates/hello_cargo", "admin"),
				`"vers":"1.0.0"`, `"vers":"1.1.0"`)
			wantRefusal(t, "excluded crate", get(policyFirst, "admin"), rejected(policyCrate))
			wantOK(t, "other crate", get(policySecond, "admin"))
			excluded = "index/**"
			f.patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
			wantRefusal(t, "excluded index", get(policyIndex, "admin"), rejected(policyIndex))
			wantOK(t, "config beside an excluded index", get("index/config.json", "admin"))
			// The patterns do not refuse the versions API: where 7.x would
			// keep it is not documented.
			wantOK(t, "versions beside an excluded index", get("api/v1/crates/hello_cargo", "admin"))
			wantOK(t, "crate beside an excluded index", get(policyFirst, "admin"))
			excluded = "config.json"
			f.patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
			wantRefusal(t, "excluded config", get("index/config.json", "admin"), rejected("config.json"))
			wantOK(t, "index beside an excluded config", get(policyIndex, "admin"))
		})
	}
}

// deliveryHook is the source service's revalidation hook. It counts its
// calls and answers err from call number from on, when from is set.
type deliveryHook struct {
	calls, from int
	err         error
}

func (h *deliveryHook) check(context.Context, source.Request, source.Permit) error {
	h.calls++
	if h.from > 0 && h.calls >= h.from {
		return h.err
	}
	return nil
}

// policyRemote is a remote over an upstream sparse index of hello_cargo
// 1.0.0 and 1.1.0, with a revalidation hook.
func policyRemote(t *testing.T) (*fixture, *fakeupstream.Server, *deliveryHook) {
	t.Helper()
	up := fakeupstream.New(t, fakeupstream.Options{})
	hook := new(deliveryHook)
	f := newFixture(t, repo.Remote, func(db *inmem.Store, items *item.Service) *source.Service {
		c, err := items.Store.Repos().Get(t.Context(), "cargo-test")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/cargo/"
		if err := items.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := upstream.NewResolver(db, nil, upstream.ResolverOptions{RevisionKey: key})
		if err != nil {
			t.Fatal(err)
		}
		pool := new(upstream.Pool)
		t.Cleanup(pool.Close)
		policy := &remote.Policy{
			Store: db, Blobs: items.Blobs, Items: items, Pool: pool,
			Options: resolver.Options, Key: key,
		}
		return &source.Service{
			HTTP: policy, Cache: policy,
			Revalidate: hook.check,
			Resolve: func(ctx context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return items.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
	var lines strings.Builder
	for _, v := range []string{"1.0.0", "1.1.0"} {
		crate := archive(t, "hello_cargo", v)
		sum := sha256.Sum256(crate)
		lines.WriteString(`{"name":"hello_cargo","vers":"` + v + `","deps":[],"features":{},"cksum":"` +
			hex.EncodeToString(sum[:]) + `","yanked":false}` + "\n")
		up.Set("/cargo/crates/hello_cargo/"+v+"/download", fakeupstream.Resource{Body: crate})
	}
	up.Set("/cargo/config.json",
		fakeupstream.Resource{Body: []byte(`{"dl":"` + up.URL + `/cargo/crates"}`)})
	up.Set("/cargo/he/ll/hello_cargo", fakeupstream.Resource{Body: []byte(lines.String())})
	return f, up, hook
}

// TestAccDownloadPolicyOfARemoteRepository: a blacked-out remote refuses
// config.json, an index file and a crate with the blackout's 404 naming
// each one's 7.x path, before the upstream is read; its patterns refuse
// config.json and an index file at those paths, and a crate at its own,
// while a crate they reject keeps its index line and the lookups a
// download makes are not refused by them (#1237).
func TestAccDownloadPolicyOfARemoteRepository(t *testing.T) {
	f, up, _ := policyRemote(t)
	get := func(p, user string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, policyBase+p, user, nil)
	}
	blackout := true
	f.patch(func(c *repo.Config) { c.BlackedOut = &blackout })
	wantRefusal(t, "blacked out config", get("index/config.json", "admin"), blackedOut("config.json"))
	wantRefusal(t, "blacked out index", get(policyIndex, "admin"), blackedOut(policyIndex))
	wantRefusal(t, "blacked out crate", get(policyFirst, "admin"), blackedOut(policyCrate))
	if got := get(policyIndex, "nobody"); got.Code != http.StatusForbidden {
		t.Errorf("blacked out index, no permission: %d %s", got.Code, got.Body)
	}
	for _, p := range []string{"/cargo/config.json", "/cargo/he/ll/hello_cargo"} {
		if n := up.Count(http.MethodGet, p); n != 0 {
			t.Errorf("blacked out remote read %s %d times", p, n)
		}
	}
	blackout = false
	excluded := policyCrate
	f.patch(func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &blackout, &excluded })
	wantOK(t, "excluded crate's index", get(policyIndex, "admin"), `"vers":"1.0.0"`, `"vers":"1.1.0"`)
	wantRefusal(t, "excluded crate", get(policyFirst, "admin"), rejected(policyCrate))
	wantOK(t, "other crate", get(policySecond, "admin"))
	excluded = "index/**"
	f.patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantRefusal(t, "excluded index", get(policyIndex, "admin"), rejected(policyIndex))
	wantOK(t, "crate found through an excluded index", get(policyFirst, "admin"))
	wantOK(t, "config beside an excluded index", get("index/config.json", "admin"))
	excluded = "config.json"
	f.patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantRefusal(t, "excluded config", get("index/config.json", "admin"), rejected("config.json"))
	wantOK(t, "crate found through an excluded config", get(policySecond, "admin"))
	wantOK(t, "index beside an excluded config", get(policyIndex, "admin"))
}

// TestAccDownloadPolicyRefusalOfARemoteRevalidation: a remote's policy
// refusal answers its own 404 and message in Artifactory's envelope, where
// the revalidation of config.json returns it unmapped too (R4); another
// failure there keeps its answer (#1237).
func TestAccDownloadPolicyRefusalOfARemoteRevalidation(t *testing.T) {
	f, _, hook := policyRemote(t)
	get := func() *httptest.ResponseRecorder {
		return f.do(http.MethodGet, policyBase+"index/config.json", "admin", nil)
	}
	wantOK(t, "config", get())
	// The service calls the hook while it reads config.json; the handler's
	// revalidation before it answers is the request's last call.
	perRequest := hook.calls
	message := blackedOut("config.json")
	hook.from, hook.err = hook.calls+perRequest, format.Rejectf(http.StatusNotFound, "%s", message)
	wantRefusal(t, "refused at revalidation", get(), message)
	hook.from = hook.calls + 1
	wantRefusal(t, "refused at the read", get(), message)
	hook.from, hook.err = hook.calls+perRequest, source.ErrUnavailable
	if got := get(); got.Code != http.StatusInternalServerError {
		t.Errorf("unavailable at revalidation: %d %s", got.Code, got.Body)
	}
}
