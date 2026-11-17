package pkgroute_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// cacheWorld is walkFixture with the merged metadata cache's routes over a
// cache on a fake clock: /cached/* merges a document's version lists
// through Merged, /parts/* keeps only the remote member's part
// (MergedPart) and merges the local members' fresh, and /evict/* drops a
// document (EvictMerged). builds counts merges and reads each member's
// reads of the document; the remote's are in remote.reads too.
type cacheWorld struct {
	*kitFixture
	remote *walkRemote
	cache  *virtual.Cache
	mu     sync.Mutex
	now    time.Time
	builds int
	reads  map[string]int
}

// cacheFixture builds a cacheWorld, with the cache when cached is set. On
// top of walkFixture's grants, scoped may read all of local-b and
// remote-a and only idx/ of local-a, remotescoped all of the locals and
// only idx/ of remote-a, anyall every repository through an ANY target,
// and anyidx every member but only idx/ of the others, the virtual
// repositories among them.
func cacheFixture(t *testing.T, cached bool, tune func(*pkgroute.Options)) *cacheWorld {
	t.Helper()
	f, remote := walkFixture(t, nil)
	w := &cacheWorld{
		kitFixture: f, remote: remote, reads: map[string]int{},
		now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC),
	}
	all, idx := accesstest.Target{Include: []string{"**"}}, accesstest.Target{Include: []string{"idx/**"}}
	for user, targets := range map[string]map[string]accesstest.Target{
		"scoped":       {"local-a": idx, "local-b": all, "remote-a": all},
		"remotescoped": {"local-a": all, "local-b": all, "remote-a": idx},
		"anyall":       {api.TargetAny: all},
		"anyidx":       {api.TargetAny: idx, "local-a": all, "local-b": all, "remote-a": all},
	} {
		if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
			Name: user,
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{user: api.ActionRead}},
				Targets: targets,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	o := f.o
	if cached {
		w.cache = virtual.NewCache(virtual.CacheOptions{Now: w.clock})
		o.Cache = w.cache
	}
	if tune != nil {
		tune(&o)
	}
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Remote: remote, repo.Virtual: pkgroute.Virtual{},
	}
	r := chi.NewRouter()
	r.Use(baseurl.Middleware(""))
	r.Use(accesstest.Over(t, f.db).Middleware(t, true, func(r *http.Request) kernel.Principal {
		name := r.Header.Get("X-Test-User")
		return kernel.Principal{Name: name, Admin: name == "root"}
	}))
	pkgroute.Mount(r, o, w.format(sources))
	f.h = r
	return w
}

func (w *cacheWorld) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

func (w *cacheWorld) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
}

// format is walkFormat with the cache's routes.
func (w *cacheWorld) format(sources map[repo.Class]pkgroute.Source) pkgroute.Protocol {
	p := walkFormat(sources)
	routes := p.Routes
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		routes(r, k)
		path := func(r *http.Request) string {
			_, rest, _ := strings.Cut(pkgroute.RequestFrom(r).Rest, "/")
			return rest
		}
		read := func(mr *http.Request, p string) ([]string, error) {
			w.reads[pkgroute.RequestFrom(mr).Repo.Key]++
			return readVersions(k, mr, p)
		}
		merge := func(
			r *http.Request, p string, read func(*http.Request) ([]string, error),
		) (virtual.Entry, error) {
			w.builds++
			parts, err := pkgroute.Collect(k, r, p, read)
			if err != nil {
				return virtual.Entry{}, err
			}
			if len(parts) == 0 {
				return virtual.Entry{}, apierror.New(http.StatusNotFound, noMember)
			}
			doc, err := versionsMerger{
				base: baseurl.From(r.Context()), key: pkgroute.RequestFrom(r).Repo.Key,
			}.Merge(r.Context(), parts)
			if err != nil {
				return virtual.Entry{}, err
			}
			body, err := json.Marshal(doc)
			return virtual.Entry{Body: body, Header: http.Header{
				"Content-Type": {"application/json"}, "Etag": {fmt.Sprintf(`"%d"`, w.builds)},
			}}, err
		}
		answer := func(rw http.ResponseWriter, r *http.Request, e virtual.Entry, err error) {
			if err != nil {
				k.Error(rw, r, pkgroute.SourceError(err, false))
				return
			}
			for name, values := range e.Header {
				rw.Header()[name] = values
			}
			_, _ = rw.Write(e.Body)
		}
		r.Get("/cached/*", func(rw http.ResponseWriter, r *http.Request) {
			p := path(r)
			e, err := k.Merged(r, p, p, func(r *http.Request) (virtual.Entry, error) {
				return merge(r, p, func(mr *http.Request) ([]string, error) { return read(mr, p) })
			})
			answer(rw, r, e, err)
		})
		r.Get("/parts/*", func(rw http.ResponseWriter, r *http.Request) {
			p := path(r)
			e, err := merge(r, p, func(mr *http.Request) ([]string, error) {
				if pkgroute.RequestFrom(mr).Repo.Class != repo.Remote {
					versions, err := read(mr, p)
					if errors.Is(err, kernel.ErrNotFound) {
						k.EvictMerged(mr, p)
					}
					return versions, err
				}
				part, err := k.MergedPart(mr, p, func(mr *http.Request) (virtual.Entry, error) {
					versions, err := read(mr, p)
					return virtual.Entry{Body: []byte(strings.Join(versions, ","))}, err
				})
				if err != nil {
					return nil, err //nolint:wrapcheck // the kit's own answer
				}
				return strings.Split(string(part.Body), ","), nil
			})
			answer(rw, r, e, err)
		})
		r.Get("/evict/*", func(rw http.ResponseWriter, r *http.Request) {
			k.EvictMerged(r, path(r))
			rw.WriteHeader(http.StatusNoContent)
		})
	}
	return p
}

// get answers GET path as user with headers, failing t unless it is 200.
func (w *cacheWorld) get(t *testing.T, path, user string, headers ...string) *http.Response {
	t.Helper()
	rec := w.do(http.MethodGet, base+path, user, "", headers...)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s as %q: %d %s", path, user, rec.Code, rec.Body)
	}
	return rec.Result()
}

// wantBuilds checks that GET path as user merged n times.
func (w *cacheWorld) wantBuilds(t *testing.T, path, user string, n int) {
	t.Helper()
	before := w.builds
	w.get(t, path, user)
	if w.builds-before != n {
		t.Fatalf("GET %s as %q: %d merges, want %d", path, user, w.builds-before, n)
	}
}

// body is res's body.
func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAccMergedCache: within the virtual repository's period a second
// request reads no member and answers the first merge with its headers;
// past it every member is read again. A period of 0, or -1 stored by an
// older configuration, merges on every request.
func TestAccMergedCache(t *testing.T) {
	w := cacheFixture(t, true, nil)
	first := w.get(t, "outer/cached/idx/versions", "merger")
	if w.builds != 1 || len(w.remote.reads) != 1 || w.reads["local-a"] != 1 {
		t.Fatalf("first: %d merges, reads %v %v", w.builds, w.reads, w.remote.reads)
	}
	firstBody := body(t, first)
	w.advance(2 * time.Hour)
	second := w.get(t, "outer/cached/idx/versions", "merger")
	if w.builds != 1 || len(w.remote.reads) != 1 || w.reads["local-a"] != 1 ||
		body(t, second) != firstBody || second.Header.Get("ETag") != `"1"` ||
		second.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("within the period: %d merges, reads %v %v, %v",
			w.builds, w.reads, w.remote.reads, second.Header)
	}
	w.advance(time.Second)
	if third := w.get(t, "outer/cached/idx/versions", "merger"); third.Header.Get("ETag") != `"2"` ||
		len(w.remote.reads) != 2 || w.reads["local-b"] != 2 {
		t.Fatalf("past the period: %v, reads %v %v", third.Header, w.reads, w.remote.reads)
	}
	for _, secs := range []int{0, -1} {
		w.patch(t, "outer", func(c *repo.Config) { c.VirtualRetrievalCachePeriodSecs = &secs })
		w.wantBuilds(t, "outer/cached/idx/versions", "merger", 1)
		w.wantBuilds(t, "outer/cached/idx/versions", "merger", 1)
	}
	// Without a cache, or through a repository that is not virtual, every
	// request merges.
	w.wantBuilds(t, "local-b/cached/idx/versions", "merger", 1)
	w.wantBuilds(t, "local-b/cached/idx/versions", "merger", 1)
	plain := cacheFixture(t, false, nil)
	plain.wantBuilds(t, "outer/cached/idx/versions", "merger", 1)
	plain.wantBuilds(t, "outer/cached/idx/versions", "merger", 1)
}

// TestAccMergedVisibility: callers who may read different members, and an
// administrator, who may read the virtual repository itself, each get an
// entry of their own, which a caller who may read every repository
// through an ANY target shares with the administrator; a caller whose
// grant covers only some paths of a member, or of the virtual repository,
// gets a merge of its own on every request.
func TestAccMergedVisibility(t *testing.T) {
	w := cacheFixture(t, true, nil)
	for _, user := range []string{"merger", "partial", "root"} {
		w.wantBuilds(t, "outer/cached/idx/versions", user, 1)
		w.wantBuilds(t, "outer/cached/idx/versions", user, 0)
	}
	w.wantBuilds(t, "outer/cached/idx/versions", "anyall", 0)
	for _, user := range []string{"scoped", "anyidx"} {
		w.wantBuilds(t, "outer/cached/idx/versions", user, 1)
		w.wantBuilds(t, "outer/cached/idx/versions", user, 1)
	}
	if got := body(t, w.get(t, "outer/cached/idx/versions", "partial")); strings.Contains(got, "local-b") {
		t.Fatalf("partial got another caller's merge: %s", got)
	}
}

// TestAccMergedMembers: a member removed from the virtual repository, a
// new one, a remote member's step without its upstream (R7) and another
// base URL each miss the entries built before; the virtual repository's
// own patterns refuse the document whatever the cache holds.
func TestAccMergedMembers(t *testing.T) {
	w := cacheFixture(t, true, func(o *pkgroute.Options) {
		o.NoUpstream = func(r *http.Request, _ repo.Config) bool {
			return r.Header.Get("X-From-Artifactory") != ""
		}
	})
	const doc = "outer/cached/idx/versions"
	w.wantBuilds(t, doc, "merger", 1)
	w.patch(t, "outer", func(c *repo.Config) { c.Repositories = []string{"inner"} })
	w.wantBuilds(t, doc, "merger", 1)
	w.patch(t, "outer", func(c *repo.Config) {
		c.Repositories = []string{"inner", "local-b", "gen-local"}
	})
	w.wantBuilds(t, doc, "merger", 1)
	w.patch(t, "outer", func(c *repo.Config) { c.Repositories = []string{"inner", "local-b"} })
	w.wantBuilds(t, doc, "merger", 0)

	before := w.builds
	w.get(t, doc, "merger", "X-From-Artifactory", "true")
	w.get(t, doc, "merger", "X-From-Artifactory", "true")
	other := w.get(t, doc, "merger", baseurl.OverrideHeader, "https://other.example")
	if w.builds-before != 2 || !strings.Contains(body(t, other), "https://other.example/artifactory/") {
		t.Fatalf("%d merges", w.builds-before)
	}

	w.patch(t, "outer", func(c *repo.Config) { c.ExcludesPattern = strPtr("idx/**") })
	before = w.builds
	w.wantPolicy(t, doc, "merger", http.StatusNotFound,
		"No repository found to serve the request for outer:idx/versions")
	if w.builds != before {
		t.Fatal("a refused document was merged")
	}
}

// TestAccMergedGone: a document gone from every member answers 404 and
// its entry is dropped, so a rebuild that fails has nothing to fall back
// on; an expired entry whose rebuild fails is answered instead.
func TestAccMergedGone(t *testing.T) {
	w := cacheFixture(t, true, nil)
	const doc = "outer/cached/solo/versions"
	w.remote.docs["solo/versions"] = "v3.0.0"
	cached := body(t, w.get(t, doc, "merger"))

	w.cache.Zap("outer", "solo/versions")
	w.remote.err = errors.New("boom")
	if got := body(t, w.get(t, doc, "merger")); got != cached {
		t.Fatalf("stale fallback: %s", got)
	}

	w.remote.err = nil
	delete(w.remote.docs, "solo/versions")
	w.cache.Zap("outer", "solo/versions")
	w.wantPolicy(t, doc, "merger", http.StatusNotFound, noMember)
	w.remote.err = errors.New("boom")
	w.wantPolicy(t, doc, "merger", http.StatusInternalServerError, "")
}

// TestAccMergedParts: a format that keeps only the remote member's part
// reads the local members on every request and the remote once per
// period; a caller with a grant on part of the remote reads it every
// time; EvictMerged drops the document's parts, through the virtual
// repository or a member request whose read found nothing; and a direct
// request to the remote has no part to keep.
func TestAccMergedParts(t *testing.T) {
	w := cacheFixture(t, true, nil)
	const doc = "outer/parts/idx/versions"
	first := body(t, w.get(t, doc, "merger"))
	second := body(t, w.get(t, doc, "merger"))
	if first != second || len(w.remote.reads) != 1 || w.reads["local-a"] != 2 || w.builds != 2 {
		t.Fatalf("within the period: %d merges, reads %v %v", w.builds, w.reads, w.remote.reads)
	}
	w.advance(2*time.Hour + time.Second)
	w.get(t, doc, "merger")
	if len(w.remote.reads) != 2 {
		t.Fatalf("past the period: remote reads %v", w.remote.reads)
	}
	w.get(t, doc, "remotescoped")
	w.get(t, doc, "remotescoped")
	if len(w.remote.reads) != 4 {
		t.Fatalf("a scoped caller: remote reads %v", w.remote.reads)
	}
	if rec := w.do(http.MethodGet, base+"outer/evict/idx/versions", "merger", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("evict: %d", rec.Code)
	}
	w.get(t, doc, "merger")
	if len(w.remote.reads) != 5 {
		t.Fatalf("after the eviction: remote reads %v", w.remote.reads)
	}
	if rec := w.do(http.MethodGet, base+"local-b/evict/idx/versions", "merger", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("evict through a local repository: %d", rec.Code)
	}
	w.get(t, "remote-a/parts/idx/versions", "merger")
	w.get(t, "remote-a/parts/idx/versions", "merger")
	if len(w.remote.reads) != 7 {
		t.Fatalf("direct: remote reads %v", w.remote.reads)
	}
	// Only the remote holds solo/versions, so each request's local reads
	// find nothing and drop the remote's part before it is read.
	w.remote.docs["solo/versions"] = "v3.0.0"
	w.get(t, "outer/parts/solo/versions", "merger")
	w.get(t, "outer/parts/solo/versions", "merger")
	if len(w.remote.reads) != 9 {
		t.Fatalf("a member's 404: remote reads %v", w.remote.reads)
	}
	plain := cacheFixture(t, false, nil)
	plain.get(t, doc, "merger")
	plain.get(t, doc, "merger")
	if len(plain.remote.reads) != 2 {
		t.Fatalf("no cache: remote reads %v", plain.remote.reads)
	}
}
