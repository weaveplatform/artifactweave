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
	"net/url"
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
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
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

// collectedPrefix is where fileListRouter serves collectedStorage.
const collectedPrefix = "/collected/"

// collectedStorage is getStorage as it answered before #1409 streamed its
// lists: the same checks, then the file list, a folder's info and its last
// modified item from below's items, collected whole, and the file list
// sorted by render. The streamed views must answer its bytes.
func (s ArtifactoryAPI) collectedStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, p, q, err := s.storageRequest(r, collectedPrefix)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	mode, err := storageMode(q)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if !v.acc.Folder(v.repo, v.path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	switch {
	case mode == "list" && p.Anonymous,
		mode == "lastModified" && !v.acc.Folder(v.repo, v.path, accessapi.ActionWrite),
		mode == "permissions" && !p.Admin:
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if mode == "list" && v.repo.Class != repo.Virtual {
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
	var (
		media string
		body  any
	)
	switch mode {
	case "":
		media, body, err = v.collectedInfo(ctx, f)
	case "list":
		media, body, err = v.collectedList(ctx, f, q)
	case "lastModified":
		media, body, err = v.collectedLastModified(ctx, f)
	default:
		err = httpkit.BadRequestf("the collected views are info, list and lastModified")
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage "+mode, err))
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func (v storageView) collectedInfo(ctx context.Context, f itemkit.Found) (string, any, error) {
	body := v.infoBody(ctx, f.Item)
	if !f.Item.Folder {
		return mediaFileInfo, body, nil
	}
	children, err := v.below(ctx, f.Item.FullPath(), false)
	if err != nil {
		return "", nil, err
	}
	entries := make([]map[string]any, 0, len(children))
	for _, c := range children {
		entries = append(entries, map[string]any{"uri": "/" + c.Name, "folder": c.Folder})
	}
	body["children"] = entries
	return mediaFolderInfo, body, nil
}

func (v storageView) collectedList(
	ctx context.Context,
	f itemkit.Found,
	q url.Values,
) (string, any, error) {
	if !f.Item.Folder {
		return "", nil, httpkit.BadRequestf(
			"%s is a file; list needs a folder",
			f.Item.StoragePath(),
		)
	}
	l, err := parseListing(q)
	if err != nil {
		return "", nil, err
	}
	folder := f.Item.FullPath()
	items, err := v.below(ctx, folder, l.isDeep)
	if err != nil {
		return "", nil, err
	}
	files := make([]listed, 0, len(items))
	for _, it := range items {
		files = append(files, listed{path: it.FullPath(), item: it})
	}
	return mediaFileList, l.render(v.uri(ctx, folder), folder, f.Item, files), nil
}

func (v storageView) collectedLastModified(
	ctx context.Context,
	f itemkit.Found,
) (string, any, error) {
	newest := f.Item
	if f.Item.Folder {
		items, err := v.below(ctx, f.Item.FullPath(), true)
		if err != nil {
			return "", nil, err
		}
		for _, it := range items {
			if it.Modified.After(newest.Modified) {
				newest = it
			}
		}
		if newest.Modified.IsZero() {
			return "", nil, itemkit.ItemNotFound()
		}
	}
	return mediaLastModified, map[string]any{
		"uri":          v.uri(ctx, newest.FullPath()),
		"lastModified": httpkit.FormatTime(newest.Modified),
	}, nil
}

// below is what the views collected before #1409: what lies under folder in
// every tree the view reads, direct children or (deep) every descendant,
// tree by tree, in Children's or Descendants' order. A name that several
// members hold is reported once, from the first member the caller may read
// it in.
func (v storageView) below(ctx context.Context, folder string, deep bool) ([]model.Item, error) {
	trees, err := v.trees(ctx, v.repo, map[string]bool{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []model.Item
	for _, t := range trees {
		list := v.st.Items().Children
		if deep {
			list = v.st.Items().Descendants
		}
		page := kernel.Page{Limit: kernel.MaxLimit}
		for {
			batch, next, err := list(ctx, t.Repo.Key, t.Item.Cached, folder, page)
			if err != nil {
				return nil, fmt.Errorf("list %s/%s: %w", t.Repo.Key, folder, err)
			}
			for _, it := range batch {
				// What the caller may not see is left out; a folder shows
				// when something below it is readable.
				if !seen[it.FullPath()] &&
					v.acc.Item(t.Repo, it.FullPath(), it.Folder, accessapi.ActionRead) {
					seen[it.FullPath()] = true
					out = append(out, it)
				}
			}
			if next == "" {
				break
			}
			page.After = next
		}
	}
	return out, nil
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

// fileListRouter serves the storage and trash routes over db, and
// collectedStorage and collectedTrash under collectedPrefix and
// collectedTrashPrefix, to callers who send Basic credentials.
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
	s.RegisterTrashRoutes(mux)
	mux.Get(collectedPrefix+"*", s.collectedStorage)
	mux.Get(collectedTrashPrefix, s.collectedTrash)
	mux.Get(collectedTrashPrefix+"/*", s.collectedTrash)
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
	"a/0", "a/b+c", "a/~", "z.txt", "-.txt", "A.txt", "é.txt", "-d/f.txt",
}

// seedFileLists creates libs-local and libs-other (local), remote (whose
// cache holds files), libs (virtual over libs-local, remote, libs-other and
// a missing member) and outer (virtual over libs, libs-local and
// libs-other, which the walk visits once each). Paths in both locals carry
// different content, so the entry shows whose item is listed. A file was
// last modified 0 to 3 hours after its folders, by the length of its path,
// so many files tie for a folder's newest. pr reads and deploys a/** and
// wide/f0* of libs-local, a/b/** and wide/** of libs-other, and every path
// of remote and of the virtual repositories.
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
		{Key: "tie-a", Class: repo.Local},
		{Key: "tie-b", Class: repo.Local},
		{Key: "tie", Class: repo.Virtual, Repositories: []string{"tie-a", "tie-b"}},
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
				Modified: modified.Add(time.Duration(len(full)%4) * time.Hour),
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
		for _, full := range []string{"a/b-c", "a/b/c/x.txt", "a/b/d.txt", "other/x", "-d/g.txt"} {
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
	seedTies(t, db, modified)
	if err := db.Users().
		Create(ctx, accesstest.User{Name: "pr", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "pr-lists", Resources: map[accessapi.ResourceType]accesstest.Resource{
			accessapi.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]accessapi.Actions{
						"pr": accessapi.ActionRead | accessapi.ActionWrite,
					},
				},
				Targets: map[string]accesstest.Target{
					"libs-local": {Include: []string{"a/**", "wide/f0*"}},
					"libs-other": {Include: []string{"a/b/**", "wide/**"}},
					"remote":     {Include: []string{"**"}},
					"libs":       {Include: []string{"**"}},
					"outer":      {Include: []string{"**"}},
					"tie-a":      {Include: []string{"x/**", "y/**"}},
					"tie-b":      {Include: []string{"**"}},
					"tie":        {Include: []string{"**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// seedTies fills tie-a and tie-b, the members of the virtual tie, with
// files that tie for their folder's newest, where the order the collected
// last-modified read met them in (tree, then (path, name)) is not their
// full path order: -d/f before !x at the root, y/b/d before y/b-c/e, and
// tie-a's x/z.bin before tie-b's x/a.bin.
func seedTies(t *testing.T, db serverStore, at time.Time) {
	t.Helper()
	ctx := t.Context()
	newest := at.Add(5 * time.Hour)
	for _, it := range []model.Item{
		{RepoKey: "tie-a", Path: ".", Name: "-d", Folder: true, Modified: at},
		{RepoKey: "tie-a", Path: ".", Name: "x", Folder: true, Modified: at},
		{RepoKey: "tie-a", Path: ".", Name: "y", Folder: true, Modified: at},
		{RepoKey: "tie-a", Path: "y", Name: "b", Folder: true, Modified: at},
		{RepoKey: "tie-a", Path: "y", Name: "b-c", Folder: true, Modified: at},
		{RepoKey: "tie-a", Path: "-d", Name: "f", Modified: newest},
		{RepoKey: "tie-a", Path: ".", Name: "!x", Modified: newest},
		{RepoKey: "tie-a", Path: "x", Name: "z.bin", Modified: newest},
		{RepoKey: "tie-a", Path: "x", Name: "b.bin", Modified: at.Add(time.Hour)},
		{RepoKey: "tie-a", Path: "y/b", Name: "d", Modified: newest},
		{RepoKey: "tie-a", Path: "y/b-c", Name: "e", Modified: newest},
		{RepoKey: "tie-b", Path: ".", Name: "x", Folder: true, Modified: at},
		{RepoKey: "tie-b", Path: ".", Name: "y", Folder: true, Modified: at},
		{RepoKey: "tie-b", Path: ".", Name: "q.txt", Modified: newest},
		{RepoKey: "tie-b", Path: "x", Name: "a.bin", Modified: newest},
		{RepoKey: "tie-b", Path: "y", Name: "b", Modified: at.Add(2 * time.Hour)},
	} {
		if _, err := db.Items().Put(ctx, it); err != nil {
			t.Fatal(err)
		}
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

// TestAccStorageFolderViewsStreamTheCollected: on every backend, a
// folder's info, whose children are written as they are read, and its last
// modified item, folded over the merged walk, answer the collected views'
// bytes for local, remote and virtual repositories, the root, nested and
// missing folders and files, for the administrator and a partial reader.
// A virtual repository lists its members' children member by member, not
// merged by name; and among equally new items the one the collected read
// met first is reported.
func TestAccStorageFolderViewsStreamTheCollected(t *testing.T) {
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			db := fileListStore(t, backend)
			seedFileLists(t, db)
			h := fileListRouter(t, db)
			children := 0
			for _, key := range []string{
				"libs-local", "libs-other", "remote", "remote-cache", "libs", "outer",
				"tie-a", "tie-b", "tie",
			} {
				for _, item := range []string{
					"", "a", "a/b", "a/b/c", "deep", "deep/d00", "wide", "-d", "other", "none",
					"z.txt", "a/b-c", "x", "y",
				} {
					for _, view := range []string{"", "?lastModified"} {
						for _, user := range []string{"admin", "pr"} {
							target := key + "/" + item + view
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
								got.Body.String() != want.Body.String() {
								t.Fatalf(
									"%s as %s: %d %s, collected %d %s\n got %.600s\nwant %.600s",
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
							if view == "" && got.Code == http.StatusOK {
								children = max(
									children,
									strings.Count(got.Body.String(), `"uri":"/`),
								)
							}
						}
					}
				}
			}
			// The widest folder's children cross several store pages.
			if children < 2*kernel.MaxLimit {
				t.Fatalf("the most children listed: %d", children)
			}
		})
	}
}

// fileListFaults fails the item store's walks of a folder, by full path,
// by (path, name) and by name alike, once skip walks have been let
// through.
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

func (f faultyFileWalks) Children(
	ctx context.Context, repoKey string, cached bool, folder string, p kernel.Page,
) ([]model.Item, string, error) {
	if f.fail() {
		return nil, "", errBoom
	}
	return f.ItemStore.Children(ctx, repoKey, cached, folder, p) //nolint:wrapcheck // test double
}

// cutShort gets target from a real server and requires a 200 whose body
// begins with prefix and holds more, then breaks off unparsed.
func cutShort(t *testing.T, h http.Handler, target, prefix string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+target, nil)
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
		t.Fatalf("failure after the first byte of %s: %d, %d bytes %.80q, read error %v",
			target, res.StatusCode, len(body), body, err)
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		t.Fatalf("the cut body of %s parses as JSON", target)
	}
}

// TestAccStorageFolderViewsStoreFaults: a store failure before a folder's
// info has written its first byte, or anywhere in a last-modified walk,
// answers the collected view's error; one after the first page of children
// has gone out aborts the info, cut short.
func TestAccStorageFolderViewsStoreFaults(t *testing.T) {
	db := fileListStore(t, "inmem")
	seedFileLists(t, db)
	skip := 0
	h := fileListRouter(t, fileListFaults{serverStore: db, skip: &skip})
	// wide's children and descendants cross a store page: the second read
	// fails after the first is let through.
	for _, c := range []struct {
		target string
		skip   int
	}{
		{"libs-local/wide", 0},
		{"libs/a", 0},
		{"libs/a", 1},
		{"libs-local/wide?lastModified", 0},
		{"libs-local/wide?lastModified", 1},
		{"libs/?lastModified", 2},
	} {
		skip = c.skip
		got := do(t, h, http.MethodGet, storagePrefix+c.target, basic("admin", "pw"), "")
		skip = c.skip
		was := do(t, h, http.MethodGet, collectedPrefix+c.target, basic("admin", "pw"), "")
		if got.Code != http.StatusInternalServerError || got.Body.String() != was.Body.String() ||
			got.Header().Get("Content-Type") != was.Header().Get("Content-Type") {
			t.Fatalf("%s failing after %d reads: %d %s, collected %d %s",
				c.target, c.skip, got.Code, got.Body, was.Code, was.Body)
		}
	}
	skip = 1
	cutShort(t, h, storagePrefix+"libs-local/wide", `{"children":[{`)
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
