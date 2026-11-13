package itemkit

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

// ItemRequest is a parsed item URL.
type ItemRequest struct {
	Key      string
	Path     string
	IsFolder bool
	// Props are the matrix parameters; mandatory holds the keys sent as
	// "key+=value", which a download requires the item to carry.
	Props     model.Properties
	Mandatory map[string]bool
}

// matrix returns the request's matrix parameters as a download matches them.
func (r ItemRequest) matrix() model.Matrix {
	return model.Matrix{Props: r.Props, Mandatory: r.Mandatory}
}

// ParseItemPath splits /artifactory/{repoKey}/{path} into the key, the path
// and the matrix parameters (";key=value" after any segment). It works on
// the escaped path, so an encoded ";" or "/" in a name stays part of it.
func ParseItemPath(r *http.Request) (ItemRequest, error) {
	return ParseEscapedItemPath(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"))
}

// ParseEscapedItemPath parses an escaped "{repoKey}/{path}", as found after
// a route's prefix.
func ParseEscapedItemPath(escaped string) (ItemRequest, error) {
	parsed, err := pkgroute.ParsePath(escaped)
	if err != nil {
		return ItemRequest{}, err //nolint:wrapcheck // already a 400 for the client
	}
	req := ItemRequest{IsFolder: parsed.Folder, Props: parsed.Props, Mandatory: parsed.Mandatory}
	if len(parsed.Segments) > 0 {
		req.Key = parsed.Segments[0]
		req.Path = strings.Join(parsed.Segments[1:], "/")
	}
	if req.Path != "" {
		if _, _, _, err := kernel.SplitItemPath(req.Path); err != nil {
			return req, httpkit.BadRequestf("malformed path %q", req.Path)
		}
	}
	return req, nil
}

// ItemCaller returns the caller and its permissions for an item request.
func (s *Kit) ItemCaller(
	ctx context.Context,
	st Store,
) (kernel.Principal, Access, error) {
	p, err := httpkit.Principal(ctx)
	if err != nil {
		return p, Access{}, err
	}
	d, _ := accessapi.DeciderFrom(ctx)
	a, err := AccessFor(ctx, s.Access.Authorizer, p, d)
	if err != nil {
		return p, Access{}, s.InternalError(ctx, "item permissions", err)
	}
	return p, a, nil
}

// ResolveRepo returns the repository a key names and whether the key is a
// remote repository's cache alias.
func ResolveRepo(ctx context.Context, st Store, key string) (repo.Config, bool, error) {
	stored, cached := model.ResolveRepoKey(key)
	c, err := st.Repos().Get(ctx, stored)
	if err == nil && (!cached || c.Class == repo.Remote) {
		return c, cached, nil
	}
	if err == nil || errors.Is(err, kernel.ErrNotFound) {
		return c, cached, NotFoundRepo(key)
	}
	return c, cached, fmt.Errorf("get repository %s: %w", key, err)
}

// ItemError maps the item service's errors onto statuses. A format's
// RejectionError answers with its own status and message.
func (s *Kit) ItemError(ctx context.Context, op string, err error) error {
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
		return OverwriteRefusal(ctx, overwrite)
	case errors.As(err, &sidecar) && errors.Is(err, kernel.ErrNotFound):
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
	case errors.Is(err, kernel.ErrInvalidItemPath):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, model.ErrPropertyKeyTooLong):
		// The routes refuse long keys themselves; this answers for any
		// write that reaches a store with one.
		return apierror.New(http.StatusBadRequest, model.ErrPropertyKeyTooLong.Error())
	case errors.Is(err, kernel.ErrNotFound):
		return ItemNotFound()
	}
	return s.InternalError(ctx, op, err)
}

// OverwriteRefusal answers a write refused because it would replace a file
// the caller may not delete, with Artifactory's text
// (item.OverwriteDeniedError) in whatever envelope the route renders. The
// status is 403, or 401 with a challenge to an anonymous caller, as 6.20's
// UploadServiceImpl.handleInvalidDeployPathError answers one: a client that
// sends credentials only when challenged would otherwise never send them.
func OverwriteRefusal(ctx context.Context, e *item.OverwriteDeniedError) error {
	if p, ok := kernel.FromContext(ctx); ok && !p.Anonymous {
		return apierror.New(http.StatusForbidden, e.Error())
	}
	return apierror.New(http.StatusUnauthorized, e.Error())
}

// ItemNotFound is the 404 for a missing item. No public source documents
// Artifactory's wording.
func ItemNotFound() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

// Items returns the item service over st, with the kit's blobs, formats,
// work queue and access keys, which every item write goes through.
func (s *Kit) Items(st Store) *item.Service {
	svc := &item.Service{
		Store:            st,
		Blobs:            s.Blobs,
		Keys:             s.Access.Keys,
		Formats:          s.Format,
		ReindexAuthorize: reindexauth.New(s.Access.Authenticator, s.Access.Authorizer),
	}
	// A nil queue must not become a non-nil Enqueuer.
	if s.Work != nil {
		svc.Work = s.Work
	}
	return svc
}

// Format returns a package type's format, with the server's settings.
func (s *Kit) Format(t repo.PackageType) format.Handler {
	if s.Formats != nil {
		return s.Formats(t)
	}
	return s.FormatSettings.For(t)
}

// DeployTarget resolves where a deploy to c lands: a local or federated
// repository itself, or a virtual repository's default deployment
// repository. Remote repositories and their caches take no deploys.
func DeployTarget(
	ctx context.Context,
	st Store,
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

// MayBeIdentical reports whether a deploy over a file the caller may not
// delete goes on to the item service, which stores it only with the file's
// own content: an upload always does, and a checksum deploy unless
// deploy.enforce_permission_check_on_identical_checksum_deploy is on. The
// declared checksums are not compared here, as 6.20's isFileOverwrite
// compares them before it reads the body: a 403 for a wrong checksum and
// none for the right one would tell a caller that may deploy but not read
// what the file holds. Compared with the content, after the checksum
// policy and, for a checksum deploy, the read check (#379), the answer
// tells only a caller that already holds the content.
func (s *Kit) MayBeIdentical(byChecksum bool) bool {
	return !byChecksum || !s.EnforceIdenticalChecksumDeploy
}

// ItemCreated is the ItemCreated body of a deploy. Unlike the storage API's
// item info, its uri is the item's download URL, the same as downloadUri
// (the deployartifact page's example), and a folder's path and uri end in
// "/" (6.20's SuccessfulDeploymentResponseHelper).
func ItemCreated(ctx context.Context, it model.Item) map[string]any {
	uri := baseurl.From(ctx) + "/artifactory/" + it.ReportedRepoKey() + "/" +
		httpkit.EscapePath(it.FullPath())
	body := map[string]any{
		"repo":      it.ReportedRepoKey(),
		"path":      it.StoragePath(),
		"created":   httpkit.FormatTime(it.Created),
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
	body["checksums"] = ChecksumsJSON(it.Actual)
	body["originalChecksums"] = ChecksumsJSON(it.Original)
	return body
}

// withSlash ends p in one "/"; the repository root's already does.
func withSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// ChecksumsJSON is the file info's checksum object, which in Artifactory names
// sha1, md5 and sha256 only; SHA-512 reaches clients as a ".sha512" file.
func ChecksumsJSON(c kernel.Checksums) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{"sha1": c.SHA1, "md5": c.MD5, "sha256": c.SHA256} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// Found is an item a download resolved, with the repository it lives in.
type Found struct {
	Repo repo.Config
	Item model.Item
	Used func(context.Context) error
}

// resolver rewrites a download's path in a repository that holds items,
// as its format resolves it (item.Service.Resolve).
type resolver func(ctx context.Context, c repo.Config, d format.Download) (string, error)

// Download is what a download applies on its way to an item, and the
// storage API, which describes the path as given, does not: Artifactory's
// download checks (generic.CheckDownload) against the path and folder it
// checks, and the format's rewrite of the path.
type Download struct {
	path     string
	isFolder bool
	matrix   model.Matrix
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
func newDownload(p string, folder bool, m model.Matrix, resolve resolver) *Download {
	if archive, _, ok := strings.Cut(p, "!/"); ok {
		p, folder = archive, false
	} else if artifact, _, ok := item.Sidecar(p); ok {
		p, folder = artifact, false
	}
	return &Download{path: p, isFolder: folder, matrix: m, resolve: resolve}
}

// Lookup finds the item at p for a download from c: a local repository's
// own tree, a remote repository's cache (nothing is fetched upstream until
// P9), or the first member of a virtual repository, in order, that the
// caller may read and that holds it. root reports the repository root, which
// is not an item. A non-nil dl refuses what Artifactory refuses in each
// repository it reaches, and rewrites p as each repository that holds items
// resolves it before looking there; the storage API passes nil.
func Lookup(
	ctx context.Context,
	st Store,
	c repo.Config,
	cached bool,
	p string,
	acc Access,
	dl *Download,
	seen map[string]bool,
) (Found, error) {
	if seen[c.Key] {
		return Found{}, kernel.ErrNotFound
	}
	seen[c.Key] = true
	if dl != nil {
		if err := generic.CheckDownload(c, dl.path, dl.isFolder); err != nil {
			dl.stepf("Request is forbidden by %s", c.Key)
			return Found{}, err //nolint:wrapcheck // a RejectionError is the answer
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
					return acc.File(c, fullPath, accessapi.ActionRead)
				},
			}); err != nil {
				return Found{}, err
			}
			if p != asked {
				dl.stepf("Request path was translated to %s", p)
			}
		}
		it, err := st.Items().Get(ctx, c.Key, cached, p)
		if err != nil {
			if errors.Is(err, kernel.ErrNotFound) {
				dl.stepf("Unable to find resource in %s", traceRepoPath(c, cached, p))
			}
			return Found{}, err //nolint:wrapcheck // ErrNotFound is the answer
		}
		return Found{Repo: c, Item: it}, nil
	}
	for _, key := range c.Repositories {
		m, err := st.Repos().Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return Found{}, fmt.Errorf("member %s of %s: %w", key, c.Key, err)
		}
		if !acc.Folder(m, p, accessapi.ActionRead) {
			continue
		}
		dl.stepf("Searching for the resource within %s", m.Key)
		f, err := Lookup(ctx, st, m, false, p, acc, dl, seen)
		// A member that refuses the download is passed over like one that
		// lacks the item, as Artifactory's search goes on past every member
		// that does not serve it (VirtualRepoDownloadStrategy.processStandard).
		var refused *format.RejectionError
		if err == nil || (!errors.Is(err, kernel.ErrNotFound) && !errors.As(err, &refused)) {
			return f, err
		}
	}
	return Found{}, kernel.ErrNotFound
}

// GetItem answers GET and HEAD on an item path: file content, a checksum
// sidecar, or a folder listing; with ?trace, the resolution instead.
func (s *Kit) GetItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := r.URL.Query()["trace"]; ok {
		s.traceItem(w, r)
		return
	}
	ctx := r.Context()
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	req, err := ParseItemPath(r)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	c, cached, err := ResolveRepo(ctx, st, req.Key)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve repository", err))
		return
	}
	// Format routes take precedence over the generic lookup, in their
	// fixed dispatch order (Deps.Overlays).
	for _, o := range s.Overlays {
		if o(s, w, r, st, c, cached, req, p, acc) {
			return
		}
	}
	// A folder check first: the path may be a file or a folder, and in a
	// virtual repository the item lives in a member. The exact right is
	// checked once the item is found. Artifactory refuses a blacked-out
	// repository or an excluded path before it checks permissions; this
	// answers a caller who may not read the path without telling it how
	// the repository is configured (DV-057).
	if !acc.Folder(c, req.Path, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if s.getRemoteFolder(w, r, c, cached, req, p, acc) {
		return
	}
	if remoteDownloadType(c, cached) &&
		req.Path != "" &&
		!req.IsFolder &&
		s.RemoteSources != nil {
		if s.getRemoteItem(w, r, c, req, p, acc) {
			return
		}
	}
	dl := newDownload(req.Path, req.IsFolder, req.matrix(), s.Items(st).Resolve)
	if req.Path == "" {
		if err := generic.CheckDownload(c, "", true); err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "download check", err))
			return
		}
		s.listFolder(
			w,
			r,
			st,
			c,
			Found{Repo: c, Item: model.Item{RepoKey: c.Key, Cached: cached, Folder: true}},
			acc,
		)
		return
	}
	f, err := Lookup(ctx, st, c, cached, req.Path, acc, dl, map[string]bool{})
	if errors.Is(err, kernel.ErrNotFound) {
		// "<archive>!/<entry>" names an entry inside an archive file; a path
		// that exists as an item is served as one first.
		if archive, entry, ok := strings.Cut(req.Path, "!/"); ok {
			a, err := Lookup(ctx, st, c, cached, archive, acc, dl, map[string]bool{})
			if err != nil {
				httpkit.WriteErr(w, r, ItemNotFound())
				return
			}
			if !acc.File(a.Repo, a.Item.FullPath(), accessapi.ActionRead) {
				httpkit.WriteErr(w, r, httpkit.Denied(p))
				return
			}
			s.getArchiveEntry(w, r, st, a, entry)
			return
		}
		s.getSidecar(w, r, st, c, cached, req.Path, acc, dl)
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "lookup item", err))
		return
	}
	if !acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if ok, err := matchesMatrix(ctx, st, f.Item, req); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "item properties", err))
		return
	} else if !ok {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	if f.Item.Folder {
		s.listFolder(w, r, st, c, f, acc)
		return
	}
	s.ServeItemFile(w, r, st, f, p)
}

// matchesMatrix applies download matrix parameters (model.Matrix.Matches).
func matchesMatrix(
	ctx context.Context,
	st Store,
	it model.Item,
	req ItemRequest,
) (bool, error) {
	if len(req.Props) == 0 {
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
// client sends one. It answers conditional requests and ranges as a remote
// repository's checksum file does (serveChecksum).
func (s *Kit) getSidecar(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	c repo.Config,
	cached bool,
	p string,
	acc Access,
	dl *Download,
) {
	artifact, alg, ok := item.Sidecar(p)
	if !ok {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	f, err := Lookup(r.Context(), st, c, cached, artifact, acc, dl, map[string]bool{})
	// A file the caller may not read has no visible checksum either.
	if err != nil || f.Item.Folder ||
		!acc.File(f.Repo, f.Item.FullPath(), accessapi.ActionRead) {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	value := item.ServedChecksum(f.Repo, f.Item, alg)
	if value == "" {
		// Artifactory 6.20's message (DownloadServiceImpl.respondForChecksumRequest).
		// 7.x evaluates the file's isNotModified before it looks the
		// checksum up, so a matching conditional request would get 304 for
		// a checksum it then cannot find; the 404 stays, as before [I].
		httpkit.WriteErr(w, r, apierror.New(http.StatusNotFound,
			"Checksum not found for "+f.Item.FullPath()))
		return
	}
	serveChecksum(w, r, p, value, f.Item.Actual, f.Item.Modified)
}

// contentTypeProperty is the property whose first value, when not blank,
// is the Content-Type a file downloads with (7.x's CONTENT_TYPE_PROP_KEY,
// RTFACT-4653).
const contentTypeProperty = "artifactory.content-type"

// fileContentType is the Content-Type a file downloads with on the item
// route, GET and HEAD: its contentTypeProperty value, as 7.x's
// DbStoringRepoMixin.setMimeType puts it on the resource its download
// answers from, otherwise the type recorded from its path. 7.x's storage
// info, deploy answer, archive entries and signed URLs type a file by its
// path alone, and so do this server's.
func fileContentType(ctx context.Context, st Store, it model.Item) (string, error) {
	props, err := st.Items().Properties(ctx, it.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		// A file served without an item of its own, such as a remote
		// file a format route describes from its cache, has no properties.
		return it.MimeType, nil
	}
	if err != nil {
		return "", fmt.Errorf("properties of %s: %w", it.FullPath(), err)
	}
	if values := props[contentTypeProperty]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
		return values[0], nil
	}
	return it.MimeType, nil
}

// checksumType is the Content-Type of the checksum file at name: the MIME
// table's type of its path on a GET, which 7.x's sendBodyResponseForChecksum
// sends, application/x-checksum for .sha1, .sha256 and .md5 and the
// table's default for .sha512, which 7.x has no row for; and
// application/x-checksum on a HEAD, ChecksumResource's type, for all four
// [I for .sha512, which 7.x does not generate].
func checksumType(r *http.Request, name string) string {
	if r.Method == http.MethodHead {
		return "application/x-checksum"
	}
	return item.MimeType(name)
}

// ServeFile streams a file's content, typed by its path, with the headers
// the JFrog CLI reads (Content-Length, Accept-Ranges and the X-Checksum
// headers); ranges on a GET and conditional requests are
// http.ServeContent's, less the two Artifactory's download never evaluates
// (withoutUnevaluatedPreconditions). A Content-Disposition already in w's
// headers is kept, so a protocol can send its own form. The native APIs
// and the Docker registry serve their files with it.
func (s *Kit) ServeFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
) {
	s.serveFile(w, r, st, f, p, f.Item.MimeType)
}

// ServeItemFile is ServeFile for a download on the item route,
// /artifactory/<repo>/<path>, for every package type and repository
// class: the file is typed by its artifactory.content-type property when
// that is set (fileContentType). 7.x answers that route through
// RepoFilter and DownloadServiceImpl.process, which take the type from the
// resource the repository's getInfo returns, typed by the property in
// DbStoringRepoMixin.setMimeType for a local or cache repository of any
// package type, and send it on a GET and a HEAD (RequestResponseHelper).
// The native APIs and the Docker registry keep ServeFile's type: their
// 7.x code is not in the OSS sources, and JFrog's docs do not name them.
func (s *Kit) ServeItemFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
) {
	contentType, err := fileContentType(r.Context(), st, f.Item)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "item properties", err))
		return
	}
	s.serveFile(w, r, st, f, p, contentType)
}

func (s *Kit) serveFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
	contentType string,
) {
	ctx := r.Context()
	it := f.Item
	// Before the redirect, so that a redirecting repository answers the
	// conditional request as a streamed download does.
	r = downloadRequest(r)
	h := w.Header()
	pkgroute.SetChecksums(h, it.Actual)
	h.Set("X-Artifactory-Filename", it.Name)
	h.Set("Content-Type", contentType)
	if h.Get("Content-Disposition") == "" {
		h.Set("Content-Disposition", blob.Disposition(it.Name))
	}
	if it.Actual.SHA1 != "" {
		// Quoted for ServeContent's comparisons; the client receives it
		// bare, as 7.x sends it (bareETagWriter).
		h.Set("ETag", `"`+it.Actual.SHA1+`"`)
		w = &bareETagWriter{ResponseWriter: w, sha1: it.Actual.SHA1}
		r = quoteETag(r, it.Actual.SHA1)
	}
	if s.redirect(w, r, st, f, p) {
		return
	}
	content, _, err := s.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		// The index names content the blob store does not hold: data loss,
		// not a client error.
		httpkit.WriteErr(w, r, s.InternalError(ctx, "open blob", err))
		return
	}
	defer func() { _ = content.Close() }()
	sw := &StatusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, it.Name, it.Modified, content)
	if sw.Complete(r) {
		s.recordCacheUse(ctx, f)
		if countsDownload(r, sw.status) {
			recordDownload(ctx, st, f, p)
		}
	}
}

// StatusWriter records the status ServeContent chose, so a 304 or a later
// range of a split download is not counted as a download.
type StatusWriter struct {
	http.ResponseWriter
	status    int
	bytes     int64
	hasFailed bool
}

// WriteHeader records code before it writes it.
func (s *StatusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Write counts what it writes, and records a failure to write.
func (s *StatusWriter) Write(b []byte) (int, error) {
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
func (s *StatusWriter) ReadFrom(r io.Reader) (int64, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := io.Copy(s.ResponseWriter, r)
	s.bytes += n
	s.hasFailed = s.hasFailed || err != nil
	return n, err //nolint:wrapcheck // a pass-through writer
}

// Complete excludes truncated bodies even when their headers already said 200.
func (s *StatusWriter) Complete(r *http.Request) bool {
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
func (s *StatusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// startsAtZero reports whether a Range header starts at the first byte, so a
// split download counts once rather than once per part.
func startsAtZero(rng string) bool { return strings.HasPrefix(strings.TrimSpace(rng), "bytes=0-") }

// listFolder answers a folder GET with a plain HTML index, as Artifactory
// serves browsers. named is the repository the request named, a virtual
// repository when f was found in one of its members.
func (s *Kit) listFolder(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	named repo.Config,
	f Found,
	acc Access,
) {
	ctx := r.Context()
	folder := f.Item.FullPath()
	var children []model.Item
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		batch, next, err := st.Items().Children(ctx, f.Repo.Key, f.Item.Cached, folder, page)
		if err != nil {
			httpkit.WriteErr(w, r, s.InternalError(ctx, "list folder", err))
			return
		}
		children = append(children, batch...)
		if next == "" {
			break
		}
		page.After = next
	}
	title := html.EscapeString(path.Join(f.Repo.Key, folder))
	var b strings.Builder
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html><head><title>Index of %s</title></head><body>\n", title)
	fmt.Fprintf(&b, "<h1>Index of %s</h1>\n<pre>", title)
	patterns := []repo.RepoPatterns{repo.PatternsOf(f.Repo)}
	if named.Key != f.Repo.Key {
		patterns = append(patterns, repo.PatternsOf(named))
	}
	for _, c := range children {
		// Children the caller may not see are left out; a folder shows when
		// something below it is readable. So are children the patterns
		// reject, the member's and the virtual repository's, as
		// Artifactory's browsing leaves them out
		// (TreeBrowsingCriteriaBuilder.applyRepoIncludeExclude,
		// RepositoryBrowsingServiceImpl.virtualRepoAccepts).
		if !acc.Item(f.Repo, c.FullPath(), c.Folder, accessapi.ActionRead) ||
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
func acceptedByAll(patterns []repo.RepoPatterns, p string, folder bool) bool {
	for _, pat := range patterns {
		if !pat.Accepts(p, folder) {
			return false
		}
	}
	return true
}

func remoteDownloadType(c repo.Config, cached bool) bool {
	return c.Class == repo.Remote &&
		(c.PackageType == repo.Generic && !cached || maven.Serves(c.PackageType) ||
			c.PackageType == repo.P2 ||
			(alpine.Serves(c.PackageType) || debian.Serves(c.PackageType) ||
				rpm.Serves(c.PackageType)) && !cached)
}

// DownloadURI is the URL an item downloads from under the key key.
func DownloadURI(ctx context.Context, key, fullPath string) string {
	return baseurl.From(ctx) + "/artifactory/" + key + "/" + httpkit.EscapePath(fullPath)
}

// SplitRepoPath splits an escaped "<repo>/<path>" into the key and the
// unescaped path; folder reports a trailing slash.
func SplitRepoPath(escaped string) (key, p string, folder bool, err error) {
	folder = strings.HasSuffix(escaped, "/")
	segs := strings.Split(strings.Trim(escaped, "/"), "/")
	for i, seg := range segs {
		if segs[i], err = url.PathUnescape(seg); err != nil {
			return "", "", false, httpkit.BadRequestf("malformed path: %v", err)
		}
	}
	key, p = segs[0], strings.Join(segs[1:], "/")
	if key == "" {
		return "", "", false, httpkit.BadRequestf("a repository key is required")
	}
	if p != "" {
		if _, _, _, err := kernel.SplitItemPath(p); err != nil {
			return "", "", false, httpkit.BadRequestf("malformed path %q", p)
		}
	}
	return key, p, folder, nil
}

// getArchiveEntry serves one entry of an archive file: the part of an item
// path after "!/" (decision 0002). An archive whose blob is lost answers
// 500, as its own download does, and so does one whose blob fails a read
// before the entry's status is sent, where returning without a write would
// send an empty 200. Once the entry's status is sent, a failure can only be
// logged.
func (s *Kit) getArchiveEntry(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	entry string,
) {
	ctx := r.Context()
	if f.Item.Folder {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	sw := &StatusWriter{ResponseWriter: w}
	err := s.Items(st).ArchiveEntry(ctx, f.Item, entry, func(size int64, rd io.Reader) error {
		h := sw.Header()
		h.Set("Content-Type", item.MimeType(entry))
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		h.Set("X-Artifactory-Filename", path.Base(entry))
		sw.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return nil
		}
		_, err := io.Copy(sw, rd)
		return err //nolint:wrapcheck // the status is sent; only logged
	})
	if err == nil {
		return
	}
	switch {
	case sw.status != 0:
		_ = s.InternalError(ctx, "archive entry", err)
	case errors.Is(err, item.ErrNotFound):
		httpkit.WriteErr(w, r, ItemNotFound())
	default:
		httpkit.WriteErr(w, r, s.InternalError(ctx, "archive entry", err))
	}
}
