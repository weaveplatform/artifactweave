package artifactshttp

// This file holds the tests of the storage API's file list as it is written
// from its trees' store pages (#1409, decision 0043 "Streaming complete
// lists"): that it answers, on every backend, the bytes of the list that
// was collected whole and sorted, and how a store failure before and after
// the first byte ends the answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	pgdrv "github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// collectedPrefix is where fileListRouter serves collectedFileList.
const collectedPrefix = "/collected/"

// collectedFileList is the file list as getStorage answered it before
// #1409 streamed it: the same checks, then below's items from every tree,
// rendered and sorted whole by render. The streamed list must answer its
// bytes.
func (s ArtifactoryAPI) collectedFileList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, p, q, err := s.storageRequest(r, collectedPrefix)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if !v.acc.Folder(v.repo, v.path, accessapi.ActionRead) || p.Anonymous {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if v.repo.Class != repo.Virtual {
		if err := generic.CheckBlackedOut(v.repo, v.path); err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "storage list", err))
			return
		}
	}
	f, err := v.target(ctx)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage item", err))
		return
	}
	if !v.acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if !f.Item.Folder {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage list", httpkit.BadRequestf(
			"%s is a file; list needs a folder", f.Item.StoragePath())))
		return
	}
	l, err := parseListing(q)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage list", err))
		return
	}
	folder := f.Item.FullPath()
	items, err := v.below(ctx, folder, l.isDeep)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage list", err))
		return
	}
	files := make([]listed, 0, len(items))
	for _, it := range items {
		files = append(files, listed{path: it.FullPath(), item: it})
	}
	w.Header().Set("Content-Type", mediaFileList)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(l.render(v.uri(ctx, folder), folder, f.Item, files))
}

// fileListStore opens an empty store of backend, with the built-in
// principals, admin's password "pw".
func fileListStore(t *testing.T, backend string) serverStore {
	t.Helper()
	ctx := t.Context()
	var db serverStore
	switch backend {
	case "inmem":
		db = inmem.New()
	case "sqlite":
		s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "list.db"), sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		db = s
	default:
		s, err := postgres.Open(ctx, platsqltest.PostgresDSN(t), postgres.Options{MaxOpenConns: 8})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		appstoretest.Reset(t, s.DB(), pgdrv.Dialect)
		db = s
	}
	support.Seed(t, db)
	return db
}

// fileListRouter serves the storage routes over db, and collectedFileList
// under collectedPrefix, to callers who send Basic credentials.
func fileListRouter(t *testing.T, db serverStore) http.Handler {
	t.Helper()
	k := accesstest.Over(t, db)
	s := newArtifactoryAPI(Config{Store: db, Access: k.API()})
	mux := chi.NewRouter()
	mux.Use(k.Middleware(t, false, func(r *http.Request) kernel.Principal {
		user, password, _ := r.BasicAuth()
		p, err := k.API().Authenticator.Basic(r.Context(), user, password)
		if err != nil {
			t.Errorf("basic credentials of %q: %v", user, err)
		}
		return p
	}))
	s.RegisterStorageRoutes(mux)
	mux.Get(collectedPrefix+"*", s.collectedFileList)
	return mux
}

// Sizes that make the walks cross store pages of kernel.MaxLimit: deep/
// holds fileListDeep files of libs-local below 20 folders, and wide/ holds
// fileListWide files, each in libs-local or libs-other or both.
const (
	fileListDeep = 1200
	fileListWide = 2300
)

// fileListTricky are full paths whose byte order differs from their
// (folder, name) order or from a case-folded or accent-aware order:
// "-", " " and "!" sort before "/", "B" before "b", "é" after "z".
var fileListTricky = []string{
	"a/b/c/x.txt", "a/b/!", "a/b-c/y", "a/b c/z", "a/b.d", "a/B", "a/é/e.txt", "a/z",
	"a/0", "a/b+c", "a/~", "z.txt", "-.txt", "A.txt", "é.txt",
}

// seedFileLists creates libs-local and libs-other (local), remote (whose
// cache holds files), libs (virtual over libs-local, remote, libs-other and
// a missing member) and outer (virtual over libs, libs-local and
// libs-other, which the walk visits once each). Paths in both locals carry
// different content, so the entry shows whose item is listed. pr reads
// a/** and wide/f0* of libs-local, a/b/** and wide/** of libs-other, and
// every path of remote and of the virtual repositories.
func seedFileLists(t *testing.T, db serverStore) {
	t.Helper()
	ctx := t.Context()
	for _, c := range []repo.Config{
		{Key: "libs-local", Class: repo.Local},
		{Key: "libs-other", Class: repo.Local},
		{Key: "remote", Class: repo.Remote, URL: "https://upstream.example/"},
		{
			Key: "libs", Class: repo.Virtual,
			Repositories: []string{"libs-local", "remote", "libs-other", "gone"},
		},
		{
			Key: "outer", Class: repo.Virtual,
			Repositories: []string{"libs", "libs-local", "libs-other"},
		},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	modified := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	err := db.Tx(ctx, func(ctx context.Context) error {
		folders := map[string]bool{}
		put := func(key, full string, cached bool) error {
			for dir := path.Dir(full); dir != "."; dir = path.Dir(dir) {
				if folders[key+"/"+dir] {
					continue
				}
				folders[key+"/"+dir] = true
				p, name := path.Split(dir)
				if _, err := db.Items().Put(ctx, model.Item{
					RepoKey: key, Path: folderOf(p), Name: name, Folder: true, Cached: cached,
					Modified: modified,
				}); err != nil {
					return fmt.Errorf("folder %s/%s: %w", key, dir, err)
				}
			}
			p, name := path.Split(full)
			content := key + ":" + full
			it := model.Item{
				RepoKey:  key,
				Path:     folderOf(p),
				Name:     name,
				Cached:   cached,
				Size:     int64(len(content)),
				Modified: modified,
				Actual: kernel.Checksums{
					SHA1: sha1Hex(content), SHA256: sha256Hex(content), MD5: md5Hex(content),
				},
			}
			if len(full)%3 == 0 {
				it.PropertiesModified = modified.Add(time.Minute)
			}
			if _, err := db.Items().Put(ctx, it); err != nil {
				return fmt.Errorf("file %s/%s: %w", key, full, err)
			}
			return nil
		}
		var writes []func() error
		for _, full := range fileListTricky {
			writes = append(writes, func() error { return put("libs-local", full, false) })
		}
		for _, full := range []string{"a/b-c", "a/b/c/x.txt", "a/b/d.txt", "other/x"} {
			writes = append(writes, func() error { return put("libs-other", full, false) })
		}
		for _, full := range []string{"a/r.txt", "deep/d00/f000", "wide/f0001", "a/B"} {
			writes = append(writes, func() error { return put("remote", full, true) })
		}
		for i := range fileListDeep {
			full := fmt.Sprintf("deep/d%02d/f%03d", i%20, i/20)
			writes = append(writes, func() error { return put("libs-local", full, false) })
		}
		for i := range fileListWide {
			full := fmt.Sprintf("wide/f%04d", i)
			if i%2 == 0 || i%7 == 0 {
				writes = append(writes, func() error { return put("libs-local", full, false) })
			}
			if i%2 == 1 || i%7 == 0 {
				writes = append(writes, func() error { return put("libs-other", full, false) })
			}
		}
		for _, write := range writes {
			if err := write(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Users().
		Create(ctx, accesstest.User{Name: "pr", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "pr-lists", Resources: map[accessapi.ResourceType]accesstest.Resource{
			accessapi.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]accessapi.Actions{"pr": accessapi.ActionRead},
				},
				Targets: map[string]accesstest.Target{
					"libs-local": {Include: []string{"a/**", "wide/f0*"}},
					"libs-other": {Include: []string{"a/b/**", "wide/**"}},
					"remote":     {Include: []string{"**"}},
					"libs":       {Include: []string{"**"}},
					"outer":      {Include: []string{"**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// folderOf is a path.Split directory as an item's path: "." at the root.
func folderOf(dir string) string {
	if dir = strings.TrimSuffix(dir, "/"); dir == "" {
		return "."
	}
	return dir
}

// created is the file list's own time, which each answer stamps anew.
var created = regexp.MustCompile(`"created":"[^"]*"`)

// TestAccStorageListStreamsTheCollectedList: on every backend, the file
// list of local, remote and virtual repositories, deep and not, with each
// listing parameter, answers the collected list's bytes, for the
// administrator and for a caller who reads part of each tree, and long
// lists cross several store pages of every tree they merge.
func TestAccStorageListStreamsTheCollectedList(t *testing.T) {
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			db := fileListStore(t, backend)
			seedFileLists(t, db)
			h := fileListRouter(t, db)
			longest := map[string]int{}
			for _, key := range []string{"libs-local", "libs-other", "remote", "libs", "outer"} {
				for _, folder := range []string{"", "a", "a/b", "deep", "wide"} {
					for _, query := range []string{
						"deep=1&listFolders=1&includeRootPath=1&mdTimestamps=1",
						"deep=1",
						"deep=1&depth=2&listFolders=1",
						"listFolders=1",
						"",
						"deep=0&includeRootPath=1&listFolders=1&depth=1&mdTimestamps=1",
					} {
						for _, user := range []string{"admin", "pr"} {
							target := key + "/" + folder + "?list&" + query
							got := do(
								t,
								h,
								http.MethodGet,
								storagePrefix+target,
								basic(user, "pw"),
								"",
							)
							want := do(
								t,
								h,
								http.MethodGet,
								collectedPrefix+target,
								basic(user, "pw"),
								"",
							)
							if got.Code != want.Code ||
								got.Header().
									Get("Content-Type") !=
									want.Header().
										Get("Content-Type") ||
								created.ReplaceAllString(got.Body.String(), "") !=
									created.ReplaceAllString(want.Body.String(), "") {
								t.Fatalf(
									"%s as %s: %d %s, collected %d %s\n got %.400s\nwant %.400s",
									target,
									user,
									got.Code,
									got.Header().Get("Content-Type"),
									want.Code,
									want.Header().Get("Content-Type"),
									got.Body,
									want.Body,
								)
							}
							if got.Code == http.StatusOK {
								longest[user] = max(
									longest[user],
									strings.Count(got.Body.String(), `"uri":`),
								)
							}
						}
					}
				}
			}
			// The longest lists cross several pages of the trees they merge,
			// and the partial reader's leave entries out.
			if longest["admin"] < 2*kernel.MaxLimit || longest["pr"] >= longest["admin"] ||
				longest["pr"] < kernel.MaxLimit {
				t.Fatalf("longest lists: %v", longest)
			}
		})
	}
}

// TestAccStorageListAnswersAsBefore: what the list refuses or cannot find
// is answered before it is walked, as it was.
func TestAccStorageListAnswersAsBefore(t *testing.T) {
	db := fileListStore(t, "inmem")
	seedFileLists(t, db)
	h := fileListRouter(t, db)
	for target, want := range map[string]int{
		"libs-local/a/z?list":                http.StatusBadRequest,
		"libs-local/none?list":               http.StatusNotFound,
		"libs/a?list&deep=1&depth=-1":        http.StatusBadRequest,
		"libs/a?list&deep=1&depth=x":         http.StatusBadRequest,
		"libs-other/deep?list&deep=1":        http.StatusNotFound,
		"libs-local/wide?list&listFolders=1": http.StatusOK,
	} {
		got := do(t, h, http.MethodGet, storagePrefix+target, basic("admin", "pw"), "")
		was := do(t, h, http.MethodGet, collectedPrefix+target, basic("admin", "pw"), "")
		if got.Code != want || was.Code != want ||
			created.ReplaceAllString(got.Body.String(), "") !=
				created.ReplaceAllString(was.Body.String(), "") {
			t.Errorf("%s: %d %s, want %d as before: %s", target, got.Code, got.Body, want, was.Body)
		}
	}
}

// fileListFaults fails the item store's walks of a folder, by full path
// and by (path, name) alike, once skip walks have been let through.
type fileListFaults struct {
	serverStore
	skip *int
}

func (f fileListFaults) Items() port.ItemStore {
	return faultyFileWalks{f.serverStore.Items(), f.skip}
}

type faultyFileWalks struct {
	port.ItemStore
	skip *int
}

func (f faultyFileWalks) fail() bool {
	if *f.skip == 0 {
		return true
	}
	*f.skip--
	return false
}

func (f faultyFileWalks) DescendantsByFullPath(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	if f.fail() {
		return nil, "", errBoom
	}
	return f.ItemStore.DescendantsByFullPath(
		ctx,
		repoKey,
		cached,
		folder,
		p,
	) //nolint:wrapcheck // test double
}

func (f faultyFileWalks) Descendants(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	if f.fail() {
		return nil, "", errBoom
	}
	return f.ItemStore.Descendants(
		ctx,
		repoKey,
		cached,
		folder,
		p,
	) //nolint:wrapcheck // test double
}

// TestAccStorageListStoreFaults: a store failure on the first page answers
// the collected list's error; one after the first page has gone out aborts
// the answer, so the client reads a body that is cut short and that no
// JSON parser accepts.
func TestAccStorageListStoreFaults(t *testing.T) {
	db := fileListStore(t, "inmem")
	seedFileLists(t, db)
	skip := 0
	h := fileListRouter(t, fileListFaults{serverStore: db, skip: &skip})
	const target = "libs-local/?list&deep=1"
	got := do(t, h, http.MethodGet, storagePrefix+target, basic("admin", "pw"), "")
	was := do(t, h, http.MethodGet, collectedPrefix+target, basic("admin", "pw"), "")
	if got.Code != http.StatusInternalServerError || got.Body.String() != was.Body.String() ||
		got.Header().Get("Content-Type") != was.Header().Get("Content-Type") {
		t.Fatalf("failure before the first byte: %d %s, collected %d %s",
			got.Code, got.Body, was.Code, was.Body)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	// The first page of libs-local's walk is read; the second fails.
	skip = 1
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+storagePrefix+target, nil)
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
	if res.StatusCode != http.StatusOK || err == nil ||
		!bytes.HasPrefix(
			body,
			[]byte(`{"created":`),
		) || !bytes.Contains(body, []byte(`"files":[{`)) {
		t.Fatalf("failure after the first byte: %d, %d bytes, read error %v",
			res.StatusCode, len(body), err)
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		t.Fatal("the cut body parses as JSON")
	}
}
