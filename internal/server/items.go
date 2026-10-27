package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/alpine"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/format/rpm"
	"github.com/weaveplatform/artifactweave/internal/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// registerItemRoutes serves the item paths (decision 0002). They are
// registered under exactly the patterns registerHandwrittenStubs derives from
// the spec, so they replace its 501 stubs rather than sitting beside them.
// HEAD is not in the spec; it shares the GET handler. Item routes are outside
// the management body limit: an upload is streamed, however large.
func (s artifactoryService) registerItemRoutes(r chi.Router) {
	for _, p := range chiPatterns("/artifactory/{repoKey}/{filePath}") {
		r.Put(p, s.deployItem)
		r.Get(p, s.getItem)
		r.Head(p, s.getItem)
		r.Delete(p, s.deleteItem)
	}
	r.Post("/artifactory/api/search/aql", s.searchAQL)
	r.Get("/artifactory/api/agentplugins/{repoKey}/{marketplace}", s.getAgentMarketplace)
	r.Head("/artifactory/api/agentplugins/{repoKey}/{marketplace}", s.getAgentMarketplace)
	r.Post("/artifactory/api/cran/{repoKey}/sources", s.uploadCranSource)
	r.Post("/artifactory/api/cran/{repoKey}/binaries", s.uploadCranBinary)
	s.registerTransferRoutes(r)
	s.registerDebianSnapshotRoutes(r)
}

// itemRequest is a parsed item URL.
type itemRequest struct {
	key      string
	path     string
	isFolder bool
	// props are the matrix parameters; mandatory holds the keys sent as
	// "key+=value", which a download requires the item to carry.
	props     types.Properties
	mandatory map[string]bool
}

// matrix returns the request's matrix parameters as a download matches them.
func (r itemRequest) matrix() types.Matrix {
	return types.Matrix{Props: r.props, Mandatory: r.mandatory}
}

// parseItemPath splits /artifactory/{repoKey}/{path} into the key, the path
// and the matrix parameters (";key=value" after any segment). It works on
// the escaped path, so an encoded ";" or "/" in a name stays part of it.
func parseItemPath(r *http.Request) (itemRequest, error) {
	return parseEscapedItemPath(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"))
}

// parseEscapedItemPath parses an escaped "{repoKey}/{path}", as found after
// a route's prefix.
func parseEscapedItemPath(escaped string) (itemRequest, error) {
	parsed, err := pkgroute.ParsePath(escaped)
	if err != nil {
		return itemRequest{}, err //nolint:wrapcheck // already a 400 for the client
	}
	req := itemRequest{isFolder: parsed.Folder, props: parsed.Props, mandatory: parsed.Mandatory}
	if len(parsed.Segments) > 0 {
		req.key = parsed.Segments[0]
		req.path = strings.Join(parsed.Segments[1:], "/")
	}
	if req.path != "" {
		if _, _, _, err := types.SplitItemPath(req.path); err != nil {
			return req, badRequestf("malformed path %q", req.path)
		}
	}
	return req, nil
}

// itemCaller returns the caller and its permissions for an item request.
func (s artifactoryService) itemCaller(
	ctx context.Context,
	st store.Store,
) (auth.Principal, access, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, access{}, err
	}
	a, err := accessFor(ctx, st, p)
	if err != nil {
		return p, access{}, s.internalError(ctx, "item permissions", err)
	}
	return p, a, nil
}

// resolveRepo returns the repository a key names and whether the key is a
// remote repository's cache alias.
func resolveRepo(ctx context.Context, st store.Store, key string) (repo.Config, bool, error) {
	stored, cached := types.ResolveRepoKey(key)
	c, err := st.Repos().Get(ctx, stored)
	if err == nil && (!cached || c.Class == repo.Remote) {
		return c, cached, nil
	}
	if err == nil || errors.Is(err, store.ErrNotFound) {
		return c, cached, notFoundRepo(key)
	}
	return c, cached, fmt.Errorf("get repository %s: %w", key, err)
}

// itemError maps the item service's errors onto statuses. A format's
// RejectionError answers with its own status and message.
func (s artifactoryService) itemError(ctx context.Context, op string, err error) error {
	var api *apierror.Error
	var refused *format.RejectionError
	var sidecar *item.SidecarError
	var overwrite *item.OverwriteDeniedError
	switch {
	case errors.As(err, &api):
		return api
	case errors.As(err, &refused):
		return apierror.New(refused.Status, refused.Message)
	case errors.As(err, &overwrite):
		return overwriteRefusal(ctx, overwrite)
	case errors.As(err, &sidecar) && errors.Is(err, store.ErrNotFound):
		return apierror.New(http.StatusNotFound, sidecar.Message)
	case errors.As(err, &sidecar):
		return apierror.New(http.StatusConflict, sidecar.Message)
	case errors.Is(err, item.ErrChecksumMismatch):
		return apierror.New(http.StatusConflict,
			"Checksum policy rejected the upload: the content does not match the declared checksum")
	case errors.Is(err, item.ErrBadChecksum):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, item.ErrNoBlob):
		return apierror.New(http.StatusNotFound,
			"Checksum deploy failed: no stored content matches the checksum")
	case errors.Is(err, item.ErrConflict):
		return apierror.New(http.StatusConflict, err.Error())
	case errors.Is(err, item.ErrCollected):
		return apierror.New(http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, item.ErrBadArchive):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, types.ErrInvalidItemPath):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, types.ErrPropertyKeyTooLong):
		// The routes refuse long keys themselves; this answers for any
		// write that reaches a store with one.
		return apierror.New(http.StatusBadRequest, types.ErrPropertyKeyTooLong.Error())
	case errors.Is(err, store.ErrNotFound):
		return itemNotFound()
	}
	return s.internalError(ctx, op, err)
}

// overwriteRefusal answers a write refused because it would replace a file
// the caller may not delete, with Artifactory's text
// (item.OverwriteDeniedError) in whatever envelope the route renders. The
// status is 403, or 401 with a challenge to an anonymous caller, as 6.20's
// UploadServiceImpl.handleInvalidDeployPathError answers one: a client that
// sends credentials only when challenged would otherwise never send them.
func overwriteRefusal(ctx context.Context, e *item.OverwriteDeniedError) error {
	if p, ok := auth.FromContext(ctx); ok && !p.Anonymous {
		return apierror.New(http.StatusForbidden, e.Error())
	}
	return apierror.New(http.StatusUnauthorized, e.Error())
}

// itemNotFound is the 404 for a missing item. No public source documents
// Artifactory's wording.
func itemNotFound() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func (s artifactoryService) items(st store.Store) *item.Service {
	svc := &item.Service{
		Store:            st,
		Blobs:            s.cfg.Blobs,
		Formats:          s.format,
		ReindexAuthorize: reindexauth.New(st, s.cfg.Auth),
	}
	// A nil queue must not become a non-nil Enqueuer.
	if s.cfg.Work != nil {
		svc.Work = s.cfg.Work
	}
	return svc
}

// builds and registry return the services that write items on behalf of
// builds and Docker promotion, with the same format hooks, and settings,
// as items.
func (s artifactoryService) builds(st store.Store) *build.Service {
	svc := &build.Service{
		Store: st, Blobs: s.cfg.Blobs, Formats: s.format,
		AllowDuplicateEntries: s.cfg.AllowDuplicateBuildEntries,
	}
	if s.cfg.Work != nil {
		svc.Work = s.cfg.Work
	}
	return svc
}

func (s artifactoryService) registry(st store.Store) *registry.Service {
	svc := &registry.Service{Store: st, Blobs: s.cfg.Blobs, Formats: s.format}
	if s.cfg.Work != nil {
		svc.Work = s.cfg.Work
	}
	return svc
}

// format returns a package type's format, with the server's settings.
func (s artifactoryService) format(t repo.PackageType) format.Handler {
	if s.cfg.Formats != nil {
		return s.cfg.Formats(t)
	}
	return s.cfg.FormatSettings.For(t)
}

// writeErr renders err in r's surface. An error that is not an
// *apierror.Error reaches the client as a bare 500, so its cause goes to the
// access log.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var typed *apierror.Error
	if !errors.As(err, &typed) {
		noteCause(r.Context(), err)
	}
	apierror.Write(w, surfaceFor(r.URL.Path), err)
}

// deployTarget resolves where a deploy to c lands: a local or federated
// repository itself, or a virtual repository's default deployment
// repository. Remote repositories and their caches take no deploys.
func deployTarget(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	cached bool,
) (repo.Config, error) {
	switch {
	case c.PackageType == repo.P2:
		return c, apierror.New(http.StatusMethodNotAllowed,
			"P2 repositories are remote or virtual; publish to a local Maven or Generic member")
	case cached || c.Class == repo.Remote:
		return c, apierror.New(http.StatusMethodNotAllowed,
			fmt.Sprintf("Cannot deploy to the remote repository '%s'", c.Key))
	case c.Class == repo.Virtual && c.DefaultDeploymentRepo == "":
		return c, apierror.New(http.StatusMethodNotAllowed, fmt.Sprintf(
			"Virtual repository '%s' has no default deployment repository", c.Key))
	case c.Class == repo.Virtual:
		target, err := st.Repos().Get(ctx, c.DefaultDeploymentRepo)
		if err != nil {
			return c, fmt.Errorf("default deployment repository of %s: %w", c.Key, err)
		}
		return target, nil
	}
	return c, nil
}

// deployItem answers PUT on an item path: a file, a folder (trailing slash),
// a checksum deploy, or a checksum sidecar for an existing file.
func (s artifactoryService) deployItem(w http.ResponseWriter, r *http.Request) {
	s.deployEscapedItem(w, r, strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"))
}

// deployEscapedItem also serves package APIs that expose ordinary item PUTs.
// Keeping the original request preserves its error envelope and base URL.
func (s artifactoryService) deployEscapedItem(
	w http.ResponseWriter,
	r *http.Request,
	escaped string,
) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseEscapedItemPath(escaped)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err == nil {
		c, err = deployTarget(ctx, st, c, cached)
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve deploy target", err))
		return
	}
	if !acc.item(c, req.path, req.isFolder, types.ActionWrite) {
		writeErr(w, r, denied(p))
		return
	}
	if req.path == "" {
		writeErr(w, r, badRequestf("a deploy needs a path inside the repository"))
		return
	}
	svc := s.items(st)
	t := item.Target{Repo: c, Path: req.path}
	explode := strings.EqualFold(r.Header.Get("X-Explode-Archive"), "true") ||
		strings.EqualFold(r.Header.Get("X-Explode-Archive-Atomic"), "true")
	byChecksum := strings.EqualFold(r.Header.Get("X-Checksum-Deploy"), "true")
	// Overwriting replaces content, which needs the right to delete it,
	// except with the same content (item.Deploy.OverwriteIdentical). Only
	// the item service can tell that, once it holds the content, so only a
	// deploy that may not replace a file in any case is refused here.
	existing, err := st.Items().Get(ctx, c.Key, false, req.path)
	switch {
	case err == nil && !existing.Folder && !req.isFolder && !explode &&
		!s.mayBeIdentical(byChecksum) && !acc.file(c, req.path, types.ActionDelete):
		writeErr(w, r, overwriteRefusal(ctx, &item.OverwriteDeniedError{
			Repo: c.Key, Path: req.path, User: p.Name,
		}))
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		writeErr(w, r, s.internalError(ctx, "get item", err))
		return
	}
	var it types.Item
	switch {
	case req.isFolder:
		it, err = svc.MakeFolder(ctx, t, req.props, p.Name)
	case explode:
		it, err = svc.Explode(ctx, item.Explode{
			Deploy: s.deployRequest(r, t, req, p),
			Atomic: strings.EqualFold(r.Header.Get("X-Explode-Archive-Atomic"), "true"),
			// Replacing files needs the right to delete them, as a deploy does;
			// it is checked at the archive's path, not per entry.
			CanOverwrite: acc.file(c, req.path, types.ActionDelete),
		})
	case byChecksum:
		d := s.fileDeploy(r, t, req, p, acc)
		d.Reusable = s.reusable(st, p, acc, r.Header.Get("X-Checksum-Deploy-Token"))
		d.OverwriteIdentical = s.mayBeIdentical(true)
		it, err = svc.DeployByChecksum(ctx, d)
	default:
		if artifact, alg, ok := item.Sidecar(req.path); ok &&
			(svc.OwnsSidecars(c) || s.isFile(ctx, st, c, artifact)) {
			s.recordChecksum(w, r, svc, item.Target{Repo: c, Path: artifact}, alg)
			return
		}
		d := s.fileDeploy(r, t, req, p, acc)
		d.OverwriteIdentical = s.mayBeIdentical(false)
		it, err = svc.Deploy(ctx, d)
	}
	if answered(w, err) {
		return
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "deploy", err))
		return
	}
	// Every deploy names what it stored, where a format may have put it, as
	// 6.20's SuccessfulDeploymentResponseHelper does; an archive names the
	// folder it was extracted into.
	w.Header().Set("Location", downloadURI(ctx, it.ReportedRepoKey(), it.FullPath()))
	if explode {
		// The deployartifactsfromarchive page documents a text/plain 201
		// and gives no text. jfrog-client-go decodes any body an upload
		// answers as JSON (ExtractSha256FromResponseBody), so jf rt u
		// --explode could only work against Artifactory with no body.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		return
	}
	writeJSON(w, http.StatusCreated, itemCreated(ctx, it))
}

// answered writes the status of a write the format took without making it
// (format.AcceptedError), with no body, as Artifactory answers one.
func answered(w http.ResponseWriter, err error) bool {
	var accepted *format.AcceptedError
	if !errors.As(err, &accepted) {
		return false
	}
	w.WriteHeader(accepted.Status)
	return true
}

func (artifactoryService) deployRequest(
	r *http.Request,
	t item.Target,
	req itemRequest,
	p auth.Principal,
) item.Deploy {
	return item.Deploy{
		Target:     t,
		Body:       r.Body,
		Declared:   pkgroute.DeclaredChecksums(r.Header),
		Properties: req.props,
		By:         p.Name,
	}
}

// fileDeploy is the deploy of one file. The format may store it under
// another name (Maven's unique snapshots), and replacing a file there needs
// the right to delete it, as at the path sent.
func (s artifactoryService) fileDeploy(
	r *http.Request,
	t item.Target,
	req itemRequest,
	p auth.Principal,
	acc access,
) item.Deploy {
	d := s.deployRequest(r, t, req, p)
	d.CanOverwritePath = func(full string) bool { return acc.file(t.Repo, full, types.ActionDelete) }
	return d
}

// mayBeIdentical reports whether a deploy over a file the caller may not
// delete goes on to the item service, which stores it only with the file's
// own content: an upload always does, and a checksum deploy unless
// deploy.enforce_permission_check_on_identical_checksum_deploy is on. The
// declared checksums are not compared here, as 6.20's isFileOverwrite
// compares them before it reads the body: a 403 for a wrong checksum and
// none for the right one would tell a caller that may deploy but not read
// what the file holds. Compared with the content, after the checksum
// policy and, for a checksum deploy, the read check (#379), the answer
// tells only a caller that already holds the content.
func (s artifactoryService) mayBeIdentical(byChecksum bool) bool {
	return !byChecksum || !s.cfg.EnforceIdenticalChecksumDeploy
}

// reusable decides whether a checksum deploy may store content the server
// already holds, as Artifactory does: only when the caller may read a file
// with that content. Otherwise anyone who learned a checksum could copy a
// file out of a repository it cannot read into one it can write. The
// exception is token, the checksum token of the caller's own finished
// multipart upload of the content, which no item holds until this deploy.
func (s artifactoryService) reusable(
	st store.Store,
	p auth.Principal,
	acc access,
	token string,
) func(context.Context, types.Blob) (bool, error) {
	return func(ctx context.Context, b types.Blob) (bool, error) {
		if token != "" && s.cfg.Multipart != nil {
			ok, err := s.cfg.Multipart.Finished(ctx, token, p.Name, b.Checksums.SHA256)
			if err != nil || ok {
				return ok, err //nolint:wrapcheck // DeployByChecksum wraps it
			}
		}
		return readsContent(ctx, st, acc, b.Checksums.SHA256)
	}
}

// readsContent reports whether the caller may read a file whose content
// has SHA-256 sum. Only files in repositories count, so content that only
// the trash can or a multipart upload holds is reused by no one; the client
// uploads it again.
func readsContent(ctx context.Context, st store.Store, acc access, sum string) (bool, error) {
	// A nil entry is a repository that no longer exists.
	repos := map[string]*repo.Config{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		files, next, err := st.Items().FindByChecksum(ctx, types.Checksums{SHA256: sum}, page)
		if err != nil {
			return false, fmt.Errorf("find files by checksum: %w", err)
		}
		for _, f := range files {
			c, seen := repos[f.RepoKey]
			if !seen {
				got, err := st.Repos().Get(ctx, f.RepoKey)
				switch {
				case err == nil:
					c = &got
				case !errors.Is(err, store.ErrNotFound):
					return false, fmt.Errorf("get repository %s: %w", f.RepoKey, err)
				}
				repos[f.RepoKey] = c
			}
			if c != nil && acc.file(*c, f.FullPath(), types.ActionRead) {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		page.After = next
	}
}

func (artifactoryService) isFile(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	p string,
) bool {
	it, err := st.Items().Get(ctx, c.Key, false, p)
	return err == nil && !it.Folder
}

// recordChecksum stores a checksum sidecar upload (Maven sends one after
// each file) as the file's original checksum.
func (s artifactoryService) recordChecksum(
	w http.ResponseWriter,
	r *http.Request,
	svc *item.Service,
	t item.Target,
	alg item.Algorithm,
) {
	// A checksum is at most 64 hex characters; anything longer is not one.
	b, err := io.ReadAll(io.LimitReader(r.Body, 256))
	if err != nil {
		writeErr(w, r, badRequestf("reading the checksum: %v", err))
		return
	}
	// Sidecars may carry "<hex>  <file name>" as sha1sum writes them.
	value, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	it, err := svc.RecordSidecar(r.Context(), t, alg, value, r.ContentLength)
	if answered(w, err) {
		return
	}
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "record checksum", err))
		return
	}
	// The file the checksum was recorded on, which a format may have named
	// differently from the sidecar (UploadServiceUtils
	// .sendUploadedChecksumResponse in 6.20).
	w.Header().Set("Location", downloadURI(r.Context(), it.ReportedRepoKey(), it.FullPath()))
	w.WriteHeader(http.StatusCreated)
}

// itemCreated is the ItemCreated body of a deploy. Unlike the storage API's
// item info, its uri is the item's download URL, the same as downloadUri
// (the deployartifact page's example), and a folder's path and uri end in
// "/" (6.20's SuccessfulDeploymentResponseHelper).
func itemCreated(ctx context.Context, it types.Item) map[string]any {
	uri := baseurl.From(ctx) + "/artifactory/" + it.ReportedRepoKey() + "/" +
		escapePath(it.FullPath())
	body := map[string]any{
		"repo":      it.ReportedRepoKey(),
		"path":      it.StoragePath(),
		"created":   formatTime(it.Created),
		"createdBy": it.CreatedBy,
		"uri":       uri,
	}
	if it.Folder {
		body["path"], body["uri"] = withSlash(it.StoragePath()), withSlash(uri)
		return body
	}
	body["downloadUri"] = uri
	body["mimeType"] = it.MimeType
	body["size"] = fmt.Sprint(it.Size)
	body["checksums"] = checksumsJSON(it.Actual)
	body["originalChecksums"] = checksumsJSON(it.Original)
	return body
}

// withSlash ends p in one "/"; the repository root's already does.
func withSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// checksumsJSON is the file info's checksum object, which in Artifactory names
// sha1, md5 and sha256 only; SHA-512 reaches clients as a ".sha512" file.
func checksumsJSON(c types.Checksums) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{"sha1": c.SHA1, "md5": c.MD5, "sha256": c.SHA256} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// formatTime renders Artifactory's ISO 8601 form with milliseconds.
func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// found is an item a download resolved, with the repository it lives in.
type found struct {
	repo repo.Config
	item types.Item
	used func(context.Context) error
}

// resolver rewrites a download's path in a repository that holds items,
// as its format resolves it (item.Service.Resolve).
type resolver func(ctx context.Context, c repo.Config, d format.Download) (string, error)

// download is what a download applies on its way to an item, and the
// storage API, which describes the path as given, does not: Artifactory's
// download checks (generic.CheckDownload) against the path and folder it
// checks, and the format's rewrite of the path.
type download struct {
	path     string
	isFolder bool
	matrix   types.Matrix
	resolve  resolver
	// trace records the steps of a ?trace request (traceItem); nil for a
	// download.
	trace *resolutionTrace
}

// newDownload checks p as Artifactory does: its request parsing splits an
// archive entry ("<archive>!/<entry>") off the archive's path
// (ArtifactoryRequestBase.zipResourcePath), and RepoBase.accepts checks a
// checksum sidecar as the file it describes. folder is whether the request
// ended in a slash; m are its matrix parameters, which a resolution filters
// by as the download filters what it serves.
func newDownload(p string, folder bool, m types.Matrix, resolve resolver) *download {
	if archive, _, ok := strings.Cut(p, "!/"); ok {
		p, folder = archive, false
	} else if artifact, _, ok := item.Sidecar(p); ok {
		p, folder = artifact, false
	}
	return &download{path: p, isFolder: folder, matrix: m, resolve: resolve}
}

// lookup finds the item at p for a download from c: a local repository's
// own tree, a remote repository's cache (nothing is fetched upstream until
// P9), or the first member of a virtual repository, in order, that the
// caller may read and that holds it. root reports the repository root, which
// is not an item. A non-nil dl refuses what Artifactory refuses in each
// repository it reaches, and rewrites p as each repository that holds items
// resolves it before looking there; the storage API passes nil.
func lookup(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	cached bool,
	p string,
	acc access,
	dl *download,
	seen map[string]bool,
) (found, error) {
	if seen[c.Key] {
		return found{}, store.ErrNotFound
	}
	seen[c.Key] = true
	if dl != nil {
		if err := generic.CheckDownload(c, dl.path, dl.isFolder); err != nil {
			dl.stepf("Request is forbidden by %s", c.Key)
			return found{}, err //nolint:wrapcheck // a RejectionError is the answer
		}
	}
	if c.Class != repo.Virtual {
		cached = cached || c.Class == repo.Remote
		if dl != nil && dl.resolve != nil {
			var err error
			asked := p
			if p, err = dl.resolve(ctx, c, format.Download{
				Path:   p,
				Cached: cached,
				Matrix: dl.matrix,
				Readable: func(fullPath string) bool {
					return acc.file(c, fullPath, types.ActionRead)
				},
			}); err != nil {
				return found{}, err
			}
			if p != asked {
				dl.stepf("Request path was translated to %s", p)
			}
		}
		it, err := st.Items().Get(ctx, c.Key, cached, p)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				dl.stepf("Unable to find resource in %s", traceRepoPath(c, cached, p))
			}
			return found{}, err //nolint:wrapcheck // ErrNotFound is the answer
		}
		return found{repo: c, item: it}, nil
	}
	for _, key := range c.Repositories {
		m, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return found{}, fmt.Errorf("member %s of %s: %w", key, c.Key, err)
		}
		if !acc.folder(m, p, types.ActionRead) {
			continue
		}
		dl.stepf("Searching for the resource within %s", m.Key)
		f, err := lookup(ctx, st, m, false, p, acc, dl, seen)
		// A member that refuses the download is passed over like one that
		// lacks the item, as Artifactory's search goes on past every member
		// that does not serve it (VirtualRepoDownloadStrategy.processStandard).
		var refused *format.RejectionError
		if err == nil || (!errors.Is(err, store.ErrNotFound) && !errors.As(err, &refused)) {
			return f, err
		}
	}
	return found{}, store.ErrNotFound
}

// getItem answers GET and HEAD on an item path: file content, a checksum
// sidecar, or a folder listing; with ?trace, the resolution instead.
func (s artifactoryService) getItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := r.URL.Query()["trace"]; ok {
		s.traceItem(w, r)
		return
	}
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseItemPath(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve repository", err))
		return
	}
	if s.tryFormatItem(w, r, st, c, cached, req, p, acc) {
		return
	}
	// A folder check first: the path may be a file or a folder, and in a
	// virtual repository the item lives in a member. The exact right is
	// checked once the item is found. Artifactory refuses a blacked-out
	// repository or an excluded path before it checks permissions; this
	// answers a caller who may not read the path without telling it how
	// the repository is configured (DV-057).
	if !acc.folder(c, req.path, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	if s.getRemoteFolder(w, r, c, cached, req, p, acc) {
		return
	}
	if remoteDownloadType(c, cached) &&
		req.path != "" &&
		!req.isFolder &&
		s.cfg.RemoteSources != nil {
		if s.getRemoteItem(w, r, c, req, p, acc) {
			return
		}
	}
	dl := newDownload(req.path, req.isFolder, req.matrix(), s.items(st).Resolve)
	if req.path == "" {
		if err := generic.CheckDownload(c, "", true); err != nil {
			writeErr(w, r, s.itemError(ctx, "download check", err))
			return
		}
		s.listFolder(
			w,
			r,
			st,
			c,
			found{repo: c, item: types.Item{RepoKey: c.Key, Cached: cached, Folder: true}},
			acc,
		)
		return
	}
	f, err := lookup(ctx, st, c, cached, req.path, acc, dl, map[string]bool{})
	if errors.Is(err, store.ErrNotFound) {
		// "<archive>!/<entry>" names an entry inside an archive file; a path
		// that exists as an item is served as one first.
		if archive, entry, ok := strings.Cut(req.path, "!/"); ok {
			a, err := lookup(ctx, st, c, cached, archive, acc, dl, map[string]bool{})
			if err != nil {
				writeErr(w, r, itemNotFound())
				return
			}
			if !acc.file(a.repo, a.item.FullPath(), types.ActionRead) {
				writeErr(w, r, denied(p))
				return
			}
			s.getArchiveEntry(w, r, st, a, entry)
			return
		}
		s.getSidecar(w, r, st, c, cached, req.path, acc, dl)
		return
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "lookup item", err))
		return
	}
	if !acc.item(f.repo, f.item.FullPath(), f.item.Folder, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	if ok, err := matchesMatrix(ctx, st, f.item, req); err != nil {
		writeErr(w, r, s.internalError(ctx, "item properties", err))
		return
	} else if !ok {
		writeErr(w, r, itemNotFound())
		return
	}
	if f.item.Folder {
		s.listFolder(w, r, st, c, f, acc)
		return
	}
	s.serveFile(w, r, st, f, p)
}

// tryFormatItem lets format-specific metadata and package routes take
// precedence over generic item lookup while preserving their dispatch order.
func (s artifactoryService) tryFormatItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	return s.tryAgentMarketplaceItem(w, r, st, c, cached, req, p, acc) ||
		s.tryAlpineItem(w, r, st, c, cached, req, p, acc) ||
		s.tryCranItem(w, r, st, c, cached, req, p, acc) ||
		s.tryDebianItem(w, r, st, c, cached, req, p, acc) ||
		s.tryRPMItem(w, r, st, c, cached, req, p, acc) ||
		s.tryOpkgItem(w, r, st, c, cached, req, p, acc) ||
		s.tryP2Item(w, r, st, c, cached, req, p, acc)
}

// matchesMatrix applies download matrix parameters (types.Matrix.Matches).
func matchesMatrix(
	ctx context.Context,
	st store.Store,
	it types.Item,
	req itemRequest,
) (bool, error) {
	if len(req.props) == 0 {
		return true, nil
	}
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return false, fmt.Errorf("properties of %s: %w", it.FullPath(), err)
	}
	return req.matrix().Matches(props), nil
}

// getSidecar serves "<file>.sha256", ".sha512", ".sha1" and ".md5" from the file's
// recorded checksums (decision 0004); they are never stored as items. Which
// checksum is served is item.ServedChecksum's: under client-checksums the
// client's, so a file deployed without one has no SHA-1 file until the
// client sends one.
func (s artifactoryService) getSidecar(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	c repo.Config,
	cached bool,
	p string,
	acc access,
	dl *download,
) {
	artifact, alg, ok := item.Sidecar(p)
	if !ok {
		writeErr(w, r, itemNotFound())
		return
	}
	f, err := lookup(r.Context(), st, c, cached, artifact, acc, dl, map[string]bool{})
	// A file the caller may not read has no visible checksum either.
	if err != nil || f.item.Folder ||
		!acc.file(f.repo, f.item.FullPath(), types.ActionRead) {
		writeErr(w, r, itemNotFound())
		return
	}
	value := item.ServedChecksum(f.repo, f.item, alg)
	if value == "" {
		// Artifactory 6.20's message (DownloadServiceImpl.respondForChecksumRequest).
		writeErr(w, r, apierror.New(http.StatusNotFound,
			"Checksum not found for "+f.item.FullPath()))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Last-Modified", f.item.Modified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, value)
	}
}

// serveFile streams a file's content with the headers the JFrog CLI reads
// (Content-Length, Accept-Ranges and the X-Checksum headers); ranges and
// conditional requests are http.ServeContent's.
func (s artifactoryService) serveFile(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	f found,
	p auth.Principal,
) {
	ctx := r.Context()
	it := f.item
	h := w.Header()
	pkgroute.SetChecksums(h, it.Actual)
	h.Set("X-Artifactory-Filename", it.Name)
	h.Set("Content-Type", it.MimeType)
	if h.Get("Content-Disposition") == "" {
		h.Set("Content-Disposition", blob.Disposition(it.Name))
	}
	if it.Actual.SHA1 != "" {
		h.Set("ETag", `"`+it.Actual.SHA1+`"`)
	}
	if s.redirect(w, r, st, f, p) {
		return
	}
	content, _, err := s.cfg.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		// The index names content the blob store does not hold: data loss,
		// not a client error.
		writeErr(w, r, s.internalError(ctx, "open blob", err))
		return
	}
	defer func() { _ = content.Close() }()
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, it.Name, it.Modified, content)
	if sw.complete(r) {
		s.recordCacheUse(ctx, f)
		if countsDownload(r, sw.status) {
			recordDownload(ctx, st, f, p)
		}
	}
}

// statusWriter records the status ServeContent chose, so a 304 or a later
// range of a split download is not counted as a download.
type statusWriter struct {
	http.ResponseWriter
	status    int
	bytes     int64
	hasFailed bool
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	s.hasFailed = s.hasFailed || err != nil
	return n, err //nolint:wrapcheck // a pass-through writer
}

// ReadFrom keeps the underlying writer's zero-copy path (sendfile for fs
// blobs) that io.Copy would otherwise lose behind this wrapper.
func (s *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := io.Copy(s.ResponseWriter, r)
	s.bytes += n
	s.hasFailed = s.hasFailed || err != nil
	return n, err //nolint:wrapcheck // a pass-through writer
}

// complete excludes truncated bodies even when their headers already said 200.
func (s *statusWriter) complete(r *http.Request) bool {
	if r.Method != http.MethodGet || s.hasFailed || (s.status != 200 && s.status != 206) {
		return false
	}
	length := s.Header().Get("Content-Length")
	if length == "" {
		return true
	}
	want, err := strconv.ParseInt(length, 10, 64)
	return err == nil && want == s.bytes
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// startsAtZero reports whether a Range header starts at the first byte, so a
// split download counts once rather than once per part.
func startsAtZero(rng string) bool { return strings.HasPrefix(strings.TrimSpace(rng), "bytes=0-") }

// listFolder answers a folder GET with a plain HTML index, as Artifactory
// serves browsers. named is the repository the request named, a virtual
// repository when f was found in one of its members.
func (s artifactoryService) listFolder(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	named repo.Config,
	f found,
	acc access,
) {
	ctx := r.Context()
	folder := f.item.FullPath()
	var children []types.Item
	page := store.Page{Limit: store.MaxLimit}
	for {
		batch, next, err := st.Items().Children(ctx, f.repo.Key, f.item.Cached, folder, page)
		if err != nil {
			writeErr(w, r, s.internalError(ctx, "list folder", err))
			return
		}
		children = append(children, batch...)
		if next == "" {
			break
		}
		page.After = next
	}
	title := html.EscapeString(path.Join(f.repo.Key, folder))
	var b strings.Builder
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html><head><title>Index of %s</title></head><body>\n", title)
	fmt.Fprintf(&b, "<h1>Index of %s</h1>\n<pre>", title)
	patterns := []authz.RepoPatterns{authz.PatternsOf(f.repo)}
	if named.Key != f.repo.Key {
		patterns = append(patterns, authz.PatternsOf(named))
	}
	for _, c := range children {
		// Children the caller may not see are left out; a folder shows when
		// something below it is readable. So are children the patterns
		// reject, the member's and the virtual repository's, as
		// Artifactory's browsing leaves them out
		// (TreeBrowsingCriteriaBuilder.applyRepoIncludeExclude,
		// RepositoryBrowsingServiceImpl.virtualRepoAccepts).
		if !acc.item(f.repo, c.FullPath(), c.Folder, types.ActionRead) ||
			!acceptedByAll(patterns, c.FullPath(), c.Folder) {
			continue
		}
		href, text := url.PathEscape(c.Name), c.Name
		if strings.Contains(href, ":") {
			// A browser reads "sha256:<hex>/", a Docker digest folder, as a
			// URL with the scheme sha256 (RFC 3986, 4.2); the dot segment
			// keeps it relative.
			href = "./" + href
		}
		if c.Folder {
			href, text = href+"/", text+"/"
		}
		fmt.Fprintf(&b, "<a href=\"%s\">%s</a>\n", html.EscapeString(href), html.EscapeString(text))
	}
	b.WriteString("</pre></body></html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, b.String())
	}
}

// acceptedByAll reports whether every set of patterns accepts p.
func acceptedByAll(patterns []authz.RepoPatterns, p string, folder bool) bool {
	for _, pat := range patterns {
		if !pat.Accepts(p, folder) {
			return false
		}
	}
	return true
}

// deleteItem answers DELETE on an item path: the item and everything below
// it, into the trash can for local repositories.
func (s artifactoryService) deleteItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseItemPath(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve repository", err))
		return
	}
	if c.Class == repo.Virtual {
		writeErr(w, r, apierror.New(http.StatusMethodNotAllowed,
			fmt.Sprintf("Cannot delete from the virtual repository '%s'", c.Key)))
		return
	}
	if !acc.file(c, req.path, types.ActionDelete) {
		writeErr(w, r, denied(p))
		return
	}
	if c.PackageType == repo.Debian && strings.HasPrefix(req.path, "snapshots/") {
		parts := strings.Split(req.path, "/")
		if len(parts) != 2 || parts[1] == "" {
			writeErr(w, r, apierror.New(http.StatusConflict,
				"Delete the complete Debian snapshot folder"))
			return
		}
		ctx = debian.WithSnapshotDelete(ctx)
	}
	t := item.Target{Repo: c, Cached: cached || c.Class == repo.Remote, Path: req.path}
	svc := s.items(st)
	// A file's sidecars go with it, such as a Helm chart's .prov, so the
	// caller needs Delete on each as on the file (decision 0039).
	sidecars, err := svc.SidecarsBeside(ctx, t, t.Path)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "sidecars", err))
		return
	}
	for _, sc := range sidecars {
		if !acc.file(c, sc.From, types.ActionDelete) {
			writeErr(w, r, denied(p))
			return
		}
	}
	if _, err := svc.Delete(ctx, t, p.Name); err != nil {
		writeErr(w, r, s.itemError(ctx, "delete item", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetUploadsConfig tells the JFrog CLI whether to upload large files in
// parts (internal/service/multipart) or in one request (research 09).
func (s artifactoryService) GetUploadsConfig(
	context.Context,
	artgen.GetUploadsConfigRequestObject,
) (artgen.GetUploadsConfigResponseObject, error) {
	supported := s.cfg.Multipart != nil
	return artgen.GetUploadsConfig200JSONResponse{Supported: &supported}, nil
}

func remoteDownloadType(c repo.Config, cached bool) bool {
	return c.Class == repo.Remote &&
		(c.PackageType == repo.Generic && !cached || maven.Serves(c.PackageType) ||
			c.PackageType == repo.P2 ||
			(alpine.Serves(c.PackageType) || debian.Serves(c.PackageType) ||
				rpm.Serves(c.PackageType)) && !cached)
}
