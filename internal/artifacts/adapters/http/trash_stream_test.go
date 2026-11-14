package artifactshttp

// This file holds the tests of the trash can's storage views as they walk
// its tree a store page at a time (#1409, decision 0043 "Streaming
// complete lists"): that they answer, on every backend, the bytes of the
// views that collected the whole tree first, and how a store failure
// before and after the first byte ends the answer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/trash"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// collectedTrashPrefix is where fileListRouter serves collectedTrash.
const collectedTrashPrefix = "/collected-trash"

// collectedTrash is trashStorage as it answered before #1409 streamed its
// lists: the same checks, then every view from the tree collectedTrashBelow
// collects whole, and the file list sorted by render.
func (s ArtifactoryAPI) collectedTrash(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	q, err := rawQuery(r.URL.RawQuery)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if _, err := httpkit.Principal(ctx); err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	switch cfg, err := trash.Settings(ctx, st); {
	case err != nil:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "trash can settings", err))
		return
	case !cfg.Enabled:
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	}
	mode, err := storageMode(q)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	dir := strings.Trim(strings.TrimPrefix(
		strings.TrimPrefix(r.URL.Path, collectedTrashPrefix), "/"), "/")
	key, rest := cutRepoPath(dir)
	p, sees, err := s.trashSight(ctx, st, key, rest)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if mode == "permissions" && !p.Admin {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	v := trashView{
		svc: s.trashService(st), dir: dir, sees: sees,
		base: baseurl.From(ctx) + storagePrefix + trash.Repository,
	}
	n, children, err := v.collectedTarget(ctx)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		httpkit.WriteErr(w, r, itemkit.ItemNotFound())
		return
	case err != nil:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "find trash", err))
		return
	}
	if n.Entry != nil && sees != nil && !sees(n.Entry.Record.Item) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	media, body, err := v.collectedView(ctx, mode, n, children, q)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "trash "+mode, err))
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func (v trashView) collectedTarget(ctx context.Context) (trash.Node, []trash.Node, error) {
	n := trash.Node{Path: v.dir, Folder: true}
	children, err := collectedTrashBelow(ctx, v.svc, v.dir, false, v.sees)
	if err != nil {
		return n, nil, err
	}
	key, rest := cutRepoPath(v.dir)
	if rest == "" {
		if key != "" && len(children) == 0 {
			return n, nil, kernel.ErrNotFound
		}
		return n, children, nil
	}
	e, err := v.svc.Find(ctx, key, rest)
	switch {
	case errors.Is(err, kernel.ErrNotFound) && len(children) == 0:
		return n, nil, kernel.ErrNotFound
	case errors.Is(err, kernel.ErrNotFound):
	case err != nil:
		return n, nil, err //nolint:wrapcheck // as the view did
	case len(children) == 0 || e.Record.Item.Folder:
		n.Entry, n.Folder = &e, e.Record.Item.Folder
	}
	return n, children, nil
}

func (v trashView) collectedView(
	ctx context.Context,
	mode string,
	n trash.Node,
	children []trash.Node,
	q url.Values,
) (string, any, error) {
	switch mode {
	case "list":
		if !n.Folder {
			return "", nil, httpkit.BadRequestf("/%s is a file; list needs a folder", v.dir)
		}
		l, err := parseListing(q)
		if err != nil {
			return "", nil, err
		}
		nodes, err := collectedTrashBelow(ctx, v.svc, v.dir, l.isDeep, v.sees)
		if err != nil {
			return "", nil, err
		}
		items := make([]listed, 0, len(nodes))
		for _, c := range nodes {
			items = append(items, listed{path: c.Path, item: trashItem(c)})
		}
		return mediaFileList, l.render(v.uri(v.dir), v.dir, trashItem(n), items), nil
	case "lastModified":
		newest, at := trashItem(n), v.dir
		if n.Folder {
			nodes, err := collectedTrashBelow(ctx, v.svc, v.dir, true, v.sees)
			if err != nil {
				return "", nil, err
			}
			for _, c := range nodes {
				if it := trashItem(c); it.Modified.After(newest.Modified) {
					newest, at = it, c.Path
				}
			}
			if newest.Modified.IsZero() {
				return "", nil, itemkit.ItemNotFound()
			}
		}
		return mediaLastModified, map[string]any{
			"uri": v.uri(at), "lastModified": httpkit.FormatTime(newest.Modified),
		}, nil
	case "":
		if !n.Folder {
			return mediaFileInfo, trashFileInfo(v.uri(v.dir), v.dir, *n.Entry), nil
		}
		list := make([]map[string]any, 0, len(children))
		for _, c := range children {
			list = append(list, map[string]any{"uri": "/" + path.Base(c.Path), "folder": c.Folder})
		}
		return mediaFolderInfo, map[string]any{
			"repo": trash.Repository, "path": "/" + v.dir, "children": list, "uri": v.uri(v.dir),
		}, nil
	}
	return v.view(ctx, mode, n, q)
}

// collectedTrashBelow is trash.Service.Below as it was before it walked the
// store a page at a time: every entry below dir collected, filtered,
// reduced to the newest per path, and the tree derived in a map and sorted.
func collectedTrashBelow(
	ctx context.Context,
	s *trash.Service,
	dir string,
	deep bool,
	sees func(model.Item) bool,
) ([]trash.Node, error) {
	dir = strings.Trim(dir, "/")
	repoKey, prefix, _ := strings.Cut(dir, "/")
	entries, err := s.Entries(ctx, repoKey, prefix)
	if err != nil {
		return nil, err //nolint:wrapcheck // the trash service wraps its own
	}
	visible := make([]model.TrashEntry, 0, len(entries))
	for _, e := range entries {
		if sees == nil || sees(e.Record.Item) {
			visible = append(visible, e)
		}
	}
	nodes := map[string]*trash.Node{}
	node := func(p string) *trash.Node {
		n, ok := nodes[p]
		if !ok {
			n = &trash.Node{Path: p}
			nodes[p] = n
		}
		return n
	}
	for _, e := range trash.Newest(visible) {
		rel := strings.Trim(e.Record.Item.RepoKey+"/"+e.Record.Item.FullPath(), "/")
		if dir != "" {
			cut, ok := strings.CutPrefix(rel, dir+"/")
			if !ok {
				continue
			}
			rel = cut
		}
		segs := strings.Split(rel, "/")
		for i := 1; i < len(segs) && (deep || i == 1); i++ {
			node(path.Join(dir, path.Join(segs[:i]...))).Folder = true
		}
		if !deep && len(segs) > 1 {
			continue
		}
		n := node(path.Join(dir, rel))
		n.Entry, n.Folder = &e, n.Folder || e.Record.Item.Folder
	}
	out := make([]trash.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Folder && n.Entry != nil && !n.Entry.Record.Item.Folder {
			n.Entry = nil
		}
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b trash.Node) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// trashWide is how many files wide/ of libs-local held when it was
// trashed, and how many w-NNN siblings sort between w and w/: enough that
// the walk crosses store pages of kernel.MaxLimit.
const trashWide = 1100

// seedTrashLists trashes, from libs-local, libs-other and gone (a
// repository since deleted), paths whose byte order puts a sibling such as
// a/b-c between a folder a/b and its contents; a file a/b trashed before
// the folder a/b held trashed files; z trashed twice at one time; and
// wide/ and the w-NNN siblings of w/, each more than a store page. Items
// were last modified 0 to 3 hours after t0, by the length of their path.
func seedTrashLists(t *testing.T, db serverStore) {
	t.Helper()
	ctx := t.Context()
	t0 := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	deleted := t0.Add(24 * time.Hour)
	add := func(ctx context.Context, key, full string, folder bool, at time.Time) error {
		p, name := path.Split(full)
		_, err := db.Trash().Add(ctx, model.TrashEntry{
			Record: model.TrashRecord{
				Item: model.Item{
					RepoKey: key, Path: folderOf(p), Name: name, Folder: folder,
					Modified: t0.Add(time.Duration(len(full)%4) * time.Hour),
					Created:  t0, Updated: t0, CreatedBy: "admin", ModifiedBy: "admin",
				},
				Properties:  model.Properties{"k": {full}},
				PackageType: "generic",
			},
			DeletedAt: at, DeletedBy: "admin",
		})
		return err //nolint:wrapcheck // test seeding
	}
	err := db.Tx(ctx, func(ctx context.Context) error {
		var writes []func() error
		for i, full := range []string{
			"a/b", "a/b/c.jar", "a/b-c/d", "a/b.d", "a/b c/e", "a/b!", "a-1/x", "a.txt", "-d/f",
			"é/z", "z", "z", "w/x", "a/b/c/d/e.txt",
		} {
			writes = append(writes, func() error {
				return add(
					ctx,
					"libs-local",
					full,
					false,
					deleted.Add(time.Duration(i%3)*time.Minute),
				)
			})
		}
		writes = append(writes,
			func() error { return add(ctx, "libs-local", "a", true, deleted) },
			func() error { return add(ctx, "libs-other", "a/b/c.jar", false, deleted) },
			func() error { return add(ctx, "libs-other", "q/r", false, deleted) },
			func() error { return add(ctx, "gone", "x/y", false, deleted) },
		)
		for i := range trashWide {
			writes = append(
				writes,
				func() error {
					return add(ctx, "libs-local", fmt.Sprintf("wide/f%04d", i), false, deleted)
				},
				func() error { return add(ctx, "libs-local", fmt.Sprintf("w-%04d", i), false, deleted) },
			)
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
}

// TestAccTrashViewsStreamTheCollected: on every backend, the trash can's
// folder info, file list (deep and not, with each listing parameter) and
// last-modified view, at its root, a repository's folder, folders below
// and trashed files, answer the collected views' bytes, for the
// administrator and for a caller who sees part of the trash; and the long
// views cross several store pages.
func TestAccTrashViewsStreamTheCollected(t *testing.T) {
	for _, backend := range []string{"inmem", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			db := fileListStore(t, backend)
			seedFileLists(t, db)
			seedTrashLists(t, db)
			h := fileListRouter(t, db)
			longest := 0
			for _, dir := range []string{
				"", "/libs-local", "/libs-local/a", "/libs-local/a/b", "/libs-local/a/b-c",
				"/libs-local/wide", "/libs-local/z", "/libs-local/a/b/c.jar", "/libs-local/w",
				"/libs-other", "/gone", "/none", "/libs-local/none",
			} {
				for _, view := range []string{
					"", "?list", "?list&deep=1",
					"?list&deep=1&listFolders=1&includeRootPath=1&mdTimestamps=1",
					"?list&listFolders=1&includeRootPath=1", "?list&deep=1&depth=2&listFolders=1",
					"?lastModified", "?properties", "?stats",
				} {
					for _, user := range []string{"admin", "pr"} {
						target := dir + view
						got := do(t, h, http.MethodGet,
							storagePrefix+trash.Repository+target, basic(user, "pw"), "")
						want := do(t, h, http.MethodGet,
							collectedTrashPrefix+target, basic(user, "pw"), "")
						if got.Code != want.Code ||
							got.Header().Get("Content-Type") != want.Header().Get("Content-Type") ||
							created.ReplaceAllString(got.Body.String(), "") !=
								created.ReplaceAllString(want.Body.String(), "") {
							t.Fatalf("%s as %s: %d %s, collected %d %s\n got %.600s\nwant %.600s",
								target, user, got.Code, got.Header().Get("Content-Type"),
								want.Code, want.Header().Get("Content-Type"), got.Body, want.Body)
						}
						if got.Code == http.StatusOK {
							longest = max(longest, strings.Count(got.Body.String(), `"uri":"/`))
						}
					}
				}
			}
			if longest < 2*kernel.MaxLimit {
				t.Fatalf("the longest trash view listed %d entries", longest)
			}
		})
	}
}

// trashWalkFaults fails the trash store's walk by path once skip reads
// have been let through.
type trashWalkFaults struct {
	serverStore
	skip *int
}

func (f trashWalkFaults) Trash() port.TrashStore {
	return faultyTrashWalk{f.serverStore.Trash(), f.skip}
}

type faultyTrashWalk struct {
	port.TrashStore
	skip *int
}

func (f faultyTrashWalk) BelowByPath(
	ctx context.Context,
	dir string,
	p kernel.Page,
) ([]model.TrashEntry, string, error) {
	if *f.skip == 0 {
		return nil, "", errBoom
	}
	*f.skip--
	return f.TrashStore.BelowByPath(ctx, dir, p) //nolint:wrapcheck // test double
}

func (f faultyTrashWalk) List(
	ctx context.Context,
	repoKey, prefix string,
	p kernel.Page,
) ([]model.TrashEntry, string, error) {
	if *f.skip == 0 {
		return nil, "", errBoom
	}
	*f.skip--
	return f.TrashStore.List(ctx, repoKey, prefix, p) //nolint:wrapcheck // test double
}

// TestAccTrashViewsStoreFaults: a store failure before a trash view has
// written its first byte answers the collected view's error. Wherever a
// failure falls in a list or folder info, the answer is a 500 or a 200 cut
// short of the whole answer that no JSON parser accepts, never a shorter
// list that parses; some failures fall after the first byte.
func TestAccTrashViewsStoreFaults(t *testing.T) {
	db := fileListStore(t, "inmem")
	seedFileLists(t, db)
	seedTrashLists(t, db)
	skip := 0
	h := fileListRouter(t, trashWalkFaults{serverStore: db, skip: &skip})
	for _, target := range []string{
		"", "?list&deep=1", "?lastModified", "/libs-local/wide?list", "/libs-local/a",
	} {
		skip = 0
		got := do(
			t,
			h,
			http.MethodGet,
			storagePrefix+trash.Repository+target,
			basic("admin", "pw"),
			"",
		)
		skip = 0
		was := do(t, h, http.MethodGet, collectedTrashPrefix+target, basic("admin", "pw"), "")
		if got.Code != http.StatusInternalServerError || got.Body.String() != was.Body.String() ||
			got.Header().Get("Content-Type") != was.Header().Get("Content-Type") {
			t.Fatalf(
				"%s failing: %d %s, collected %d %s",
				target,
				got.Code,
				got.Body,
				was.Code,
				was.Body,
			)
		}
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, target := range []string{"?list&deep=1&listFolders=1", "/libs-local", "/libs-local?lastModified"} {
		skip = 1 << 20
		whole := created.ReplaceAllString(do(t, h, http.MethodGet,
			storagePrefix+trash.Repository+target, basic("admin", "pw"), "").Body.String(), "")
		cuts := 0
		for n := 0; ; n++ {
			skip = n
			code, body, readErr := fetch(t, srv, storagePrefix+trash.Repository+target)
			body = created.ReplaceAllString(body, "")
			if code == http.StatusOK && readErr == nil {
				if body != whole {
					t.Fatalf(
						"%s after %d reads: a whole answer that differs:\n%.300s",
						target,
						n,
						body,
					)
				}
				break
			}
			var v any
			switch {
			case code == http.StatusInternalServerError:
			case code == http.StatusOK && strings.HasPrefix(whole, body) && len(body) < len(whole) &&
				json.Unmarshal([]byte(body), &v) != nil:
				cuts++
			default:
				t.Fatalf("%s after %d reads: %d, %d bytes, %v", target, n, code, len(body), readErr)
			}
		}
		if wantCut := !strings.Contains(target, "lastModified"); wantCut != (cuts > 0) {
			t.Fatalf("%s: %d answers cut short", target, cuts)
		}
	}
}

// fetch gets target from a real server as the administrator and reads the
// whole body, or as much as arrives before the answer breaks off.
func fetch(t *testing.T, srv *httptest.Server, target string) (int, string, error) {
	t.Helper()
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
	return res.StatusCode, string(body), err
}
