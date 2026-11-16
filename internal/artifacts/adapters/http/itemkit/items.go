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
	"slices"
	"strconv"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
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
	// member is whether a virtual repository reads the file through a
	// member's key (MemberRead), which keeps the virtual's answers.
	member bool
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
		// The deploy lost its content to a collection that ran beside it,
		// and the message asks for the deploy again, so the 503 reports a
		// transient condition and hints a retry, as decision 0043's "Rate
		// headers and Retry-After" asks (#1415). No 7.x source documents
		// this answer [I].
		e := apierror.New(http.StatusServiceUnavailable, err.Error())
		e.RetryAfterSeconds = 1
		return e
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

// DeployTarget resolves where a deploy to c lands, for every route that
// publishes a file: a local or federated repository itself, or a virtual
// repository's default deployment repository (virtual.DeployTarget), on
// whose path the caller then checks Write. A virtual repository without a
// local one answers NoDeployTarget. Remote repositories and their caches
// take no deploys; this is the native protocols' refusal of them, and the
// item routes' PUT answers 7.x's own (ItemDeployTarget).
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
	case c.Class == repo.Virtual:
		target, err := virtual.DeployTarget(ctx, st.Repos(), c)
		if errors.Is(err, virtual.ErrNoDeployTarget) {
			return c, NoDeployTarget(c.Key)
		}
		if err != nil {
			return c, fmt.Errorf("deploy target: %w", err)
		}
		return target, nil
	}
	return c, nil
}

// ItemDeployTarget is DeployTarget for a PUT on the item routes, which
// answers a remote repository, or its cache, 404 "Could not find a local
// repository named <key> to deploy to.", naming the key as sent: 7.x's
// upload looks a key that names no virtual repository up among local and
// federated repositories alone (UploadServiceUtils.getTargetRepository
// 248-261), which hold neither, and then answers
// UploadServiceImpl.sendInvalidTargetRepositoryError's 404 (337-352). A P2
// repository keeps DeployTarget's refusal.
func ItemDeployTarget(
	ctx context.Context,
	st Store,
	c repo.Config,
	cached bool,
) (repo.Config, error) {
	if c.PackageType != repo.P2 && (cached || c.Class == repo.Remote) {
		key := c.Key
		if cached {
			key += model.CacheSuffix
		}
		return c, apierror.New(http.StatusNotFound,
			"Could not find a local repository named "+key+" to deploy to.")
	}
	return DeployTarget(ctx, st, c, cached)
}

// NoDeployTarget is 7.x's answer to a deploy to the virtual repository key
// when it has no local deployment repository: 405 with Allow: GET and
// UploadServiceImpl.sendInvalidTargetRepositoryError's message (7.161.26,
// 337-352; decision 0008, R6).
func NoDeployTarget(key string) error {
	e := apierror.New(http.StatusMethodNotAllowed, "No local repository was configured as "+
		"local deployment repository for the ("+key+") virtual repository.")
	e.Allow = http.MethodGet
	return e
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
	// remote is set when a walk through a virtual repository found the
	// file through the remote member Repo's upstream step: Repo's read,
	// which the route answers as a direct read of Repo would
	// (serveRemoteRead), in place of Item.
	remote *remoteRead
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
	// upstream reads a remote member's upstream in a walk through a
	// virtual repository (lookupMember), and judges whether a copy its
	// cache holds has expired; nil passes over every upstream step, as a
	// walk that is not the request's own file does (walkStep), and serves
	// every cached copy.
	upstream *upstreamStep
	// expired holds, by member key, the copy a remote member's cache step
	// passed over as expired, with how it matched, for the member's
	// upstream step to fall back on (expiredCopy).
	expired map[string]expiredCopy
}

// expiredCopy is a copy a remote member's cache step passed over as
// expired, and how it matched the request.
type expiredCopy struct {
	found  Found
	served memberAnswer
}

// newDownload checks p as Artifactory does: its request parsing splits an
// archive entry ("<archive>!/<entry>") off the archive's path
// (ArtifactoryRequestBase.zipResourcePath), and RepoBase.accepts checks a
// checksum sidecar as the file it describes. folder is whether the request
// ended in a slash; m are its matrix parameters, which a resolution filters
// by as the download filters what it serves.
func newDownload(p string, folder bool, m model.Matrix, resolve resolver) *Download {
	if file := resourcePath(p); file != p {
		p, folder = file, false
	}
	return &Download{path: p, isFolder: folder, matrix: m, resolve: resolve}
}

// Lookup finds the item at p for a download from c: a local repository's
// own tree, a remote repository's cache (GetItem asks the upstream for
// what the cache lacks), or, for a virtual repository, the first of its
// members, in order (virtual.Members), that serves it. A non-nil dl
// refuses what Artifactory refuses in each repository it reaches, and
// rewrites p as each repository that holds items resolves it before
// looking there; through a virtual repository it walks the members as a
// download does (lookupMember), and a miss carries the walk's Resolution
// for the answer (unfound). The storage API passes nil, with
// virtual.Stored, and reads the members the caller may read.
func Lookup(
	ctx context.Context,
	st Store,
	c repo.Config,
	cached bool,
	p string,
	acc Access,
	dl *Download,
	order virtual.Order,
) (Found, error) {
	if dl != nil {
		if err := generic.CheckDownload(c, dl.path, dl.isFolder); err != nil {
			dl.denied(c, err)
			return Found{}, err //nolint:wrapcheck // a RejectionError is the answer
		}
	}
	if c.Class == repo.Virtual {
		return lookupMembers(ctx, st, c, p, acc, dl, order)
	}
	f, err := lookupStored(ctx, st, c, cached, p, acc, dl)
	if policyRefusal(err) != nil {
		// Maven's release and snapshot handling, which the format checks
		// on the path it resolves.
		dl.denied(c, err)
	}
	return f, err
}

// lookupStored is Lookup in c, a repository that holds items: a local or
// federated repository, or a remote repository's cache.
func lookupStored(
	ctx context.Context,
	st Store,
	c repo.Config,
	cached bool,
	p string,
	acc Access,
	dl *Download,
) (Found, error) {
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

// lookupMembers is Lookup through the virtual repository c: its real
// members in order, nested virtual repositories flattened, as 7.161.26's
// processStandard walks the list assembleSearchRepositoriesList gives it.
// A nested virtual repository whose patterns reject the download's path is
// left out by Members, as it filters them.
//
// c itself is never asked for Read: no permission target can name a
// virtual repository (decision 0006), and 7.x asks each member in the walk
// instead (RealRepoBase.checkDownloadIsAllowed), so what the caller may
// read through c is what it may read in the members.
func lookupMembers(
	ctx context.Context,
	st Store,
	c repo.Config,
	p string,
	acc Access,
	dl *Download,
	order virtual.Order,
) (Found, error) {
	o := virtual.Options{Order: order}
	if dl != nil {
		o.Path, o.Folder = dl.path, dl.isFolder
	}
	members, err := virtual.Members(ctx, st.Repos(), c, o)
	if err != nil {
		return Found{}, fmt.Errorf("members of %s: %w", c.Key, err)
	}
	if dl == nil {
		return lookupReadable(ctx, st, members, p, acc)
	}
	if len(members) == 0 && !dl.isFolder {
		// No real member is left to search: none is configured, each is
		// c itself or a virtual repository reached already, or a nested
		// virtual repository's patterns reject the path. 7.161.26 answers
		// a file as it answers c's own patterns rejecting the path
		// (getInfoFromSearchableRepositories 158-169), and so does this; a
		// folder request is the directory listing's, which keeps its miss
		// (unfound).
		dl.stepf("Unable to find aggregated repositories to search within - returning " +
			"unfound resource")
		return Found{}, format.Rejectf(http.StatusNotFound,
			"No repository found to serve the request for %s:%s", c.Key, dl.path)
	}
	var res virtual.Resolution
	var closest *Found
	for _, m := range members {
		// A remote member's upstream step reads it as a direct read would
		// (dl.upstream): only a file request takes one, and only the walk
		// for the file the request names, since 7.x's walk looks for that
		// resource alone (DownloadRequestContext.getResourcePath).
		if m.Upstream && (dl.upstream == nil || dl.isFolder) {
			continue
		}
		// 7.x names each step by the repository it asks: a remote's cache
		// is the repository "<key>-cache" (DbCacheRepo's descriptor), and
		// its upstream step the remote itself.
		key := m.Repo.Key
		if m.Repo.Class == repo.Remote && !m.Upstream {
			key += model.CacheSuffix
		}
		dl.stepf("Searching for the resource within %s", key)
		// Once a file lacks a matrix parameter the request names, the walk
		// looks on for one that carries it only where its properties may
		// differ: a local member, or a remote one that synchronizes them
		// (processStandard, isNotSynchronizeProperties 479-481).
		if closest != nil && m.Repo.Class == repo.Remote &&
			(m.Repo.SynchronizeProperties == nil || !*m.Repo.SynchronizeProperties) {
			continue
		}
		f, served, err := lookupMember(ctx, st, m, p, acc, dl, &res)
		switch {
		case err != nil:
			return f, err
		case served == exactMatch:
			return f, nil
		case served == closestMatch && closest == nil:
			// processStandard says it returns the first such file; its
			// loop keeps the last, which this takes for a slip.
			dl.stepf("Resource is not an exact match - keeping as closest match")
			closest = &f
		}
	}
	if closest != nil {
		dl.stepf("Unable to find an exact matching resource - returning closest match")
		return *closest, nil
	}
	return Found{}, &memberMiss{res: res}
}

// memberAnswer is how a member answers one step of a download's walk.
type memberAnswer int

const (
	// passedOver: the member does not serve the path.
	passedOver memberAnswer = iota
	// exactMatch: the member serves the path, carrying every matrix
	// parameter the request names, if any.
	exactMatch
	// closestMatch: the member serves the path but lacks a matrix
	// parameter the request names (PropertiesImpl.matchQuery's NO_MATCH).
	closestMatch
)

// lookupReadable is the storage API's walk of members: the first that the
// caller may read at p and that holds the item.
func lookupReadable(
	ctx context.Context,
	st Store,
	members []virtual.Member,
	p string,
	acc Access,
) (Found, error) {
	for _, m := range members {
		if m.Upstream || !acc.Folder(m.Repo, p, accessapi.ActionRead) {
			continue
		}
		f, err := lookupStored(ctx, st, m.Repo, false, p, acc, nil)
		if !errors.Is(err, kernel.ErrNotFound) {
			return f, err
		}
	}
	return Found{}, kernel.ErrNotFound
}

// lookupMember is one step of a download's walk through a virtual
// repository: step's member m, a real member, in the order 7.161.26's
// member answers it (DbLocalRepo.checkDownloadIsAllowed 212-219,
// RemoteRepoBase.checkDownloadIsAllowed 744-758, then getInfo), each
// refusal traced as RealRepoBase.createUnfoundRepoResource 224-227 logs it.
// When m does not serve the download, the walk goes on. A remote member's
// upstream step makes the same checks, Read on the file included, before
// dl.upstream asks the upstream anything.
//
//   - The download policy first: a refusal passes over silently, since 7.x
//     answers it 404 and only a 403 counts toward the walk's answer. A
//     request to the member itself checks Read first instead (DV-057), so
//     that a caller learns nothing of a repository it may not read; in a
//     walk the pass-over tells it nothing either.
//   - Then Read on the path, which res keeps when it is refused (R3), the
//     path a file request names whether m holds it or not, as
//     checkDownloadIsAllowed checks it before getInfo looks.
//   - Then the item. Matrix parameters it conflicts with pass over too, as
//     DbStoringRepoMixin.getInfo answers that unfound; one it lacks makes
//     it the closest match.
func lookupMember(
	ctx context.Context,
	st Store,
	step virtual.Member,
	p string,
	acc Access,
	dl *Download,
	res *virtual.Resolution,
) (Found, memberAnswer, error) {
	m := step.Repo
	if err := generic.CheckDownload(m, dl.path, dl.isFolder); err != nil {
		dl.stepf("Download denied (%s) - returning un-found resource", err)
		return Found{}, passedOver, nil
	}
	file := resourcePath(p)
	if !acc.Folder(m, file, accessapi.ActionRead) {
		deny(ctx, dl, res, m, file)
		return Found{}, passedOver, nil
	}
	if step.Upstream {
		if !acc.File(m, file, accessapi.ActionRead) {
			deny(ctx, dl, res, m, file)
			return Found{}, passedOver, nil
		}
		return dl.upstream.read(ctx, dl, m, res)
	}
	f, err := lookupStored(ctx, st, m, false, p, acc, dl)
	var rejected *format.RejectionError
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		if !dl.isFolder && !acc.File(m, file, accessapi.ActionRead) {
			deny(ctx, dl, res, m, file)
		}
		return Found{}, passedOver, nil
	case errors.As(err, &rejected):
		// The format's refusal of the path in m, such as Maven's release
		// and snapshot policy, which assertValidPath checks with the
		// patterns.
		dl.stepf("Download denied (%s) - returning un-found resource", rejected)
		return Found{}, passedOver, nil
	case err != nil:
		return Found{}, passedOver, err
	}
	if !acc.Item(m, f.Item.FullPath(), f.Item.Folder, accessapi.ActionRead) {
		deny(ctx, dl, res, m, f.Item.FullPath())
		return Found{}, passedOver, nil
	}
	served := exactMatch
	if len(dl.matrix.Props) > 0 {
		props, err := st.Items().Properties(ctx, f.Item.ID)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return Found{}, passedOver, fmt.Errorf("properties of %s: %w", f.Item.FullPath(), err)
		}
		if !dl.matrix.Matches(props) {
			dl.stepf("Request query properties conflict with those that annotate the artifact - " +
				"returning unfound resource")
			return Found{}, passedOver, nil
		}
		if !carriesEveryKey(dl.matrix, props) {
			dl.stepf("Request query properties don't match those that annotate the artifact")
			served = closestMatch
		}
	}
	// A remote's cache step checks its copy for expiry once it has found
	// it (DbCacheRepo.getInfo, 7.161.26): an expired copy is passed over to
	// the remote's upstream step, which revalidates it as a direct read of
	// the remote does, or serves it when the upstream does not find the
	// file (fallBack).
	if m.Class == repo.Remote && !f.Item.Folder && dl.upstream != nil {
		dl.stepf("Found the resource in the cache - checking for expiry")
		expired, err := dl.upstream.expired(ctx, m, f.Item)
		if err != nil {
			return Found{}, passedOver, err
		}
		if expired {
			dl.stepf("Returning resource as expired")
			if dl.expired == nil {
				dl.expired = map[string]expiredCopy{}
			}
			dl.expired[m.Key] = expiredCopy{found: f, served: served}
			return Found{}, passedOver, nil
		}
		dl.stepf("Returning cached resource")
	}
	res.Found = true
	return f, served, nil
}

// carriesEveryKey reports whether props carry every key m asks a value of.
// A file that lacks one still serves the request (Matches), but
// PropertiesImpl.matchQuery calls it NO_MATCH, not an exact match, and a
// walk prefers a later member's file that carries it; a blank value asks
// for nothing (clearBlankAndReturnPropertyValues).
func carriesEveryKey(m model.Matrix, props model.Properties) bool {
	for k, want := range m.Props {
		if _, ok := props[k]; ok {
			continue
		}
		if slices.ContainsFunc(want, func(v string) bool { return strings.TrimSpace(v) != "" }) {
			return false
		}
	}
	return true
}

// deny keeps m's refusal of Read on p in res, under the key 7.161.26's
// refusal names: a remote member's cache, where it keeps a remote's
// permissions (RemoteRepoBase.checkDownloadIsAllowed). Each refusal is
// traced, and the one kept is named as processStandard names it.
func deny(ctx context.Context, dl *Download, res *virtual.Resolution, m repo.Config, p string) {
	key := m.Key
	if m.Class == repo.Remote {
		key += model.CacheSuffix
	}
	caller, _ := kernel.FromContext(ctx)
	d := virtual.Denial{Member: key, Path: p, User: caller.Name}
	dl.stepf("Download denied (%s) - returning un-found resource", d.Message())
	if res.Deny(d) {
		dl.stepf("Request is forbidden by %s", m.Key)
	}
}

// memberMiss is a download's walk through a virtual repository that no
// member served. It is kernel.ErrNotFound to every caller, as the miss it
// is, and keeps the walk's Resolution for the answer (unfound).
type memberMiss struct{ res virtual.Resolution }

func (e *memberMiss) Error() string { return kernel.ErrNotFound.Error() }

func (e *memberMiss) Unwrap() error { return kernel.ErrNotFound }

// GetItem answers GET and HEAD on an item path: file content, a checksum
// sidecar, or a folder listing; with ?trace, the resolution instead.
func (s *Kit) GetItem(w http.ResponseWriter, r *http.Request) {
	// A read an overlay makes through GetItem while a trace runs it, such
	// as a P2 virtual repository's member file, is decided, not traced again.
	if _, ok := r.URL.Query()["trace"]; ok && overlayTraceOf(r) == nil {
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
	req.member, _ = ctx.Value(memberReadKey{}).(bool)
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
	// A folder check first: the path may be a file or a folder. The exact
	// right is checked once the item is found, and in a virtual repository,
	// whose members decide, in each member (Admits). Artifactory refuses a
	// blacked-out repository or an excluded path before it checks
	// permissions; this answers a caller who may not read the path without
	// telling it how the repository is configured (DV-057).
	if ok, err := acc.Admits(ctx, st.Repos(), c, req.Path); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "read gate", err))
		return
	} else if !ok {
		httpkit.WriteErr(w, r, refused(c, req, p))
		return
	}
	if s.getRemoteFolder(w, r, c, cached, req, p, acc) {
		return
	}
	alias, err := aliasRemoteMetadata(ctx, st, c, cached, req.Path)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "cached document", err))
		return
	}
	if (remoteDownloadType(c, cached) || alias || fileChecksumRoute(c, cached, req.Path)) &&
		req.Path != "" &&
		!req.IsFolder &&
		s.RemoteSources != nil {
		if s.getRemoteItem(w, r, c, req, p, acc) {
			return
		}
	}
	dl := newDownload(req.Path, req.IsFolder, req.matrix(), s.Items(st).Resolve)
	dl.upstream = s.walkStep(r, req, p, acc)
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
	f, err := Lookup(ctx, st, c, cached, req.Path, acc, dl, virtual.Resolve)
	if errors.Is(err, kernel.ErrNotFound) {
		s.getUnfoundItem(w, r, st, c, cached, req, p, acc, dl, err)
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "lookup item", lookupRefusal(c, req, err)))
		return
	}
	if f.remote != nil {
		s.serveRemoteRead(w, r, f.Repo, c, memberRequest(req, f.Repo), p, *f.remote)
		return
	}
	if !acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, accessapi.ActionRead) {
		httpkit.WriteErr(w, r, refused(c, req, p))
		return
	}
	if ok, err := matchesMatrix(ctx, st, f.Item, req); err != nil {
		httpkit.WriteErr(w, r, s.InternalError(ctx, "item properties", err))
		return
	} else if !ok {
		httpkit.WriteErr(w, r, mismatched(c, req))
		return
	}
	if f.Item.Folder {
		s.listFolder(w, r, st, c, f, acc)
		return
	}
	s.ServeItemFile(w, r, st, f, p)
}

// getUnfoundItem answers req, a path the lookup did not find in c: a cold
// remote file, an entry of an archive file, a checksum, or the miss, which
// is miss, the lookup's error.
func (s *Kit) getUnfoundItem(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	c repo.Config,
	cached bool,
	req ItemRequest,
	p kernel.Principal,
	acc Access,
	dl *Download,
	miss error,
) {
	// 7.161.26's checkDownloadIsAllowed checks Read on the file before
	// getInfo looks for it, so a file the folder check let through but the
	// caller may not read is refused whether it exists or not.
	if fileRequest(c, req) && !acc.File(c, resourcePath(req.Path), accessapi.ActionRead) {
		httpkit.WriteErr(w, r, refused(c, req, p))
		return
	}
	if s.getColdRemoteItem(w, r, st, c, cached, req, p, acc, dl) {
		return
	}
	// "<archive>!/<entry>" names an entry inside an archive file; a path
	// that exists as an item is served as one first. The archive is looked
	// for as a download of it: through a virtual repository a remote
	// member's upstream step fetches it, and a remote fetches what its
	// cache lacks (remoteArchive).
	if archive, entry, ok := strings.Cut(req.Path, "!/"); ok {
		dl.upstream = s.archiveStep(r, req, archive, p, acc)
		a, err := Lookup(r.Context(), st, c, cached, archive, acc, dl, virtual.Resolve)
		if errors.Is(err, kernel.ErrNotFound) {
			fetched, read, answer := s.remoteArchive(r, c, cached, req, archive, entry, p, acc, nil)
			switch {
			case !read:
				httpkit.WriteErr(w, r, unfound(c, cached, req, p, err))
				return
			case answer != nil:
				httpkit.WriteErr(w, r, answer)
				return
			}
			a, err = fetched, nil
		}
		if err != nil {
			httpkit.WriteErr(w, r, containedRefusal(c, req, err))
			return
		}
		if !acc.File(a.Repo, a.Item.FullPath(), accessapi.ActionRead) {
			httpkit.WriteErr(w, r, refused(c, req, p))
			return
		}
		s.getArchiveEntry(w, r, st, a, entry)
		return
	}
	s.getSidecar(w, r, st, c, cached, req, p, acc, dl, miss)
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
// repository's checksum file does (serveChecksum). It answers miss, the
// lookup's failure to find req, when req names no checksum.
func (s *Kit) getSidecar(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	c repo.Config,
	cached bool,
	req ItemRequest,
	caller kernel.Principal,
	acc Access,
	dl *Download,
	miss error,
) {
	p := req.Path
	artifact, alg, ok := item.Sidecar(p)
	if !ok {
		httpkit.WriteErr(w, r, unfound(c, cached, req, caller, miss))
		return
	}
	// The file the checksum names is looked for as the file itself is:
	// through a virtual repository, a remote member's upstream step reads
	// the checksum as a direct read of the member would (#1535).
	dl.upstream = s.fileStep(r, req, caller, acc)
	f, err := Lookup(r.Context(), st, c, cached, artifact, acc, dl, virtual.Resolve)
	if errors.Is(err, kernel.ErrNotFound) {
		httpkit.WriteErr(w, r, unfound(c, cached, req, caller, err))
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, containedRefusal(c, req, err))
		return
	}
	if f.remote != nil {
		s.serveRemoteRead(w, r, f.Repo, c, memberRequest(req, f.Repo), caller, *f.remote)
		return
	}
	if f.Item.Folder {
		httpkit.WriteErr(w, r, ItemNotFound())
		return
	}
	// A file the caller may not read has no visible checksum either.
	if !acc.File(f.Repo, f.Item.FullPath(), accessapi.ActionRead) {
		httpkit.WriteErr(w, r, checksumRefused(c, req, caller))
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
	serveChecksum(w, r, f.Repo, c, p, value, f.Item.Actual, f.Item.Modified)
}

// contentTypeProperty is the property whose first value, when not blank,
// is the Content-Type a file downloads with (7.x's CONTENT_TYPE_PROP_KEY,
// RTFACT-4653).
const contentTypeProperty = "artifactory.content-type"

// fileContentType is the Content-Type a file downloads with on the item
// route and from the native APIs that type by it (ServeItemFile), GET and
// HEAD: its contentTypeProperty value, as 7.x's
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
// (withoutUnevaluatedPreconditions). The file is named as the item route
// names it (ItemDisposition, FileDisposition); a Content-Disposition
// already in w's headers is kept, so a protocol can send its own form. The
// Docker registry's blobs and the native APIs that keep the path's type
// serve their files with it (ProtocolOptions, #1396).
func (s *Kit) ServeFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
) {
	s.serveFile(w, r, st, f, p, f.Item.MimeType, ItemDisposition)
}

// ServeItemFile is ServeFile for a download on the item route,
// /artifactory/<repo>/<path>, for every package type and repository
// class: the file is typed by its artifactory.content-type property when
// that is set (fileContentType). 7.x answers the item route through
// RepoFilter and DownloadServiceImpl.process, which take the type from the
// resource the repository's getInfo returns, typed by the property in
// DbStoringRepoMixin.setMimeType for a local or cache repository of any
// package type, and send it on a GET and a HEAD (RequestResponseHelper).
func (s *Kit) ServeItemFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
) {
	s.serveTypedFile(w, r, st, f, p, ItemDisposition)
}

// ServePackageFile is ServeItemFile for a native API whose 7.x downloads
// go through the package download service (ProtocolOptions): typed by the
// property as the item route types a file, and named by
// PackageDisposition, without the item route's filename* part. 7.161.26's
// PackageArtifactServiceImpl hands the request to the item route's
// DownloadServiceImpl.process, but with a JerseyArtifactoryResponse, whose
// setContentDispositionAttachment writes the quoted name alone where the
// item route's HttpArtifactoryResponse adds filename* (#1505).
func (s *Kit) ServePackageFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
) {
	s.serveTypedFile(w, r, st, f, p, PackageDisposition)
}

// serveTypedFile is serveFile typed by fileContentType, naming the file
// with form where its repository wants a Content-Disposition
// (FileDisposition).
func (s *Kit) serveTypedFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
	form func(string) string,
) {
	contentType, err := fileContentType(r.Context(), st, f.Item)
	if err != nil {
		httpkit.WriteErr(w, r, s.InternalError(r.Context(), "item properties", err))
		return
	}
	s.serveFile(w, r, st, f, p, contentType, form)
}

// PackageDisposition is the Content-Disposition 7.x's package download
// service names a file with on a repository that does not allow content
// browsing (FileDisposition). JerseyArtifactoryResponse writes
// attachment; filename="<name>" with the name as it is and no filename*
// part (7.161.26). A space stays a space: on 7.59.9 a download's saved
// name had %20 for it, which JFrog took as a fault and fixed
// (RTFACT-29749). 7.x refuses a path holding a quote or a backslash
// (PathValidator), so it never writes either; here they are escaped as
// quoted pairs, so that such a name cannot end the quoted string early.
//
// A name with a character outside printable ASCII is named by filename*
// alone instead: on a GET and a HEAD, whichever response type answers,
// RequestResponseHelper.setHeaderForNonAscii replaces the header with an
// attachment whose only parameter is filename* in UTF-8, every byte but
// RFC 3986's unreserved characters percent-encoded (Spring's
// UriUtils.encode). 7.x's 304 skips that step and keeps the quoted form;
// this sends filename* there too, as no source shows what a client reads
// from it [I].
func PackageDisposition(name string) string {
	if !httpkit.PrintableASCII(name) {
		return nonASCIIDisposition("attachment", name)
	}
	return `attachment; filename="` + httpkit.EscapeQuoted(name) + `"`
}

func (s *Kit) serveFile(
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	f Found,
	p kernel.Principal,
	contentType string,
	form func(string) string,
) {
	if traceServed(r, "Returning found resource from "+
		traceRepoPath(f.Repo, f.Item.Cached, f.Item.FullPath())) {
		return
	}
	ctx := r.Context()
	it := f.Item
	// Before the redirect, so that a redirecting repository answers the
	// conditional request as a streamed download does.
	r = downloadRequest(r)
	h := w.Header()
	pkgroute.SetChecksums(h, it.Actual)
	h.Set("X-Artifactory-Filename", FilenameHeader(it.Name))
	h.Set("Content-Type", contentType)
	// The repository that holds the file decides, a virtual repository's
	// member included: 7.x reads the flag of the response's repository.
	own := h.Get("Content-Disposition") != ""
	if !own {
		setDisposition(h, FileDisposition(f.Repo, it.Name, form))
	}
	if it.Actual.SHA1 != "" {
		// Quoted for ServeContent's If-Range; the client receives it bare,
		// as 7.x sends it (bareETagWriter).
		h.Set("ETag", `"`+it.Actual.SHA1+`"`)
		w = &bareETagWriter{ResponseWriter: w, sha1: it.Actual.SHA1}
	}
	// 7.x decides a download's 304 before it serves the file or redirects
	// (DownloadServiceImpl.respond and isNotModified, 7.161.26), from one
	// If-None-Match line at a time or else If-Modified-Since, which differs
	// from http.ServeContent's reading of "*", of a list of tags on one
	// line and of a second line (fileNotModified). So this decides, on the
	// request as it was sent, and ServeContent and the redirect never see
	// either header.
	if fileNotModified(r, it.Actual, it.Modified) {
		if !it.Modified.IsZero() {
			h.Set("Last-Modified", it.Modified.UTC().Format(http.TimeFormat))
		}
		if !own {
			setDisposition(h, notModifiedDisposition(f.Repo, it.Name, form))
		}
		pkgroute.WriteNotModified(w)
		return
	}
	r = withoutHeaders(r, "If-None-Match", "If-Modified-Since")
	if it.Actual.SHA1 != "" {
		r = quoteETag(r, it.Actual.SHA1)
	}
	if s.redirect(w, r, st, f, p) {
		return
	}
	browserView(h, r, f.Repo, it.Name)
	content, _, err := s.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		// The index names content the blob store does not hold: data loss,
		// not a client error.
		httpkit.WriteErr(w, r, s.InternalError(ctx, "open blob", err))
		return
	}
	defer func() { _ = content.Close() }()
	// A range that does not overlap answers 416 as 7.x's does: Content-Range
	// alone, without the file's headers or http.Error's text.
	sw := &StatusWriter{ResponseWriter: &pkgroute.RangeRefusalWriter{ResponseWriter: w}}
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

// fileChecksumRoute reports whether the checksum request at p on c, a
// remote whose files this route does not read from the upstream, names a
// file the CRAN or opkg item route fetches: a package, or an index that
// route serves. 7.161.26 answers a checksum request on a remote of any type
// from the file's info (DownloadServiceImpl.respondForChecksumRequest), so
// such a request takes getRemoteItem's checksum answer once the cache does
// not hold the file (#1495).
func fileChecksumRoute(c repo.Config, cached bool, p string) bool {
	if c.Class != repo.Remote || cached {
		return false
	}
	file, _, ok := item.Sidecar(p)
	if !ok {
		return false
	}
	switch {
	case cran.Serves(c.PackageType):
		_, _, index := cran.IndexPath(file)
		return index || cran.IsPackage(file)
	case opkg.Serves(c.PackageType):
		name := path.Base(file)
		return strings.HasSuffix(file, ".ipk") && opkg.ValidPath(file) ||
			opkg.IsIndex(file) && (name == opkg.PackagesName || name == opkg.PackagesGzipName)
	}
	return false
}

// coldReadType reports whether c is a remote whose item route asks the
// upstream for a file its cache does not hold once the cache lookup has
// not found it: every remote type but those remoteDownloadType reads
// through its source first, and CRAN and opkg, whose overlays fetch their
// packages and indexes. 7.161.26's RemoteRepoBase.getInfo and
// HttpRepo.retrieveInfo ask <url>/<path> whatever the package type, and
// JFrog's remote repositories page says a download at
// /artifactory/<remote>/<path> "returns the remote artifact and stores it
// in the cache if it isn't stored yet" (#1507).
func coldReadType(c repo.Config, cached bool) bool {
	return c.Class == repo.Remote && !cached && !remoteDownloadType(c, false) &&
		!cran.Serves(c.PackageType) && !opkg.Serves(c.PackageType)
}

// coldReadPath reports whether the item route asks c's upstream for the
// file at p. A path the format builds itself (format.ClassDerived, such as
// Helm's index.yaml) is one a remote reads from its cache alone
// (remote.Policy), so it keeps the cache lookup's 404 [I]. An npm remote
// asks only for a tarball (<name>/-/<file>): its upstream answers
// <url>/<name> with the package's document, and a copy of it kept as the
// file <name> would take the place of the folder that holds the package's
// tarballs, so a later tarball download through the npm route would fail.
// No 7.x source says what its npm remote does with such a path [I], so it
// keeps the cache lookup's 404 too.
func (s *Kit) coldReadPath(c repo.Config, p string) bool {
	if format.ClassOf(s.Format(c.PackageType), c, p) == format.ClassDerived {
		return false
	}
	return !npm.Serves(c.PackageType) || strings.Contains(p, "/-/")
}

// getColdRemoteItem answers a file, or the checksum of a file, that the
// cache lookup did not find on a remote of a coldReadType type, from the
// upstream at <url>/<path>, through getRemoteItem, where coldRemoteRead
// sends it.
func (s *Kit) getColdRemoteItem(
	w http.ResponseWriter, r *http.Request, st Store, c repo.Config, cached bool,
	req ItemRequest, p kernel.Principal, acc Access, dl *Download,
) bool {
	return s.coldRemoteRead(r.Context(), st, c, cached, req, acc, dl) &&
		s.getRemoteItem(w, r, c, req, p, acc)
}

// coldRemoteRead reports whether the item route reads req, which the cache
// lookup did not find, from c's upstream: on a remote of a coldReadType
// type, at a path coldReadPath admits. The lookup has already applied the
// download policy and the format's own refusals, which keep their answers.
// A checksum whose file the lookup finds keeps the cache's answer
// (getSidecar).
func (s *Kit) coldRemoteRead(
	ctx context.Context, st Store, c repo.Config, cached bool, req ItemRequest, acc Access,
	dl *Download,
) bool {
	if !coldReadType(c, cached) || s.RemoteSources == nil || req.Path == "" || req.IsFolder ||
		strings.Contains(req.Path, "!/") {
		return false
	}
	file := req.Path
	if artifact, _, ok := item.Sidecar(req.Path); ok {
		_, err := Lookup(ctx, st, c, cached, artifact, acc, dl, virtual.Resolve)
		if !errors.Is(err, kernel.ErrNotFound) {
			return false
		}
		file = artifact
	}
	return s.coldReadPath(c, file)
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
