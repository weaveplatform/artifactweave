package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// copyOrMoveType is the media type of a copy or move report.
const copyOrMoveType = "application/vnd.org.jfrog.artifactory.storage.CopyOrMoveResult+json"

// registerTransferRoutes serves copy, move, folder archive download and
// archive entries, all hand-written in the spec, under the patterns
// chiPatterns derives so they replace the 501 stubs. An archive entry in a
// nested folder does not fit the entry pattern (a chi parameter is one
// segment) and reaches getItem through the plain item route instead.
func (s artifactoryService) registerTransferRoutes(r chi.Router) {
	for _, p := range chiPatterns("/artifactory/api/copy/{srcRepoKey}/{srcFilePath}") {
		r.Post(p, s.copyItem)
	}
	for _, p := range chiPatterns("/artifactory/api/move/{srcRepoKey}/{srcFilePath}") {
		r.Post(p, s.moveItem)
	}
	for _, p := range chiPatterns("/artifactory/api/archive/download/{repoKey}/{path}") {
		r.Get(p, s.downloadFolderArchive)
	}
	for _, p := range chiPatterns("/artifactory/{repoKey}/{archivePath}!/{entryPath}") {
		r.Get(p, s.getItem)
		r.Head(p, s.getItem)
	}
}

// splitRepoPath splits an escaped "<repo>/<path>" into the key and the
// unescaped path; folder reports a trailing slash.
func splitRepoPath(escaped string) (key, p string, folder bool, err error) {
	folder = strings.HasSuffix(escaped, "/")
	segs := strings.Split(strings.Trim(escaped, "/"), "/")
	for i, seg := range segs {
		if segs[i], err = url.PathUnescape(seg); err != nil {
			return "", "", false, badRequest("malformed path: %v", err)
		}
	}
	key, p = segs[0], strings.Join(segs[1:], "/")
	if key == "" {
		return "", "", false, badRequest("a repository key is required")
	}
	if p != "" {
		if _, _, _, err := types.SplitItemPath(p); err != nil {
			return "", "", false, badRequest("malformed path %q", p)
		}
	}
	return key, p, folder, nil
}

// flag01 reads a 0/1 query parameter (true and false are accepted too).
func flag01(q url.Values, name string, def bool) (bool, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, badRequest("%s must be 0 or 1", name)
	}
	return b, nil
}

func (s artifactoryService) copyItem(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, false)
}

func (s artifactoryService) moveItem(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, true)
}

// transfer answers POST /api/copy and /api/move. Malformed requests and
// sources or targets that cannot take part answer 400 with a report, as the
// spec shapes it; a missing source is 404; a caller without the rights gets
// 401 or 403. Otherwise the answer is 200 with per-item messages, even when
// items failed.
func (s artifactoryService) transfer(w http.ResponseWriter, r *http.Request, move bool) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	verb := "copy"
	if move {
		verb = "move"
	}
	srcKey, srcPath, _, err := splitRepoPath(
		strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/api/"+verb+"/"))
	if err != nil {
		refuseTransfer(w, err.Error())
		return
	}
	q := r.URL.Query()
	to := strings.TrimPrefix(q.Get("to"), "/")
	if to == "" {
		refuseTransfer(w, "The 'to' parameter must name the target as /<repository>/<path>")
		return
	}
	dry, err := flag01(q, "dry", false)
	if err != nil {
		refuseTransfer(w, err.Error())
		return
	}
	suppress, err := flag01(q, "suppressLayouts", true)
	if err != nil {
		refuseTransfer(w, err.Error())
		return
	}
	failFast, err := flag01(q, "failFast", false)
	if err != nil {
		refuseTransfer(w, err.Error())
		return
	}
	p, can, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	from, cached, err := resolveRepo(ctx, st, srcKey)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve source repository", err))
		return
	}
	if !can(from, types.ActionRead) || (move && !can(from, types.ActionDelete)) {
		writeErr(w, r, denied(p))
		return
	}
	cached = cached || from.Class == repo.Remote
	switch {
	case from.Class == repo.Virtual:
		refuseTransfer(w, fmt.Sprintf("Cannot %s from the virtual repository '%s'", verb, from.Key))
		return
	case move && cached:
		refuseTransfer(w, fmt.Sprintf("Cannot move from the remote repository '%s'", from.Key))
		return
	}
	toKey, toPath, into := splitTarget(to)
	target, err := st.Repos().Get(ctx, toKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		refuseTransfer(w, fmt.Sprintf("The target repository '%s' does not exist", toKey))
		return
	case err != nil:
		writeErr(w, r, s.internalError(ctx, "get target repository", err))
		return
	case target.Class != repo.Local && target.Class != repo.Federated:
		refuseTransfer(
			w,
			fmt.Sprintf("The target repository '%s' is not a local repository", toKey),
		)
		return
	}
	if !can(target, types.ActionWrite) {
		writeErr(w, r, denied(p))
		return
	}
	res, err := s.items(st).Transfer(ctx, item.Transfer{
		From:       item.Target{Repo: from, Cached: cached, Path: srcPath},
		To:         item.Target{Repo: target, Path: toPath},
		IntoFolder: into, Move: move, Dry: dry, FailFast: failFast,
		TranslateLayouts: !suppress, CanOverwrite: can(target, types.ActionDelete),
		By: p.Name,
	})
	switch {
	case errors.Is(err, item.ErrInvalidTransfer):
		refuseTransfer(w, strings.TrimPrefix(err.Error(), item.ErrInvalidTransfer.Error()+": "))
		return
	case err != nil:
		writeErr(w, r, s.itemError(ctx, verb, err))
		return
	}
	writeTransfer(w, http.StatusOK, res.Messages)
}

// splitTarget splits a decoded "to" value, "<repo>/<path>", noting a
// trailing slash (the source goes inside the target folder).
func splitTarget(to string) (key, p string, into bool) {
	into = strings.HasSuffix(to, "/")
	key, p, _ = strings.Cut(strings.Trim(to, "/"), "/")
	return key, p, into
}

func refuseTransfer(w http.ResponseWriter, text string) {
	writeTransfer(w, http.StatusBadRequest, []item.Message{{Level: item.LevelError, Text: text}})
}

func writeTransfer(w http.ResponseWriter, status int, msgs []item.Message) {
	type message struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	out := struct {
		Messages []message `json:"messages"`
	}{Messages: make([]message, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, message{m.Level, m.Text})
	}
	w.Header().Set("Content-Type", copyOrMoveType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

// downloadFolderArchive answers GET /api/archive/download/{repo}/{path}: a
// folder, or a whole repository with an empty path, streamed as one archive.
// Like Artifactory it is off until the folder download setting enables it,
// and bounded by that setting's file count and size.
func (s artifactoryService) downloadFolderArchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	key, p, _, err := splitRepoPath(
		strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/api/archive/download/"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	kind, ok := item.ParseArchiveKind(r.URL.Query().Get("archiveType"))
	if !ok {
		writeErr(w, r, badRequest("archiveType must be zip, tar, tar.gz or tgz"))
		return
	}
	sums, err := flag01(r.URL.Query(), "includeChecksumFiles", false)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	caller, can, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, key)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve repository", err))
		return
	}
	if !can(c, types.ActionRead) {
		writeErr(w, r, denied(caller))
		return
	}
	if c.Class == repo.Virtual {
		writeErr(w, r, badRequest(
			"Only local repositories and remote caches can be downloaded as an archive"))
		return
	}
	fd, err := item.FolderDownload(ctx, st)
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "folder download setting", err))
		return
	}
	if !fd.Enabled || (caller.Anonymous && !fd.EnabledForAnonymous) {
		writeErr(w, r, apierror.New(http.StatusForbidden, "Folder download is disabled"))
		return
	}
	t := item.Target{Repo: c, Cached: cached || c.Class == repo.Remote, Path: p}
	tree, err := s.items(st).Tree(ctx, t)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "list folder", err))
		return
	}
	if len(tree) > 0 && p != "" && !tree[0].Folder {
		writeErr(w, r, badRequest("%s is a file, not a folder", p))
		return
	}
	var files []types.Item
	var total int64
	for _, it := range tree {
		if !it.Folder {
			files = append(files, it)
			total += it.Size
		}
	}
	if len(files) > fd.MaxFiles || total > int64(fd.MaxDownloadSizeMB)<<20 {
		writeErr(w, r, badRequest(
			"The folder exceeds the download limits: %d files and %d MB at most",
			fd.MaxFiles, fd.MaxDownloadSizeMB))
		return
	}
	name := c.Key
	if p != "" {
		name = path.Base(p)
	}
	w.Header().Set("Content-Type", kind.ContentType())
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+kind.Ext()))
	w.WriteHeader(http.StatusOK)
	if err := s.items(st).ArchiveFiles(ctx, files, p, kind, sums, w); err != nil {
		// The status is sent; the client sees a truncated archive.
		_ = s.internalError(ctx, "stream folder archive", err)
	}
}

// getArchiveEntry serves one entry of an archive file: the part of an item
// path after "!/" (decision 0002).
func (s artifactoryService) getArchiveEntry(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	f found,
	entry string,
) {
	ctx := r.Context()
	if f.item.Folder {
		writeErr(w, r, itemNotFound())
		return
	}
	err := s.items(st).ArchiveEntry(ctx, f.item, entry, func(size int64, rd io.Reader) error {
		h := w.Header()
		h.Set("Content-Type", item.MimeType(entry))
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		h.Set("X-Artifactory-Filename", path.Base(entry))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return nil
		}
		_, err := io.Copy(w, rd)
		return err //nolint:wrapcheck // the status is sent; only logged
	})
	switch {
	case errors.Is(err, item.ErrNotFound):
		writeErr(w, r, itemNotFound())
	case err != nil:
		_ = s.internalError(ctx, "archive entry", err)
	}
}
