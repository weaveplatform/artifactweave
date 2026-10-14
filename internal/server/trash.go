package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
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
	r.Get("/artifactory/api/storage/"+trash.Repository, s.listTrash)
	r.Get("/artifactory/api/storage/"+trash.Repository+"/*", s.listTrash)
	for _, p := range chiPatterns("/artifactory/api/zap/{repoPath}") {
		r.Post(p, s.zapCache)
	}
}

func (s artifactoryService) trashService(st store.Store) *trash.Service {
	return &trash.Service{Store: st, Items: s.items(st)}
}

// trashRefusal refuses a trash operation. Artifactory reports a signed-in
// caller without the right as 400, not 403, as the spec documents; an
// anonymous caller is told to authenticate.
func trashRefusal(p auth.Principal, format string, a ...any) error {
	if p.Anonymous {
		return auth.ErrUnauthenticated
	}
	return badRequest(format, a...)
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
		return nil, trashRefusal(p, "Only an administrator can empty the trash can")
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
		return badRequest("a trash path starts with the original repository key")
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return err
	}
	if !p.Admin {
		c, err := st.Repos().Get(ctx, key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return trashRefusal(p, "Only an administrator can clean trash of a deleted repository")
		case err != nil:
			return s.internalError(ctx, "get repository", err)
		case !acc.file(c, rest, types.ActionDelete) && !acc.file(c, rest, types.ActionManage):
			return trashRefusal(p, "Delete or manage permission on '%s' is required", key)
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
			writeErr(w, r, badRequest("transaction-size must be a positive integer"))
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
// the caller needs manage permission on it, as documented.
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
		return badRequest("a trash path starts with the original repository key")
	}
	if toKey == "" {
		return badRequest("the 'to' parameter names the destination repository")
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return err
	}
	dst, err := st.Repos().Get(ctx, toKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return badRequest("Destination repository '%s' does not exist", toKey)
	case err != nil:
		return s.internalError(ctx, "get repository", err)
	case dst.Class != repo.Local && dst.Class != repo.Federated:
		return badRequest("Items can be restored only into a local repository; '%s' is %s",
			toKey, dst.Class)
	case !acc.file(dst, toPath, types.ActionManage):
		return trashRefusal(p, "Manage permission on '%s' is required", toKey)
	}
	svc := s.trashService(st)
	if size != nil {
		svc.Batch = *size
	}
	_, err = svc.Restore(ctx, fromKey, fromPath, trash.Destination{Repo: dst, Path: toPath}, p.Name)
	switch {
	case errors.Is(err, trash.ErrNothingTrashed):
		return apierror.New(http.StatusNotFound, fmt.Sprintf("Nothing is trashed at '%s'", from))
	case errors.Is(err, item.ErrConflict), errors.Is(err, types.ErrInvalidItemPath):
		return apierror.New(http.StatusConflict, err.Error())
	case err != nil:
		return s.internalError(ctx, "restore trash", err)
	}
	return nil
}

// cutRepoPath splits "key/some/path" into the key and the path, as given.
func cutRepoPath(rp string) (key, rest string) {
	key, rest, _ = strings.Cut(strings.Trim(rp, "/"), "/")
	return key, rest
}

// listTrash answers the storage API for the auto-trashcan repository: its
// root lists the repositories with trashed items, a folder its next path
// segments, and a trashed file its file info. Admin only.
func (s artifactoryService) listTrash(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err == nil {
		err = adminOnly(ctx)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	dir := strings.Trim(strings.TrimPrefix(
		strings.TrimPrefix(r.URL.Path, "/artifactory/api/storage/"+trash.Repository), "/"), "/")
	svc := s.trashService(st)
	key, rest := cutRepoPath(dir)
	var entry types.TrashEntry
	found := false
	if rest != "" {
		entry, err = svc.Find(ctx, key, rest)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, store.ErrNotFound):
			writeErr(w, r, s.internalError(ctx, "find trash", err))
			return
		}
	}
	base := baseurl.From(ctx) + "/artifactory/api/storage/" + trash.Repository
	if found && !entry.Record.Item.Folder {
		writeJSON(w, http.StatusOK, trashFileInfo(base, dir, entry))
		return
	}
	children, err := svc.Children(ctx, dir)
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "list trash", err))
		return
	}
	if dir != "" && !found && len(children) == 0 {
		writeErr(w, r, itemNotFound())
		return
	}
	list := make([]map[string]any, 0, len(children))
	for _, c := range children {
		list = append(list, map[string]any{"uri": "/" + c.Name, "folder": c.Folder})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repo": trash.Repository, "path": "/" + dir, "children": list,
		"uri": strings.TrimSuffix(base+"/"+escapePath(dir), "/"),
	})
}

// trashFileInfo is a trashed file's storage info, with when and by whom it
// was deleted, as Artifactory records them in trash.* properties.
func trashFileInfo(base, dir string, e types.TrashEntry) map[string]any {
	it := e.Record.Item
	return map[string]any{
		"repo": trash.Repository, "path": "/" + dir,
		"created": formatTime(it.Created), "createdBy": it.CreatedBy,
		"lastModified": formatTime(it.Modified), "modifiedBy": it.ModifiedBy,
		"lastUpdated": formatTime(it.Updated), "mimeType": it.MimeType,
		"size": fmt.Sprint(it.Size), "checksums": checksumsJSON(it.Actual),
		"originalChecksums": checksumsJSON(it.Original),
		"properties": map[string][]string{
			"trash.time":               {formatTime(e.DeletedAt)},
			"trash.deletedBy":          {e.DeletedBy},
			"trash.originalRepository": {it.RepoKey},
			"trash.originalPath":       {it.FullPath()},
		},
		"uri": base + "/" + escapePath(dir),
	}
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
		writeErr(w, r, badRequest("Zap applies to a remote repository's cache: use '%s'",
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
