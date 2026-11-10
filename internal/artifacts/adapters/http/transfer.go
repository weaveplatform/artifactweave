package artifactshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// copyOrMoveType is the media type of a copy or move report.
const copyOrMoveType = "application/vnd.org.jfrog.artifactory.storage.CopyOrMoveResult+json"

// RegisterTransferRoutes serves copy, move, folder archive download and
// archive entries, all hand-written in the spec, under the patterns
// httpkit.ChiPatterns derives, so, registered before the router's stubs of
// the hand-written operations, they replace the 501 stubs. An archive entry
// in a nested folder does not fit the entry pattern (a chi parameter is one
// segment) and reaches GetItem through the plain item route instead.
func (s ArtifactoryAPI) RegisterTransferRoutes(r chi.Router) {
	for _, p := range httpkit.ChiPatterns("/artifactory/api/copy/{srcRepoKey}/{srcFilePath}") {
		r.Post(p, s.copyItem)
	}
	for _, p := range httpkit.ChiPatterns("/artifactory/api/move/{srcRepoKey}/{srcFilePath}") {
		r.Post(p, s.moveItem)
	}
	for _, p := range httpkit.ChiPatterns("/artifactory/api/archive/download/{repoKey}/{path}") {
		r.Get(p, s.downloadFolderArchive)
	}
	for _, p := range httpkit.ChiPatterns("/artifactory/{repoKey}/{archivePath}!/{entryPath}") {
		r.Get(p, s.GetItem)
		r.Head(p, s.GetItem)
	}
}

// flag01 reads a 0/1 query parameter (true and false are accepted too).
func flag01(q url.Values, name string, def bool) (bool, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, httpkit.BadRequestf("%s must be 0 or 1", name)
	}
	return b, nil
}

func (s ArtifactoryAPI) copyItem(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, false)
}

func (s ArtifactoryAPI) moveItem(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, true)
}

// transfer answers POST /api/copy and /api/move. Malformed requests, and
// sources or targets that are missing or cannot take part, answer 400 with
// a report, as JFrog's copy and move pages list no 404; a caller without
// the rights gets 401 or 403. Otherwise the answer is 200 with per-item
// messages, even when items failed.
func (s ArtifactoryAPI) transfer(w http.ResponseWriter, r *http.Request, move bool) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	verb := "copy"
	if move {
		verb = "move"
	}
	srcKey, srcPath, _, err := itemkit.SplitRepoPath(
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
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	from, cached, err := itemkit.ResolveRepo(ctx, st, srcKey)
	var missing *apierror.Error
	switch {
	case errors.As(err, &missing) && missing.Status == http.StatusNotFound:
		refuseTransfer(w, missing.Message)
		return
	case err != nil:
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve source repository", err))
		return
	}
	if !acc.File(from, srcPath, api.ActionRead) ||
		(move && !acc.File(from, srcPath, api.ActionDelete)) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
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
	case errors.Is(err, kernel.ErrNotFound):
		refuseTransfer(w, fmt.Sprintf("The target repository '%s' does not exist", toKey))
		return
	case err != nil:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "get target repository", err))
		return
	case target.Class != repo.Local && target.Class != repo.Federated:
		refuseTransfer(
			w,
			fmt.Sprintf("The target repository '%s' is not a local repository", toKey),
		)
		return
	}
	// The rights are checked where the source lands: inside the target
	// folder when the target names one (a trailing slash, or the root).
	dest := toPath
	if into || toPath == "" {
		dest = path.Join(toPath, path.Base(srcPath))
	}
	if !acc.File(target, dest, api.ActionWrite) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	svc, src := s.Items(st), item.Target{Repo: from, Cached: cached, Path: srcPath}
	canOverwrite, err := s.overwriteRight(ctx, st, acc, p, landing{
		src: src, target: target, dest: dest, shouldMove: move,
	})
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	res, err := svc.Transfer(ctx, item.Transfer{
		From:       src,
		To:         item.Target{Repo: target, Path: toPath},
		IntoFolder: into, Move: move, Dry: dry, FailFast: failFast,
		TranslateLayouts: !suppress, CanOverwrite: canOverwrite,
		By: p.Name,
	})
	switch {
	case errors.Is(err, item.ErrInvalidTransfer):
		refuseTransfer(w, strings.TrimPrefix(err.Error(), item.ErrInvalidTransfer.Error()+": "))
		return
	case errors.Is(err, item.ErrNoSource):
		refuseTransfer(w, fmt.Sprintf("The source %s:%s does not exist", srcKey, srcPath))
		return
	case err != nil:
		httpkit.WriteErr(w, r, s.ItemError(ctx, verb, err))
		return
	}
	writeTransfer(w, http.StatusOK, res.Messages)
}

// landing is where a copy or move of the item at src lands: dest in
// target, the path the caller's rights are checked at.
type landing struct {
	src        item.Target
	target     repo.Config
	dest       string
	shouldMove bool
}

// overwriteRight is the caller's right to replace what a copy or move l
// lands on: Delete at its target, and at each place a stored sidecar its
// file takes along lands on a file (format.SidecarKeeper), since a sidecar
// that cannot land holds its file back (item.Service.Transfer). On each
// such sidecar, a Helm chart's .prov say, the caller needs the rights it
// needs on the file (decision 0039): Read at the source, Delete there too
// for a move, and Deploy where it lands. Without one the request is
// refused, as for the file.
func (s ArtifactoryAPI) overwriteRight(
	ctx context.Context,
	st Store,
	acc itemkit.Access,
	p kernel.Principal,
	l landing,
) (bool, error) {
	moves, err := s.Items(st).SidecarsBeside(ctx, l.src, l.dest)
	if err != nil {
		return false, s.ItemError(ctx, "sidecars", err)
	}
	want := api.ActionRead
	if l.shouldMove {
		want |= api.ActionDelete
	}
	overwrite := acc.File(l.target, l.dest, api.ActionDelete)
	for _, m := range moves {
		if !acc.File(l.src.Repo, m.From, want) || !acc.File(l.target, m.To, api.ActionWrite) {
			return false, httpkit.Denied(p)
		}
		if acc.File(l.target, m.To, api.ActionDelete) {
			continue
		}
		_, err := st.Items().Get(ctx, l.target.Key, false, m.To)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
		case err != nil:
			return false, s.ItemError(ctx, "get sidecar target", err)
		default:
			overwrite = false
		}
	}
	return overwrite, nil
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

// defaultFolderSlots is Artifactory's "Max Parallel Folder Downloads"
// default (folderDownloadConfig.maxConcurrentRequests).
const defaultFolderSlots = 10

// downloadFolderArchive answers GET /api/archive/download/{repo}/{path}: a
// folder, or a whole repository with an empty path, streamed as one archive.
// Like Artifactory it is off until the folder download setting enables it,
// bounded by that setting's file count and size, and refused with 400 while
// the most folder downloads that may run at once are running, after every
// other check as 6.20's FolderDownloadServiceImpl orders them.
func (s ArtifactoryAPI) downloadFolderArchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	key, p, _, err := itemkit.SplitRepoPath(
		strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/api/archive/download/"))
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	kind, ok := item.ParseArchiveKind(r.URL.Query().Get("archiveType"))
	if !ok {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("archiveType must be zip, tar, tar.gz or tgz"))
		return
	}
	sums, err := flag01(r.URL.Query(), "includeChecksumFiles", false)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	caller, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	c, cached, err := itemkit.ResolveRepo(ctx, st, key)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve repository", err))
		return
	}
	if !acc.Folder(c, p, api.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(caller))
		return
	}
	if c.Class == repo.Virtual {
		httpkit.WriteErr(w, r, httpkit.BadRequestf(
			"Only local repositories and remote caches can be downloaded as an archive"))
		return
	}
	fd, err := item.FolderDownload(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "folder download setting", err))
		return
	}
	if !fd.Enabled || (caller.Anonymous && !fd.EnabledForAnonymous) {
		httpkit.WriteErr(w, r, apierror.New(http.StatusForbidden, "Folder download is disabled"))
		return
	}
	t := item.Target{Repo: c, Cached: cached || c.Class == repo.Remote, Path: p}
	tree, err := s.Items(st).Tree(ctx, t)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "list folder", err))
		return
	}
	if len(tree) > 0 && p != "" && !tree[0].Folder {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("%s is a file, not a folder", p))
		return
	}
	var files []model.Item
	var total int64
	for _, it := range tree {
		// Files the caller may not read are left out of the archive.
		if !it.Folder && !protocolPrivateItem(c, it.FullPath()) &&
			!machineLearningPrivate(c, it.FullPath()) && !chefMetadataItem(c, it.FullPath()) &&
			acc.File(c, it.FullPath(), api.ActionRead) {
			files = append(files, it)
			total += it.Size
		}
	}
	if len(files) > fd.MaxFiles || total > int64(fd.MaxDownloadSizeMB)<<20 {
		httpkit.WriteErr(w, r, httpkit.BadRequestf(
			"The folder exceeds the download limits: %d files and %d MB at most",
			fd.MaxFiles, fd.MaxDownloadSizeMB))
		return
	}
	if !s.takeFolderSlot() {
		httpkit.WriteErr(w, r, httpkit.BadRequestf(
			"There are too many folder download requests currently running. Try again later."))
		return
	}
	defer s.releaseFolderSlot()
	name := c.Key
	if p != "" {
		name = path.Base(p)
	}
	w.Header().Set("Content-Type", kind.ContentType())
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+kind.Ext()))
	w.WriteHeader(http.StatusOK)
	if err := s.Items(st).ArchiveFiles(ctx, files, p, kind, sums, w); err != nil {
		// The status is sent; the client sees a truncated archive.
		_ = s.InternalError(ctx, "stream folder archive", err)
	}
}

// takeFolderSlot reserves a place among the folder downloads that may run
// at once, false when every place is taken. It never waits: a client told to
// try again later is better off than one held until another download ends.
func (s ArtifactoryAPI) takeFolderSlot() bool {
	if s.folderSlots == nil {
		return true
	}
	select {
	case s.folderSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s ArtifactoryAPI) releaseFolderSlot() {
	if s.folderSlots != nil {
		<-s.folderSlots
	}
}
