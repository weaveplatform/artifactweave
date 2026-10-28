package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// restoredMessage is the plain-text body of a restore, as documented.
const restoredMessage = "Successfully restored trash items"

// registerTrashRoutes serves what the generated routes cannot: trash paths
// that span segments (the generated clean and restore routes match one
// segment, and answer that case with the same functions), the trash can's
// storage listing under its auto-trashcan key, and zap. The patterns are the
// ones registerHandwrittenStubs derives, so they replace its stubs.
func (s artifactoryService) registerTrashRoutes(r chi.Router) {
	r.Delete("/artifactory/api/trash/clean/*", s.cleanTrashPath)
	r.Post("/artifactory/api/trash/restore/*", s.restoreTrashPath)
	r.Get(storagePrefix+trash.Repository, s.trashStorage)
	r.Get(storagePrefix+trash.Repository+"/*", s.trashStorage)
	for _, p := range chiPatterns("/artifactory/api/zap/{repoPath}") {
		r.Post(p, s.zapCache)
	}
}

func (s artifactoryService) trashService(st store.Store) *trash.Service {
	return &trash.Service{Store: st, Items: s.items(st)}
}

// trashRefusalf refuses a trash operation. Artifactory reports a signed-in
// caller without the right as 400, not 403, as the spec documents; an
// anonymous caller is told to authenticate.
func trashRefusalf(p auth.Principal, format string, a ...any) error {
	if p.Anonymous {
		return auth.ErrUnauthenticated
	}
	return badRequestf(format, a...)
}

// EmptyTrashCan purges every trashed item. Admin only; the blobs are
// reclaimed by the next garbage collection.
func (s artifactoryService) EmptyTrashCan(
	ctx context.Context,
	_ artgen.EmptyTrashCanRequestObject,
) (artgen.EmptyTrashCanResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Admin {
		return nil, trashRefusalf(p, "Only an administrator can empty the trash can")
	}
	if _, err := s.trashService(st).Empty(ctx); err != nil {
		return nil, s.internalError(ctx, "empty trash", err)
	}
	return artgen.EmptyTrashCan202Response{}, nil
}

// DeleteItemFromTrashCan is the one-segment form of the clean route.
func (s artifactoryService) DeleteItemFromTrashCan(
	ctx context.Context,
	req artgen.DeleteItemFromTrashCanRequestObject,
) (artgen.DeleteItemFromTrashCanResponseObject, error) {
	if err := s.cleanTrash(ctx, req.RepoPath); err != nil {
		return nil, err
	}
	return artgen.DeleteItemFromTrashCan204Response{}, nil
}

func (s artifactoryService) cleanTrashPath(w http.ResponseWriter, r *http.Request) {
	rp := strings.TrimPrefix(r.URL.Path, "/artifactory/api/trash/clean/")
	if err := s.cleanTrash(r.Context(), rp); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cleanTrash purges what was trashed at repoPath. The caller needs delete or
// manage on the original repository; once that repository is gone only an
// admin can. Cleaning a path with nothing trashed succeeds: what the caller
// wanted gone is gone.
func (s artifactoryService) cleanTrash(ctx context.Context, repoPath string) error {
	st, err := s.content()
	if err != nil {
		return err
	}
	key, rest := cutRepoPath(repoPath)
	if key == "" {
		return badRequestf("a trash path starts with the original repository key")
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return err
	}
	if !p.Admin {
		c, err := st.Repos().Get(ctx, key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return trashRefusalf(p, "Only an administrator can clean trash of a deleted repository")
		case err != nil:
			return s.internalError(ctx, "get repository", err)
		case !acc.removes(c, rest):
			return trashRefusalf(p, "Delete or manage permission on '%s' is required", key)
		}
	}
	if _, err := s.trashService(st).Clean(ctx, key, rest); err != nil {
		return s.internalError(ctx, "clean trash", err)
	}
	return nil
}

// RestoreItemFromTrashCan is the one-segment form of the restore route.
func (s artifactoryService) RestoreItemFromTrashCan(
	ctx context.Context,
	req artgen.RestoreItemFromTrashCanRequestObject,
) (artgen.RestoreItemFromTrashCanResponseObject, error) {
	if err := s.restoreTrash(
		ctx,
		req.FromPath,
		req.Params.To,
		req.Params.TransactionSize,
	); err != nil {
		return nil, err
	}
	return artgen.RestoreItemFromTrashCan202TextResponse(restoredMessage), nil
}

func (s artifactoryService) restoreTrashPath(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimPrefix(r.URL.Path, "/artifactory/api/trash/restore/")
	q := r.URL.Query()
	var size *int
	if v := q.Get("transaction-size"); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err != nil || n < 1 {
			writeErr(w, r, badRequestf("transaction-size must be a positive integer"))
			return
		}
		size = &n
	}
	if err := s.restoreTrash(r.Context(), from, q.Get("to"), size); err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(restoredMessage))
}

// restoreTrash moves what was trashed at from into to, a repository key
// with an optional path. The destination must be a local repository, and
// the caller needs manage permission on it, as documented. The caller also
// needs delete or manage on the original repository, as for clean, at from
// and at every restored item's original path: checking the destination
// alone let manage on one repository take another repository's trashed files
// and read them (#380). The check at from refuses before the trash is read,
// so a refusal never tells the caller whether anything is trashed there.
// Once the original repository is gone only an admin can restore from it.
func (s artifactoryService) restoreTrash(
	ctx context.Context,
	from, to string,
	size *int,
) error {
	st, err := s.content()
	if err != nil {
		return err
	}
	fromKey, fromPath := cutRepoPath(from)
	toKey, toPath := cutRepoPath(to)
	if fromKey == "" {
		return badRequestf("a trash path starts with the original repository key")
	}
	if toKey == "" {
		return badRequestf("the 'to' parameter names the destination repository")
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return err
	}
	dst, err := st.Repos().Get(ctx, toKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return badRequestf("Destination repository '%s' does not exist", toKey)
	case err != nil:
		return s.internalError(ctx, "get repository", err)
	case dst.Class != repo.Local && dst.Class != repo.Federated:
		return badRequestf("Items can be restored only into a local repository; '%s' is %s",
			toKey, dst.Class)
	case !acc.file(dst, toPath, types.ActionManage):
		return trashRefusalf(p, "Manage permission on '%s' is required", toKey)
	}
	var permit func(types.Item) bool
	var nativeMetadataRelocation, modelRelocation bool
	if !p.Admin {
		origin, err := st.Repos().Get(ctx, fromKey)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return trashRefusalf(
				p,
				"Only an administrator can restore trash of a deleted repository",
			)
		case err != nil:
			return s.internalError(ctx, "get repository", err)
		case !acc.removes(origin, fromPath):
			return trashRefusalf(p, "Delete or manage permission on '%s' is required",
				strings.Trim(from, "/"))
		}
		// An exclude pattern, or an include that admits from but not what
		// lies below it, can still refuse an item the check above allowed.
		permit = func(it types.Item) bool {
			if !acc.removes(origin, it.FullPath()) {
				return false
			}
			// Restored native metadata bytes must remain behind the native filtering route.
			// Restore preflights the entire selection before its first batch.
			if (condaShardItem(origin, it.FullPath()) || chefMetadataItem(origin, it.FullPath())) &&
				(toKey != fromKey || toPath != "" && toPath != fromPath) {
				nativeMetadataRelocation = true
				return false
			}
			if machineLearningPrivate(origin, it.FullPath()) &&
				(toKey != fromKey || toPath != "" && toPath != fromPath) {
				modelRelocation = true
				return false
			}
			return true
		}
	}
	svc := s.trashService(st)
	if size != nil {
		svc.Batch = *size
	}
	_, err = svc.Restore(ctx, fromKey, fromPath,
		trash.Destination{Repo: dst, Path: toPath}, p.Name, permit)
	switch {
	case errors.Is(err, trash.ErrNotPermitted):
		if modelRelocation {
			return trashRefusalf(
				p,
				"Private model metadata must be restored to its original repository and paths",
			)
		}
		if nativeMetadataRelocation {
			return trashRefusalf(
				p,
				"Native metadata must be restored to its original repository and paths",
			)
		}
		return trashRefusalf(p,
			"Delete or manage permission on everything trashed at '%s' is required",
			strings.Trim(from, "/"))
	case errors.Is(err, trash.ErrNothingTrashed):
		return apierror.New(http.StatusNotFound, fmt.Sprintf("Nothing is trashed at '%s'", from))
	case errors.Is(err, item.ErrConflict), errors.Is(err, types.ErrInvalidItemPath):
		return apierror.New(http.StatusConflict, err.Error())
	case err != nil:
		return s.internalError(ctx, "restore trash", err)
	}
	return nil
}

// removes reports whether the caller may take what lies at path in c out
// of the trash can: delete or manage there. Manage is accepted on its own
// because a permission can grant it without delete.
func (a access) removes(c repo.Config, path string) bool {
	return a.file(c, path, types.ActionDelete) || a.file(c, path, types.ActionManage)
}

// seesTrash reports whether the caller sees what was trashed from path in c:
// deploy or manage there, not read, as 7.x shows the trash can. Manage is
// accepted on its own because a permission can grant it without deploy.
func (a access) seesTrash(c repo.Config, path string, folder bool) bool {
	return a.item(c, path, folder, types.ActionWrite) ||
		a.item(c, path, folder, types.ActionManage)
}

// cutRepoPath splits "key/some/path" into the key and the path, as given.
func cutRepoPath(rp string) (key, rest string) {
	key, rest, _ = strings.Cut(strings.Trim(rp, "/"), "/")
	return key, rest
}

// trashStorage answers the storage API for the auto-trashcan repository
// with the views it serves for any other repository (#455): its root holds
// a folder per repository with trashed items, below which lie the trashed
// paths. An administrator sees all of it; anyone else what trashSight
// allows, in every view.
//
// While the trash can is off, every view answers 404, root included and to
// an administrator too, as a path that is not there does: 7.x fixed its
// trashed files being reachable through the native browser while the trash
// can was off (RTDEV-97807, SaaS 7.170.0). The check runs before the caller's
// rights are weighed, as a repository that does not exist answers 404 before
// them. What the trash can holds is kept, for restore, clean and empty.
func (s artifactoryService) trashStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q, err := rawQuery(r.URL.RawQuery)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := principal(ctx); err != nil {
		writeErr(w, r, err)
		return
	}
	switch cfg, err := trash.Settings(ctx, st); {
	case err != nil:
		writeErr(w, r, s.internalError(ctx, "trash can settings", err))
		return
	case !cfg.Enabled:
		writeErr(w, r, itemNotFound())
		return
	}
	mode, err := storageMode(q)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	dir := strings.Trim(strings.TrimPrefix(
		strings.TrimPrefix(r.URL.Path, storagePrefix+trash.Repository), "/"), "/")
	key, rest := cutRepoPath(dir)
	p, sees, err := s.trashSight(ctx, st, key, rest)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if mode == "permissions" && !p.Admin {
		writeErr(w, r, denied(p))
		return
	}
	v := trashView{
		svc: s.trashService(st), dir: dir, sees: sees,
		base: baseurl.From(ctx) + storagePrefix + trash.Repository,
	}
	n, children, err := v.target(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, r, itemNotFound())
		return
	case err != nil:
		writeErr(w, r, s.internalError(ctx, "find trash", err))
		return
	}
	// The folder check let the path through; an item needs the exact right,
	// as the storage API asks once it has found the item.
	if n.Entry != nil && sees != nil && !sees(n.Entry.Record.Item) {
		writeErr(w, r, denied(p))
		return
	}
	media, body, err := v.view(ctx, mode, n, children, q)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "trash "+mode, err))
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// trashView is one storage request on the trash can: the path asked for,
// below the auto-trashcan key, and what the caller sees.
type trashView struct {
	svc  *trash.Service
	dir  string
	sees func(types.Item) bool
	base string
}

// uri is the storage API URL of p, a path in the trash can ("" for its root).
func (v trashView) uri(p string) string {
	if p == "" {
		return v.base
	}
	return v.base + "/" + escapePath(p)
}

// target finds what the trash can holds at the path, with what it holds
// directly below: the root, which is always there, a trashed item, or a
// folder holding what the caller sees was trashed below it. Anything else is
// store.ErrNotFound.
func (v trashView) target(ctx context.Context) (trash.Node, []trash.Node, error) {
	n := trash.Node{Path: v.dir, Folder: true}
	children, err := v.svc.Below(ctx, v.dir, false, v.sees)
	if err != nil {
		return n, nil, err //nolint:wrapcheck // the trash service wraps its own
	}
	key, rest := cutRepoPath(v.dir)
	if rest == "" {
		if key != "" && len(children) == 0 {
			return n, nil, store.ErrNotFound
		}
		return n, children, nil
	}
	e, err := v.svc.Find(ctx, key, rest)
	switch {
	case errors.Is(err, store.ErrNotFound) && len(children) == 0:
		return n, nil, store.ErrNotFound
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return n, nil, err //nolint:wrapcheck // as above
	// A file trashed where a folder later held trashed items gives way to
	// the folder, as in Below.
	case len(children) == 0 || e.Record.Item.Folder:
		n.Entry, n.Folder = &e, e.Record.Item.Folder
	}
	return n, children, nil
}

// view renders the view mode selects, as getStorage does for a repository.
func (v trashView) view(
	ctx context.Context,
	mode string,
	n trash.Node,
	children []trash.Node,
	q url.Values,
) (string, any, error) {
	switch mode {
	case "list":
		return v.list(ctx, n, q)
	case "properties":
		return v.properties(n, q.Get("properties"))
	case "stats":
		return v.stats(n)
	case "lastModified":
		return v.lastModified(ctx, n)
	case "permissions":
		// No permission target can name the trash can (RTFACT-30167): who
		// sees a trashed item follows from the repository it was deleted
		// from, which this view does not describe.
		return "", nil, badRequestf("the permissions view does not apply to the %s repository",
			trash.Repository)
	}
	media, body := v.info(n, children)
	return media, body, nil
}

// trashItem is how a node of the trash can's tree shows in a view: the
// trashed item as recorded, whose properties last changed when it was
// trashed, as that set its trash.* properties. A folder only implied by
// what was trashed below it has no record, and so no times.
func trashItem(n trash.Node) types.Item {
	if n.Entry == nil {
		return types.Item{Folder: n.Folder}
	}
	it := n.Entry.Record.Item
	it.Folder, it.PropertiesModified = n.Folder, n.Entry.DeletedAt
	return it
}

// info is the item info view: a trashed file's FileInfo, or a folder's
// children.
func (v trashView) info(n trash.Node, children []trash.Node) (string, any) {
	if !n.Folder {
		return mediaFileInfo, trashFileInfo(v.uri(v.dir), v.dir, *n.Entry)
	}
	list := make([]map[string]any, 0, len(children))
	for _, c := range children {
		list = append(list, map[string]any{"uri": "/" + path.Base(c.Path), "folder": c.Folder})
	}
	return mediaFolderInfo, map[string]any{
		"repo": trash.Repository, "path": "/" + v.dir, "children": list, "uri": v.uri(v.dir),
	}
}

// list is the FileList view of a trash can folder, with the parameters it
// takes on any other repository.
func (v trashView) list(ctx context.Context, n trash.Node, q url.Values) (string, any, error) {
	if !n.Folder {
		return "", nil, badRequestf("/%s is a file; list needs a folder", v.dir)
	}
	l, err := parseListing(q)
	if err != nil {
		return "", nil, err
	}
	nodes, err := v.svc.Below(ctx, v.dir, l.isDeep, v.sees)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // the trash service wraps its own
	}
	items := make([]listed, 0, len(nodes))
	for _, c := range nodes {
		items = append(items, listed{path: c.Path, item: trashItem(c)})
	}
	return mediaFileList, l.render(v.uri(v.dir), v.dir, trashItem(n), items), nil
}

// properties is the properties view of a trashed item: its own properties
// and the trash.* ones, as Artifactory keeps both on the item in the trash
// can (TrashServiceImpl.buildDeletedItemProperties in 6.20). A folder only
// implied by what was trashed below it has none.
func (v trashView) properties(n trash.Node, names string) (string, any, error) {
	if n.Entry == nil {
		return "", nil, noProperties()
	}
	props := types.Properties{}
	maps.Copy(props, n.Entry.Record.Properties)
	maps.Copy(props, trashProperties(*n.Entry))
	if names != "" {
		want := pkgroute.SplitValues(names)
		maps.DeleteFunc(props, func(k string, _ []string) bool { return !slices.Contains(want, k) })
	}
	if len(props) == 0 {
		return "", nil, noProperties()
	}
	return mediaProperties, map[string]any{"uri": v.uri(v.dir), "properties": props}, nil
}

// stats is the download statistics view of a trashed file. The trash can
// keeps none: Artifactory trashes a copy of the file, which nothing has
// downloaded.
func (v trashView) stats(n trash.Node) (string, any, error) {
	if n.Folder {
		return "", nil, badRequestf("/%s is a folder; statistics are kept for files", v.dir)
	}
	return mediaStats, map[string]any{
		"uri": v.uri(v.dir), "downloadCount": 0, "lastDownloaded": 0,
	}, nil
}

// lastModified is the last-modified view: a trashed file's modification
// time, or for a folder the newest item the caller sees below it.
func (v trashView) lastModified(ctx context.Context, n trash.Node) (string, any, error) {
	newest, at := trashItem(n), v.dir
	if n.Folder {
		nodes, err := v.svc.Below(ctx, v.dir, true, v.sees)
		if err != nil {
			return "", nil, err //nolint:wrapcheck // the trash service wraps its own
		}
		for _, c := range nodes {
			if it := trashItem(c); it.Modified.After(newest.Modified) {
				newest, at = it, c.Path
			}
		}
		if newest.Modified.IsZero() {
			return "", nil, itemNotFound()
		}
	}
	return mediaLastModified, map[string]any{
		"uri": v.uri(at), "lastModified": formatTime(newest.Modified),
	}, nil
}

// trashSight decides what a caller sees of the trash can at key/rest, as
// 7.x filters it from 7.41.4 (decision 0010): what was trashed from the
// repositories it holds deploy or manage on, judged at each item's original
// path so exclude patterns hold, and no trash can at all for a caller that
// holds neither anywhere. A path where it sees nothing is refused before
// the trash is read, so a refusal never tells it whether anything is
// trashed there.
//
// An administrator gets a nil filter. Only it sees the trash of a deleted
// repository, which has no configuration left to judge a permission by, as
// for clean and restore. Anonymous is refused even holding deploy, an
// inferred choice: the listing names who deleted each item.
func (s artifactoryService) trashSight(
	ctx context.Context,
	st store.Store,
	key, rest string,
) (auth.Principal, func(types.Item) bool, error) {
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil || p.Admin {
		return p, nil, err
	}
	if p.Anonymous {
		return p, nil, denied(p)
	}
	var origins []repo.Config
	if key == "" {
		// The root shows to a holder of deploy or manage anywhere, so every
		// repository is a candidate.
		origins, err = st.Repos().List(ctx, store.RepoFilter{})
		if err != nil {
			return p, nil, s.internalError(ctx, "list repositories", err)
		}
	} else {
		c, err := st.Repos().Get(ctx, key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return p, nil, denied(p)
		case err != nil:
			return p, nil, s.internalError(ctx, "get repository", err)
		}
		origins = []repo.Config{c}
	}
	byKey := make(map[string]repo.Config, len(origins))
	allowed := false
	for _, c := range origins {
		byKey[c.Key] = c
		// A folder shows when something below it does, so at the root this
		// asks whether the caller sees anything in the repository at all.
		allowed = allowed || acc.seesTrash(c, rest, true)
	}
	if !allowed {
		return p, nil, denied(p)
	}
	return p, func(it types.Item) bool {
		c, ok := byKey[it.RepoKey]
		return ok && acc.seesTrash(c, it.FullPath(), it.Folder)
	}, nil
}

// trashFileInfo is a trashed file's storage info, with when and by whom it
// was deleted, as Artifactory records them in trash.* properties.
func trashFileInfo(uri, dir string, e types.TrashEntry) map[string]any {
	it := e.Record.Item
	return map[string]any{
		"repo": trash.Repository, "path": "/" + dir,
		"created": formatTime(it.Created), "createdBy": it.CreatedBy,
		"lastModified": formatTime(it.Modified), "modifiedBy": it.ModifiedBy,
		"lastUpdated": formatTime(it.Updated), "mimeType": it.MimeType,
		"size": fmt.Sprint(it.Size), "checksums": checksumsJSON(it.Actual),
		"originalChecksums": checksumsJSON(it.Original),
		"properties":        trashProperties(e),
		"uri":               uri,
	}
}

// trashProperties are the properties Artifactory sets on an item it moves
// to the trash can, with 6.20's values (TrashServiceImpl
// .buildDeletedItemProperties). The time is in Unix milliseconds, as 7.x
// keeps it too: RTFACT-29920 compares trash.time with an epoch in
// milliseconds. The repository type is RepoType.getType(), the enumeration
// name in lower case, which is yum for rpm.
func trashProperties(e types.TrashEntry) types.Properties {
	it := e.Record.Item
	props := types.Properties{
		"trash.time":               {strconv.FormatInt(e.DeletedAt.UnixMilli(), 10)},
		"trash.deletedBy":          {e.DeletedBy},
		"trash.originalRepository": {it.RepoKey},
		"trash.originalPath":       {it.FullPath()},
	}
	if t := e.Record.PackageType; t != "" {
		props["trash.originalRepositoryType"] = []string{strings.ToLower(repo.PackageTypeName(t))}
	}
	return props
}

// RunGarbageCollection runs a pass and answers when it is done. Admin only.
// A pass another instance is running counts as done: it collects the same
// store.
func (s artifactoryService) RunGarbageCollection(
	ctx context.Context,
	_ artgen.RunGarbageCollectionRequestObject,
) (artgen.RunGarbageCollectionResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	c, err := s.collector()
	if err != nil {
		return nil, err
	}
	if _, err := c.Run(ctx); err != nil {
		return nil, s.internalError(ctx, "garbage collection", err)
	}
	return artgen.RunGarbageCollection200Response{}, nil
}

func (s artifactoryService) collector() (*gc.Collector, error) {
	if s.cfg.GC != nil {
		return s.cfg.GC, nil
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	c, err := gc.New(st, s.cfg.Blobs, gc.Options{})
	if err != nil {
		return nil, fmt.Errorf("garbage collector: %w", err)
	}
	return c, nil
}

// zapCache marks a remote cache's items at a path as expired. The path must
// use the <key>-cache alias; the caller needs delete or manage on the remote
// repository.
func (s artifactoryService) zapCache(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rp := strings.Trim(strings.TrimPrefix(r.URL.Path, "/artifactory/api/zap/"), "/")
	key, rest := cutRepoPath(rp)
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, key)
	switch {
	case err != nil:
		writeErr(w, r, s.itemError(ctx, "resolve zap target", err))
		return
	case !cached:
		writeErr(w, r, badRequestf("Zap applies to a remote repository's cache: use '%s'",
			key+types.CacheSuffix))
		return
	case !acc.file(c, rest, types.ActionDelete) && !acc.file(c, rest, types.ActionManage):
		writeErr(w, r, denied(p))
		return
	}
	if _, err := s.items(st).Zap(ctx, item.Target{Repo: c, Cached: true, Path: rest}); err != nil {
		writeErr(w, r, s.itemError(ctx, "zap", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"Message": "Completed zapping cache in path " + rp,
	})
}
