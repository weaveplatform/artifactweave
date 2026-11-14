package artifacts_test

// This file holds the tests of Get Repositories without limit and of the
// dependency search, which stream from store pages (#1409, decision 0043
// "Streaming complete lists"): that each answers, on every backend and
// over several store pages, the bytes of the list it collected before, and
// how a store failure before and after the first byte ends the answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	artport "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// streamedStore opens an empty store of backend with the built-in
// principals, admin's password "pw".
func streamedStore(t *testing.T, backend string) harness.Store {
	t.Helper()
	var db harness.Store
	switch backend {
	case "inmem":
		db = inmem.New()
	case "sqlite":
		s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "lists.db"), sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		db = s
	default:
		db = openPostgres(t)
	}
	cheapSeed(t, db)
	return db
}

// streamedRouter is the router over db, as reg.example, with tune applied.
func streamedRouter(t *testing.T, db harness.Store, tune ...func(*Config)) http.Handler {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	c := Config{BaseURL: "https://reg.example", Store: db, Blobs: blobs, Auth: a}
	for _, f := range tune {
		f(&c)
	}
	return New(c)
}

// streamedRepos is how many repositories seedStreamedRepos creates: more
// than one window of the walk without limit, which reads maxRepoPage
// (1000) at a time.
const streamedRepos = 2150

// seedStreamedRepos creates streamedRepos repositories of every class and
// several package types, some in project proj and some with a description
// json.Encoder escapes, and reader, who sees the remote repositories and
// every tenth local one.
func seedStreamedRepos(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	targets := map[string]accesstest.Target{"ANY REMOTE": {}}
	err := db.Tx(ctx, func(ctx context.Context) error {
		for i := range streamedRepos {
			c := repo.Config{
				Key:         fmt.Sprintf("r%04d", i),
				Class:       repo.Local,
				PackageType: repo.Maven,
			}
			switch {
			case i%50 == 7:
				c.Class = repo.Virtual
			case i%3 == 1:
				c.Class, c.PackageType, c.URL = repo.Remote, repo.Generic, "https://upstream.example/"
			case i%3 == 2:
				c.PackageType = repo.NPM
			}
			if i%9 == 0 {
				c.Description = fmt.Sprintf("repo <%d> & more", i)
			}
			if i%11 == 0 {
				c.ProjectKey = "proj"
			}
			if c.Class == repo.Local && i%10 == 0 {
				targets[c.Key] = accesstest.Target{}
			}
			if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
				return fmt.Errorf("create %s: %w", c.Key, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Users().
		Create(ctx, accesstest.User{Name: "reader", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "reader-repos", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: targets,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// pagedRepositories is the list as its windows of 1000 answer it, joined
// into one array as the list without limit collected it, and the windows'
// Content-Type.
func pagedRepositories(
	t *testing.T,
	h http.Handler,
	query, user string,
	accept map[string]string,
) (string, string) {
	t.Helper()
	var elems []string
	media := ""
	for offset := 0; ; offset += 1000 {
		headers := basic(user, "pw")
		for k, v := range accept {
			headers[k] = v
		}
		rec := do(
			t,
			h,
			http.MethodGet,
			fmt.Sprintf("/artifactory/api/repositories?%slimit=1000&offset=%d",
				query, offset),
			headers,
			"",
		)
		wantStatus(t, rec, http.StatusOK)
		media = rec.Header().Get("Content-Type")
		var page []json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			elems = append(elems, string(e))
		}
		if rec.Header().Get("Link") == "" {
			return "[" + strings.Join(elems, ",") + "]\n", media
		}
	}
}

// TestAccStreamedRepositoriesAcrossBackends: Get Repositories without limit
// answers, on every backend and over several windows of the store, the
// windows' repositories as one array, in the media type asked for and with
// no paging header, for the administrator and for a caller who sees part
// of the list, with each filter.
func TestAccStreamedRepositoriesAcrossBackends(t *testing.T) {
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			db := streamedStore(t, backend)
			seedStreamedRepos(t, db)
			h := streamedRouter(t, db)
			seen := map[string]int{}
			for _, query := range []string{
				"", "type=local&", "type=remote&", "type=virtual&", "packageType=npm&",
				"project=proj&", "type=distribution&", "type=local&packageType=maven&project=proj&",
			} {
				for _, user := range []string{"admin", "reader"} {
					for _, accept := range []map[string]string{nil, {"Accept": mediaDetails}} {
						want, media := pagedRepositories(t, h, query, user, accept)
						headers := basic(user, "pw")
						for k, v := range accept {
							headers[k] = v
						}
						rec := do(
							t,
							h,
							http.MethodGet,
							"/artifactory/api/repositories?"+query,
							headers,
							"",
						)
						if rec.Code != http.StatusOK || rec.Body.String() != want ||
							rec.Header().Get("Content-Type") != media {
							t.Fatalf("%q as %s: %d %s\n got %.300s\nwant %.300s", query, user,
								rec.Code, rec.Header().Get("Content-Type"), rec.Body, want)
						}
						for _, h := range []string{"Link", "X-Total-Count", "X-Result-Offset", "X-Result-Limit"} {
							if v := rec.Header().Get(h); v != "" {
								t.Fatalf("%q as %s: %s %q without limit", query, user, h, v)
							}
						}
						seen[user] = max(seen[user], strings.Count(want, `"key":`))
					}
				}
			}
			if seen["admin"] < streamedRepos || seen["reader"] >= seen["admin"] ||
				seen["reader"] < 700 {
				t.Fatalf("longest lists: %v", seen)
			}
		})
	}
}

// pageFaults fails RepoStore.Page, and BuildStore.DependencyPage, once
// skip calls have been let through.
type pageFaults struct {
	harness.Store
	skip *int
}

func (f pageFaults) Repos() artport.RepoStore { return faultyRepoPages{f.Store.Repos(), f.skip} }

func (f pageFaults) Builds() artport.BuildStore {
	return faultyDependencyPages{f.Store.Builds(), f.skip}
}

func failAfter(skip *int) bool {
	if *skip == 0 {
		return true
	}
	*skip--
	return false
}

type faultyRepoPages struct {
	artport.RepoStore
	skip *int
}

func (f faultyRepoPages) Page(
	ctx context.Context,
	l artport.RepoListing,
) (artport.RepoPage, error) {
	if failAfter(f.skip) {
		return artport.RepoPage{}, errBoom
	}
	return f.RepoStore.Page(ctx, l) //nolint:wrapcheck // test double
}

type faultyDependencyPages struct {
	artport.BuildStore
	skip *int
}

func (f faultyDependencyPages) DependencyPage(
	ctx context.Context, c kernel.Checksums, p kernel.Page,
) ([]artmodel.BuildFileRef, string, error) {
	if failAfter(f.skip) {
		return nil, "", errBoom
	}
	return f.BuildStore.DependencyPage(ctx, c, p) //nolint:wrapcheck // test double
}

// cutShort serves path to admin over a real connection and fails unless
// the answer is a 200 whose body begins with prefix, is cut short and
// parses as no JSON.
func cutShort(t *testing.T, h http.Handler, path, prefix string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("admin", "pw")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || err == nil || !bytes.HasPrefix(body, []byte(prefix)) {
		t.Fatalf("%s, failure after the first byte: %d, %d bytes beginning %.80q, read error %v",
			path, res.StatusCode, len(body), body, err)
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		t.Fatalf("%s: the cut body parses as JSON", path)
	}
}

// TestAccStreamedRepositoriesStoreFaults: a store failure on the first
// window answers the 500 the paged list answers; one after the first
// window has gone out cuts the answer short.
func TestAccStreamedRepositoriesStoreFaults(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	seedStreamedRepos(t, db)
	skip := 0
	h := streamedRouter(t, pageFaults{Store: db, skip: &skip})
	got := do(t, h, http.MethodGet, "/artifactory/api/repositories", admin, "")
	paged := do(t, h, http.MethodGet, "/artifactory/api/repositories?limit=10", admin, "")
	if got.Code != http.StatusInternalServerError || got.Body.String() != paged.Body.String() {
		t.Fatalf("failure before the first byte: %d %s, paged %d %s",
			got.Code, got.Body, paged.Code, paged.Body)
	}
	skip = 1
	cutShort(t, h, "/artifactory/api/repositories", `[{"`)
}

// seedDependencies publishes streamedRuns runs that depend on dep: apps
// and libs alternately, every tenth number published twice, every
// twenty-fifth in project proj, each with two modules of eight dependencies
// on dep and one on something else, so dep's rows fill several store
// pages. reader reads the runs of app-*.
func seedDependencies(t *testing.T, db harness.Store, runs, perModule int, dep string) {
	t.Helper()
	ctx := t.Context()
	started := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	err := db.Tx(ctx, func(ctx context.Context) error {
		for i := range runs {
			b := artmodel.Build{
				Name: fmt.Sprintf("lib-%03d", i), Number: "1",
				Started: started.Add(time.Duration(i) * time.Second),
			}
			if i%2 == 0 {
				b.Name = fmt.Sprintf("app-%03d", i)
			}
			if i%25 == 0 {
				b.ProjectKey = "proj"
			}
			for m := range 2 {
				mod := artmodel.BuildModule{ID: fmt.Sprintf("m%d", m)}
				for d := range perModule {
					mod.Dependencies = append(mod.Dependencies, artmodel.BuildFile{
						ID: fmt.Sprintf("org:d%d:1", d), Checksums: kernel.Checksums{SHA1: dep},
					})
				}
				mod.Dependencies = append(mod.Dependencies, artmodel.BuildFile{
					ID: "org:other:1", Checksums: kernel.Checksums{SHA1: sha1Hex("other")},
				})
				b.Modules = append(b.Modules, mod)
			}
			copies := 1
			if i%10 == 0 {
				copies = 2
			}
			for c := range copies {
				b.Started = b.Started.Add(time.Duration(c) * time.Millisecond)
				if _, err := db.Builds().Put(ctx, b); err != nil {
					return fmt.Errorf("build %s: %w", b.Name, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Users().
		Create(ctx, accesstest.User{Name: "reader", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "reader-builds", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceBuild: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{
					api.BuildInfoRepository: {Include: []string{"app-*/**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// collectedDependencies is the dependency search's body as the collected
// search built it from FindDependencies: each build once, in its first
// run's order, those keep accepts, at most limit of them (-1 for all).
func collectedDependencies(
	t *testing.T, db harness.Store, dep string, keep func(artmodel.BuildRun) bool, limit int,
) string {
	t.Helper()
	refs, err := db.Builds().FindDependencies(t.Context(), kernel.Checksums{SHA1: dep})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[[3]string]bool{}
	entries := []map[string]any{}
	for _, ref := range refs {
		r := ref.Run
		id := [3]string{r.ProjectKey, r.Name, r.Number}
		if seen[id] || !keep(r) {
			continue
		}
		seen[id] = true
		u := "https://reg.example/artifactory/api/build/" + r.Name + "/" + r.Number
		if r.ProjectKey != "" {
			u += "?project=" + r.ProjectKey
		}
		entries = append(entries, map[string]any{"uri": u})
		if len(entries) == limit {
			break
		}
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{"results": entries}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestAccStreamedDependencySearchAcrossBackends: on every backend, over
// several store pages of one checksum's dependencies, the dependency search
// answers the collected search's bytes: for the administrator, for a
// caller who reads some builds, under a result cap, in the vendor media
// type, and for a checksum nothing depends on.
func TestAccStreamedDependencySearchAcrossBackends(t *testing.T) {
	dep := sha1Hex("dep")
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			db := streamedStore(t, backend)
			seedDependencies(t, db, 160, 8, dep)
			all := func(artmodel.BuildRun) bool { return true }
			apps := func(r artmodel.BuildRun) bool {
				return r.ProjectKey == "" && strings.HasPrefix(r.Name, "app-")
			}
			const q = "/artifactory/api/search/dependency?sha1="
			for _, c := range []struct {
				h      http.Handler
				sha1   string
				user   string
				accept string
				want   string
				media  string
			}{
				{streamedRouter(t, db), dep, "admin", "", collectedDependencies(t, db, dep, all, -1), "application/json"},
				{
					streamedRouter(t, db), strings.ToUpper(dep), "reader", mediaDependencySearch,
					collectedDependencies(t, db, dep, apps, -1), mediaDependencySearch,
				},
				{
					streamedRouter(t, db, func(c *Config) { c.AQL.MaxResults = 7 }), dep, "admin", "",
					collectedDependencies(t, db, dep, all, 7), "application/json",
				},
				{streamedRouter(t, db), sha1Hex("none"), "admin", "", "{\"results\":[]}\n", "application/json"},
			} {
				headers := basic(c.user, "pw")
				if c.accept != "" {
					headers["Accept"] = c.accept
				}
				rec := do(t, c.h, http.MethodGet, q+c.sha1, headers, "")
				if rec.Code != http.StatusOK || rec.Body.String() != c.want ||
					rec.Header().Get("Content-Type") != c.media {
					t.Fatalf("%s as %s: %d %s\n got %.300s\nwant %.300s", c.sha1, c.user, rec.Code,
						rec.Header().Get("Content-Type"), rec.Body, c.want)
				}
			}
			if n := strings.Count(collectedDependencies(t, db, dep, all, -1), `"uri"`); n != 160 {
				t.Fatalf("the admin search names %d builds, want 160", n)
			}
		})
	}
}

// TestAccStreamedDependencySearchStoreFaults: a store failure on the first
// page answers the search's 500; one after the first page has gone out
// cuts the answer short.
func TestAccStreamedDependencySearchStoreFaults(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	dep := sha1Hex("dep")
	// One row on dep per module, so the first page names 500 builds,
	// more than the server buffers before it sends.
	seedDependencies(t, db, 1200, 1, dep)
	skip := 0
	h := streamedRouter(t, pageFaults{Store: db, skip: &skip})
	rec := do(t, h, http.MethodGet, "/artifactory/api/search/dependency?sha1="+dep, admin, "")
	if rec.Code != http.StatusInternalServerError ||
		strings.TrimSpace(
			rec.Body.String(),
		) != `{"errors":[{"message":"internal error","status":500}]}` {
		t.Fatalf("failure before the first byte: %d %s", rec.Code, rec.Body)
	}
	skip = 1
	cutShort(t, h, "/artifactory/api/search/dependency?sha1="+dep, `{"results":[{"uri":`)
}
