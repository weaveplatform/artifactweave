package artifactshttp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

const (
	storagePrefix  = "/artifactory/api/storage/"
	metadataPrefix = "/artifactory/api/metadata/"
)

// Storage API media types, as Artifactory names its views.
const (
	mediaFileInfo     = "application/vnd.org.jfrog.artifactory.storage.FileInfo+json"
	mediaFolderInfo   = "application/vnd.org.jfrog.artifactory.storage.FolderInfo+json"
	mediaFileList     = "application/vnd.org.jfrog.artifactory.storage.FileList+json"
	mediaLastModified = "application/vnd.org.jfrog.artifactory.storage.ItemLastModified+json"
	mediaProperties   = "application/vnd.org.jfrog.artifactory.storage.ItemProperties+json"
	// 7.161.26's MT_STATS_INFO (ArtifactRestConstants), which a 6.11.3
	// capture in RTFACT-21564 shows on the wire; JFrog's reference page
	// names it without "artifactory.".
	mediaStats = "application/vnd.org.jfrog.artifactory.storage.StatsInfo+json"
)

// noProperties is the 404 for a properties view with nothing to show. The
// wording matters: jfrog-client-go's GetItemProperties reads a 404 as "no
// properties" only when the body says this, and fails on any other 404
// (research 09).
func noProperties() error {
	return apierror.New(http.StatusNotFound, "No properties could be found.")
}

// The storage API's other answers in 7.161.26's words (ArtifactResource):
// the item info's miss (prepareStorageInfoResponse), the statistics' miss
// (prepareStatsResponse), and the last modified and permissions views'
// refusal of a key that names no local repository or remote cache
// (prepareLastModifiedResponse, preparePermissionsResponse).
const (
	textNoItem     = "Unable to find item"
	textNoItemPath = "Unable to find item path"
	textNotLocal   = "This method can only be invoked on local/cached repositories."
)

// storageRefused is prepareUnAuthorizedResponse's 403, which names the
// path as a RepoPath ("<key>:<path>", "<key>:" for the root) and the
// caller, anonymous too. The exception carries its response, so Jersey
// answers it without the exception mappers (ServerRuntime.mapException,
// 3.1.9) and an anonymous caller gets this 403, not the 401 challenge the
// mappers give its other refusals (#1573). The text is the
// RestResponseFilter's error envelope; one 7.59.16 capture shows it raw
// (jfrog/jfrog-client-go#851), which reads as Jersey fixing the media
// type before the filter runs, not as the intended answer. While
// hideUnauthorizedResources is on, prepareUnAuthorizedResponse throws
// NotFoundException("Resource not found") instead, which
// NotFoundExceptionMapper answers 404 in the error envelope, to an
// anonymous caller too (ArtifactResource 300-308, 7.161.26).
func storageRefused(key, path string, p kernel.Principal) error {
	return apierror.Hide(apierror.New(http.StatusForbidden, fmt.Sprintf(
		"Request for '%s:%s' is forbidden for user: '%s'.", key, path, p.Name)),
		apierror.New(http.StatusNotFound, "Resource not found"))
}

// addonRefused is the refusal of the views the closed REST add-on serves
// (the file list, last modified and permissions): an exception without a
// response, which ArtifactoryRestExceptionMapperBase answers 403 to a user
// and 401 "Unauthorized" with its challenge to an anonymous caller
// (createUnauthorizedResponseWithChallenge), or MapperHidden while
// hideUnauthorizedResources is on (interceptForbiddenStatus).
func addonRefused(p kernel.Principal) error {
	if p.Anonymous {
		return apierror.Hide(apierror.New(http.StatusUnauthorized, "Unauthorized"), httpkit.MapperHidden)
	}
	return kernel.ErrForbidden
}

// addonMiss is the add-on's 404 for a last modified or permissions request
// whose path is not there: ArtifactResource answers the
// ItemNotFoundRuntimeException it catches with a NotFoundException of its
// message. The add-on's source is closed; RepositoryServiceImpl
// .getLastModified throws "Could not find item: <id>", and the exception's
// RepoPath constructor words the rest "Item <repoPath> does not exist". The
// file list keeps ItemNotFound, as no source shows its
// DoesNotExistException's text.
func addonMiss(mode, key, path string) error {
	switch mode {
	case "lastModified":
		return apierror.New(
			http.StatusNotFound,
			fmt.Sprintf("Could not find item: %s:%s", key, path),
		)
	case "permissions":
		return apierror.New(
			http.StatusNotFound,
			fmt.Sprintf("Item %s:%s does not exist", key, path),
		)
	}
	return itemkit.ItemNotFound()
}

// expectedFolder is the file list's 400 for a file, FolderExpectedException's
// only message, which writeStreamingFileList answers as a BadRequestException.
func expectedFolder(key, path string) error {
	return apierror.New(http.StatusBadRequest,
		fmt.Sprintf("Expected a folder but found a file, at: %s:%s", key, path))
}

// RegisterStorageRoutes serves the storage API (/api/storage) and the
// partial property update (/api/metadata) under the patterns
// httpkit.ChiPatterns derives from the spec, so, registered before the
// router's stubs of the hand-written operations, they replace their 501
// stubs. The bare /{repoKey} form adds the repository root, which the spec
// addresses with an empty item path.
func (s ArtifactoryAPI) RegisterStorageRoutes(r chi.Router) {
	for _, p := range append(httpkit.ChiPatterns(storagePrefix+"{repoKey}/{itemPath}"), storagePrefix+"{repoKey}") {
		r.Get(p, s.getStorage)
		r.Put(p, s.setProperties)
		r.Delete(p, s.deleteProperties)
	}
	for _, p := range append(httpkit.ChiPatterns(metadataPrefix+"{repoKey}/{itemPath}"), metadataPrefix+"{repoKey}") {
		r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).Patch(p, s.patchMetadata)
	}
}

// rawQuery parses a query on "&" alone. url.ParseQuery drops every pair
// holding a ";", but the properties API separates properties with ";", so
// the pairs are split here and each part unescaped.
func rawQuery(raw string) (url.Values, error) {
	q := url.Values{}
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(k)
		if err != nil {
			return nil, httpkit.BadRequestf("malformed query parameter %q", k)
		}
		value, err := url.QueryUnescape(v)
		if err != nil {
			return nil, httpkit.BadRequestf("malformed value of query parameter %q", key)
		}
		q[key] = append(q[key], value)
	}
	return q, nil
}

// flag reads a 0/1 query flag; an absent or empty flag is def (the JFrog
// CLI sends "depth=" and the like for options it leaves unset).
func flag(q url.Values, name string, def bool) (bool, error) {
	switch q.Get(name) {
	case "":
		return def, nil
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	}
	return false, httpkit.BadRequestf("%s must be 0 or 1", name)
}

// storageModes are the views a storage GET selects; at most one may be
// given, and the listing parameters only with list.
var (
	storageModes  = []string{"properties", "lastModified", "stats", "permissions", "list"}
	listingParams = []string{"deep", "depth", "listFolders", "mdTimestamps", "includeRootPath"}
)

func storageMode(q url.Values) (string, error) {
	mode := ""
	for _, m := range storageModes {
		if !q.Has(m) {
			continue
		}
		if mode != "" {
			return "", httpkit.BadRequestf(
				"only one of %s may be given",
				strings.Join(storageModes, ", "),
			)
		}
		mode = m
	}
	if mode != "list" {
		for _, p := range listingParams {
			if q.Has(p) {
				return "", httpkit.BadRequestf("%s is a listing parameter and needs list", p)
			}
		}
	}
	return mode, nil
}

// storageView is one storage request: the repository as the client named
// it, the resolved configuration, and what the caller may do. slash is
// whether the request's path ended in "/", which the statistics' URI keeps.
type storageView struct {
	s        ArtifactoryAPI
	st       Store
	key      string
	path     string
	slash    bool
	repo     repo.Config
	isCached bool
	acc      itemkit.Access
}

// uri is the storage API URL of p (a full path, "" for the root) under the
// key the client used.
func (v storageView) uri(ctx context.Context, p string) string {
	u := baseurl.From(ctx) + storagePrefix + v.key
	if p != "" {
		u += "/" + httpkit.EscapePath(p)
	}
	return u
}

func (v storageView) downloadURI(ctx context.Context, p string) string {
	return baseurl.From(ctx) + "/artifactory/" + v.key + "/" + httpkit.EscapePath(p)
}

// requestDownloadURI is 7.161.26's buildDownloadUri: the servlet context
// URL and the path the request gave, its key and any trailing "/" kept, so
// the root is "/artifactory/<key>", not the "/artifactory/<key>/" that
// downloadURI gives it. The path is percent-escaped, as downloadURI's is:
// 7.x appends the decoded path as it is, which leaves a space or "#" in a
// URI that is then not valid, so that is taken for a bug until a live 7.x
// shows clients rely on it (#216).
func (v storageView) requestDownloadURI(ctx context.Context) string {
	u := baseurl.From(ctx) + "/artifactory/" + v.key
	if v.path != "" {
		u += "/" + httpkit.EscapePath(v.path)
	}
	if v.slash {
		u += "/"
	}
	return u
}

// target finds the item the request names, through a virtual repository's
// members when it is one. The root is a folder that carries only the ID of
// its record and when its properties last changed, when properties have
// given it one: the other root views describe the repository the same way
// whether or not it has a record.
func (v storageView) target(ctx context.Context) (itemkit.Found, error) {
	if v.path == "" {
		if v.repo.Class == repo.Virtual {
			root := model.Item{RepoKey: v.repo.Key, Folder: true}
			return itemkit.Found{Repo: v.repo, Item: root}, nil
		}
		root, err := rootItem(ctx, v.st, v.repo.Key, v.isCached || v.repo.Class == repo.Remote)
		return itemkit.Found{Repo: v.repo, Item: root}, err
	}
	// The storage API describes the item at the path as given; only
	// downloads resolve it through the format.
	return itemkit.Lookup(ctx, v.st, v.repo, v.isCached, v.path, v.acc, nil, virtual.Stored)
}

// rootItem is the root of the tree key and cached name: a folder that
// carries only the ID of its record and when its properties last changed,
// when properties have given it one.
func rootItem(ctx context.Context, st Store, key string, cached bool) (model.Item, error) {
	root := model.Item{RepoKey: key, Cached: cached, Folder: true}
	rec, err := st.Items().Root(ctx, key, cached)
	switch {
	case err == nil:
		root.ID, root.PropertiesModified = rec.ID, rec.PropertiesModified
	case !errors.Is(err, kernel.ErrNotFound):
		return root, fmt.Errorf("repository root: %w", err)
	}
	return root, nil
}

// getVirtualStorage answers a storage view other than the file list of the
// virtual repository v.repo as 7.161.26's ArtifactResource does. None asks
// Read of the virtual path but the statistics, and each answers its miss
// and a member the caller may not read alike, so nothing tells a caller
// what an unreadable member holds.
func (s ArtifactoryAPI) getVirtualStorage(
	w http.ResponseWriter,
	r *http.Request,
	v storageView,
	mode string,
	q url.Values,
	p kernel.Principal,
) {
	ctx := r.Context()
	var (
		media string
		body  any
		err   error
	)
	switch mode {
	case "lastModified", "permissions":
		// prepareLastModifiedResponse and preparePermissionsResponse take
		// only a local repository or a cache (isLocalRepo) and refuse any
		// other key before they weigh a right.
		err = apierror.New(http.StatusBadRequest, textNotLocal)
	case "stats":
		// prepareStatsResponse asks canRead of the virtual path, which only
		// the plain ANY target's grants match, then exists, which no
		// virtual path does: statistics are kept for the members' files
		// alone (RTFACT-12026, "Make stats API available for virtual
		// repositories", is deferred).
		if !canRead(v.acc, v.repo, v.path) {
			// Written here, as ItemError would unwrap the refusal from the
			// answer that hides it.
			httpkit.WriteErr(w, r, storageRefused(v.key, v.path, p))
			return
		}
		err = apierror.New(http.StatusNotFound, textNoItemPath)
	case "properties":
		media, body, err = v.virtualProperties(ctx, q.Get("properties"))
	default:
		var f itemkit.Found
		if f, err = v.virtualItem(ctx); err != nil {
			break
		}
		if !f.Item.Folder {
			media, body = mediaFileInfo, v.infoBody(ctx, f.Item)
			break
		}
		var l *httpkit.ListStream
		if l, err = v.folderInfo(ctx, f); err == nil {
			_ = l.Write(w)
			return
		}
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage "+mode, err))
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// virtualMiss is the item info's answer when no member the caller may read
// holds the path: RepositoryBrowsingServiceImpl.getVirtualRepoItem returns
// null, prepareStorageInfoResponse answers it with an empty 404, and
// RestResponseFilter fills that in with the status's reason phrase.
func virtualMiss() error {
	return apierror.New(http.StatusNotFound, "Not Found")
}

// memberItems calls visit with the item at the view's path in each member
// of the virtual repository v.repo that holds one the caller may read, in
// virtual.Stored's order, until visit returns true or an error. It is
// VirtualRepo.getVirtualRepoItem's walk of
// getResolvedLocalFederatedAndCachedRepos, which leaves out a remote
// member that keeps no cache, with the Read check its callers make of each
// member that holds the item: getVirtualRepoItem's in the browsing
// service, the properties service's in getProperties. 7.x describes the
// first holder even when only a later one is readable, which leaks what an
// unreadable member holds, so the first readable one is described instead.
func (v storageView) memberItems(
	ctx context.Context,
	visit func(itemkit.Found) (bool, error),
) error {
	members, err := virtual.Members(
		ctx,
		v.st.Repos(),
		v.repo,
		virtual.Options{Order: virtual.Stored},
	)
	if err != nil {
		return fmt.Errorf("members of %s: %w", v.repo.Key, err)
	}
	for _, m := range members {
		cached := m.Repo.Class == repo.Remote
		if cached && !storesLocally(m.Repo) {
			continue
		}
		var it model.Item
		if v.path == "" {
			it, err = rootItem(ctx, v.st, m.Repo.Key, cached)
		} else {
			it, err = v.st.Items().Get(ctx, m.Repo.Key, cached, v.path)
		}
		switch {
		case errors.Is(err, kernel.ErrNotFound):
			continue
		case err != nil:
			return fmt.Errorf("item in %s: %w", m.Repo.Key, err)
		case !v.acc.Item(m.Repo, v.path, it.Folder, api.ActionRead):
			continue
		}
		if done, err := visit(itemkit.Found{Repo: m.Repo, Item: it}); done || err != nil {
			return err
		}
	}
	return nil
}

// virtualItem is the item the item info describes: the first a member the
// caller may read holds, its repo the virtual repository's (infoBody). The
// root is the virtual repository's own, there when any member's is.
func (v storageView) virtualItem(ctx context.Context) (itemkit.Found, error) {
	var found *itemkit.Found
	err := v.memberItems(ctx, func(f itemkit.Found) (bool, error) {
		found = &f
		return true, nil
	})
	switch {
	case err != nil:
		return itemkit.Found{}, err
	case found == nil:
		return itemkit.Found{}, virtualMiss()
	case v.path == "":
		return itemkit.Found{
			Repo: v.repo,
			Item: model.Item{RepoKey: v.repo.Key, Folder: true},
		}, nil
	}
	return *found, nil
}

// virtualProperties is the properties view of a virtual repository's path,
// as ArtifactResource.resolveProperties reads it: the properties of the
// first member's item, in memberItems' order, that has any, and only then
// the keys the request names (getPropertiesResponse), so a later member's
// are never read for a key the first lacks.
func (v storageView) virtualProperties(ctx context.Context, names string) (string, any, error) {
	var (
		at    model.Item
		props model.Properties
	)
	err := v.memberItems(ctx, func(f itemkit.Found) (bool, error) {
		if f.Item.ID == 0 {
			return false, nil
		}
		got, err := v.st.Items().Properties(ctx, f.Item.ID)
		if err != nil {
			return true, fmt.Errorf("properties: %w", err)
		}
		at, props = f.Item, got
		return len(got) > 0, nil
	})
	if err != nil {
		return "", nil, err
	}
	return v.propertiesView(ctx, at, props, names)
}

// trees are the repository trees a folder view reads: the repository, or a
// virtual repository's readable local and federated members and then its
// remote members' caches, never a live remote (7.161.26's
// getResolvedLocalFederatedAndCachedRepos, virtual.Stored).
func (v storageView) trees(ctx context.Context) ([]itemkit.Found, error) {
	if v.repo.Class != repo.Virtual {
		cached := v.repo.Class == repo.Remote || v.isCached
		return []itemkit.Found{
			{Repo: v.repo, Item: model.Item{RepoKey: v.repo.Key, Cached: cached}},
		}, nil
	}
	members, err := virtual.Members(
		ctx,
		v.st.Repos(),
		v.repo,
		virtual.Options{Order: virtual.Stored},
	)
	if err != nil {
		return nil, fmt.Errorf("members of %s: %w", v.repo.Key, err)
	}
	var out []itemkit.Found
	for _, m := range members {
		if !v.acc.Folder(m.Repo, v.path, api.ActionRead) {
			continue
		}
		cached := m.Repo.Class == repo.Remote
		out = append(
			out,
			itemkit.Found{Repo: m.Repo, Item: model.Item{RepoKey: m.Repo.Key, Cached: cached}},
		)
	}
	return out, nil
}

// getStorage answers GET /api/storage/{repo}/{path}: file or folder info,
// or the view one mode parameter selects.
func (s ArtifactoryAPI) getStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// 7.161.26's ArtifactResource takes a file list request before every
	// other view, and prepareFileListResponse refuses an anonymous caller
	// before it reads the repository or the path (#1573), with
	// AuthorizationRestException's 403 and its text in the error envelope
	// (RestResponseFilter). Jersey answers an exception that carries its
	// response without the exception mappers (ServerRuntime.mapException,
	// 3.1.9), so ArtifactoryRestExceptionMapperBase never turns this 403
	// into the 401 challenge it gives an anonymous caller's other refusals.
	if p, err := httpkit.Principal(ctx); err == nil && p.Anonymous {
		if q, err := rawQuery(r.URL.RawQuery); err == nil && q.Has("list") {
			httpkit.WriteErr(w, r, apierror.New(http.StatusForbidden,
				"This resource is available to authenticated users only."))
			return
		}
	}
	v, p, q, err := s.storageCaller(r, storagePrefix)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if v.repo, v.isCached, err = itemkit.ResolveRepo(ctx, v.st, v.key); err != nil {
		// The answers for a missing repository are apierror values already,
		// and a refusal among them must reach WriteErr whole to be hidden.
		if mode, merr := storageMode(q); merr == nil && isMissingRepo(err) {
			httpkit.WriteErr(w, r, missingRepoAnswer(mode, v, p))
			return
		}
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve repository", err))
		return
	}
	mode, err := storageMode(q)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	// A virtual repository's views but the file list answer as 7.161.26's
	// ArtifactResource answers one, none of them by the read gate below.
	if v.repo.Class == repo.Virtual && mode != "list" {
		s.getVirtualStorage(w, r, v, mode, q, p)
		return
	}
	// 7.161.26 runs fixPathParamterIfNeededForRemoteCached before every view
	// but the file list and the statistics (prepareResponseAccordingToType),
	// so those views name a remote's item under its cache's key throughout:
	// the item info's repo, uri and downloadUri, the properties' and last
	// modified's uri, and their refusals and misses.
	if mode != "list" && mode != "stats" {
		v.key = v.localKey()
	}
	if err := v.admit(ctx, mode, p); err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	f, err := v.target(ctx)
	if errors.Is(err, kernel.ErrNotFound) {
		// Not through ItemError, which would unwrap a refusal from the
		// answer that hides it.
		httpkit.WriteErr(w, r, v.miss(mode, p))
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage item", err))
		return
	}
	// A virtual repository's root, which only its file list reaches here,
	// is the virtual repository itself, which admit has decided by its
	// members; any other item is a member's.
	if f.Repo.Class != repo.Virtual &&
		!v.acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, api.ActionRead) {
		httpkit.WriteErr(w, r, v.refusal(mode, p))
		return
	}
	// The file list and a folder's info are written as they are read.
	if mode == "list" || mode == "" && f.Item.Folder {
		var l *httpkit.ListStream
		if mode == "list" {
			l, err = v.list(ctx, f, q)
		} else {
			l, err = v.folderInfo(ctx, f)
		}
		if err != nil {
			httpkit.WriteErr(w, r, s.ItemError(ctx, "storage "+mode, err))
			return
		}
		_ = l.Write(w)
		return
	}
	var (
		media string
		body  any
	)
	switch mode {
	case "":
		media, body, err = v.info(ctx, f)
	case "properties":
		media, body, err = v.properties(ctx, f, q.Get("properties"))
	case "stats":
		media, body, err = v.stats(ctx, f)
	case "lastModified":
		media, body, err = v.lastModified(ctx, f)
	case "permissions":
		media, body, err = v.permissions(ctx, f)
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "storage "+mode, err))
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// storageRequest parses a storage or metadata request and resolves its
// repository and caller.
func (s ArtifactoryAPI) storageRequest(
	r *http.Request,
	prefix string,
) (storageView, kernel.Principal, url.Values, error) {
	v, p, q, err := s.storageCaller(r, prefix)
	if err != nil {
		return v, p, q, err
	}
	if v.repo, v.isCached, err = itemkit.ResolveRepo(r.Context(), v.st, v.key); err != nil {
		return v, p, nil, s.ItemError(r.Context(), "resolve repository", err)
	}
	return v, p, q, nil
}

// storageCaller parses a storage or metadata request and resolves its
// caller, leaving the repository to the caller, as getStorage answers a key
// that names none in each view's own words.
func (s ArtifactoryAPI) storageCaller(
	r *http.Request,
	prefix string,
) (storageView, kernel.Principal, url.Values, error) {
	ctx := r.Context()
	var v storageView
	st, err := s.content()
	if err != nil {
		return v, kernel.Principal{}, nil, err
	}
	req, err := itemkit.ParseEscapedItemPath(strings.TrimPrefix(r.URL.EscapedPath(), prefix))
	if err != nil {
		return v, kernel.Principal{}, nil, err
	}
	q, err := rawQuery(r.URL.RawQuery)
	if err != nil {
		return v, kernel.Principal{}, nil, err
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		return v, p, nil, err
	}
	return storageView{
		s: s, st: st, key: req.Key, path: req.Path, slash: req.IsFolder, acc: acc,
	}, p, q, nil
}

// isMissingRepo reports whether err is ResolveRepo's answer for a key that
// names no repository, or a cache of one that is not remote; any other
// error is the store's.
func isMissingRepo(err error) bool {
	var e *apierror.Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// missingRepoAnswer is a view's answer for a key that names no repository,
// as 7.161.26 gives it. The item info finds no item before it weighs any
// right; the statistics ask for read first, and a repository of no known
// type matches only the plain ANY target's grants, not ANY LOCAL or ANY
// REMOTE (AuthorizationServiceImpl.createArtifactPermissionScope), so an
// administrator or a holder of read on ANY learns the path is not there and
// anyone else is refused; the properties service reads nothing; and the
// last modified and permissions views take only a local repository or a
// remote cache. The file list keeps the repository's own 404, as no source
// shows the add-on's RepoDoesNotExistException text.
func missingRepoAnswer(mode string, v storageView, p kernel.Principal) error {
	switch mode {
	case "":
		return apierror.New(http.StatusNotFound, textNoItem)
	case "stats":
		// A repository with no key and no class: no permission target names
		// it and none of its keys is a cache's, which the decider would
		// read as the remote repository's, so only plain ANY grants match.
		if !canRead(v.acc, repo.Config{}, v.path) {
			return storageRefused(v.key, v.path, p)
		}
		return apierror.New(http.StatusNotFound, textNoItemPath)
	case "properties":
		return noProperties()
	case "lastModified", "permissions":
		return apierror.New(http.StatusBadRequest, textNotLocal)
	}
	return itemkit.NotFoundRepo(v.key)
}

// canRead is 7.x's canRead of the path a request names: the root as the
// folder it is, any other path as given, which for a path that is not
// there is the file it would be.
func canRead(acc itemkit.Access, c repo.Config, path string) bool {
	if path == "" {
		return acc.Folder(c, path, api.ActionRead)
	}
	return acc.File(c, path, api.ActionRead)
}

// storesNothingAnswer answers a remote repository that keeps no cache
// (storeArtifactsLocally off), which 7.161.26 serves as it does a key that
// names no repository, as it has no local or cache repository to read:
// isLocalRepo is false and fixPathParamterIfNeededForRemoteCached finds no
// cache to turn to. Its statistics ask for read before they find nothing
// there. Nil lets the file list, which the add-on serves, go on.
func (v storageView) storesNothingAnswer(mode string, p kernel.Principal) error {
	if v.repo.Class != repo.Remote || v.storesLocally() {
		return nil
	}
	switch mode {
	case "":
		return apierror.New(http.StatusNotFound, textNoItem)
	case "stats":
		if !canRead(v.acc, v.repo, v.path) {
			return storageRefused(v.key, v.path, p)
		}
		return apierror.New(http.StatusNotFound, textNoItemPath)
	case "properties":
		return noProperties()
	case "lastModified", "permissions":
		return apierror.New(http.StatusBadRequest, textNotLocal)
	}
	return nil
}

// admit answers what getStorage refuses before it looks for the item: a
// remote repository that keeps no cache, a caller without the right the
// view needs, and a blacked-out repository's file list. Nil lets the view
// go on.
func (v storageView) admit(ctx context.Context, mode string, p kernel.Principal) error {
	if err := v.storesNothingAnswer(mode, p); err != nil {
		return err
	}
	// A folder check first, as for a download; the exact right once the
	// item is found. A virtual repository is never asked itself: its
	// members decide, its root passing when any member's does (Admits).
	if ok, err := v.acc.Admits(ctx, v.st.Repos(), v.repo, v.path); err != nil {
		return v.s.InternalError(ctx, "read gate", err)
	} else if !ok {
		return v.refusal(mode, p)
	}
	switch {
	case mode == "lastModified" && !v.acc.Folder(v.repo, v.path, api.ActionWrite),
		mode == "permissions" && !p.Admin:
		return v.refusal(mode, p)
	}
	// The file list of a blacked-out repository answers 404 with
	// BlackedOutException's message (ArtifactResource.writeStreamingFileList).
	// The other views describe the item regardless, as 6.20's do: its item
	// info checks read permission alone (prepareStorageInfoResponse).
	if mode == "list" && v.repo.Class != repo.Virtual {
		if err := generic.CheckBlackedOut(v.repo, v.path); err != nil {
			return v.s.ItemError(ctx, "storage list", err)
		}
	}
	return nil
}

// localKey is the key the views other than the file list and the
// statistics name the item by: a remote repository's cache, as
// fixPathParamterIfNeededForRemoteCached turns a remote key into its cache's
// before they run, and the key as given otherwise. A remote that keeps no
// cache has none to turn to, and storesNothingAnswer answers it first.
func (v storageView) localKey() string {
	if v.repo.Class == repo.Remote && v.storesLocally() {
		return v.repo.Key + model.CacheSuffix
	}
	return v.key
}

// storesLocally reports whether a remote repository keeps a cache, which
// it does unless storeArtifactsLocally is set off.
func (v storageView) storesLocally() bool { return storesLocally(v.repo) }

func storesLocally(c repo.Config) bool {
	return c.StoreArtifactsLocally == nil || *c.StoreArtifactsLocally
}

// refusal is the view's answer to a caller without the right it needs, in
// 7.161.26's words: the item info's and the statistics' 403 naming the
// path and the caller, the properties service's empty answer (it returns
// no properties for a path the caller may not read), or the add-on's
// refusal. A virtual repository's file list, the only view of one that
// reaches here (getVirtualStorage), keeps httpkit.Denied: no source shows
// the add-on's answer for one.
func (v storageView) refusal(mode string, p kernel.Principal) error {
	if v.repo.Class == repo.Virtual {
		return httpkit.Denied(p)
	}
	switch mode {
	case "", "stats":
		return storageRefused(v.key, v.path, p)
	case "properties":
		return noProperties()
	}
	return addonRefused(p)
}

// miss is the view's answer for a path that is not there, after the folder
// check let it through. The item info and the statistics ask for read of
// the path itself before they look (prepareStorageInfoResponse,
// prepareStatsResponse), so a path the caller could not read as a file is
// refused rather than reported missing. A virtual repository's file list
// keeps ItemNotFound, as refusal keeps its refusal.
func (v storageView) miss(mode string, p kernel.Principal) error {
	if v.repo.Class == repo.Virtual {
		return itemkit.ItemNotFound()
	}
	switch mode {
	case "", "stats":
		if !canRead(v.acc, v.repo, v.path) {
			return v.refusal(mode, p)
		}
		if mode == "" {
			return apierror.New(http.StatusNotFound, textNoItem)
		}
		return apierror.New(http.StatusNotFound, textNoItemPath)
	case "properties":
		return noProperties()
	}
	return addonMiss(mode, v.key, v.path)
}

// listsFolder refuses a file list of a file, with FolderExpectedException's
// text under the key as given for a repository (the file list runs before
// fixPathParamterIfNeededForRemoteCached), and a virtual repository's own.
func (v storageView) listsFolder(it model.Item) error {
	switch {
	case it.Folder:
		return nil
	case v.repo.Class == repo.Virtual:
		return httpkit.BadRequestf("%s is a file; list needs a folder", it.StoragePath())
	}
	return expectedFolder(v.key, v.path)
}

// putTime sets key to t in Artifactory's form, leaving it out when t is
// unknown (the repository root has no stored item).
func putTime(body map[string]any, key string, t time.Time) {
	if !t.IsZero() {
		body[key] = httpkit.FormatTime(t)
	}
}

// info is the item info view: a file's FileInfo, or a folder's FolderInfo
// with its children collected. getStorage writes a folder's as folderInfo
// streams it; a search result's info detail embeds it whole.
func (v storageView) info(ctx context.Context, f itemkit.Found) (string, any, error) {
	body := v.infoBody(ctx, f.Item)
	if !f.Item.Folder {
		return mediaFileInfo, body, nil
	}
	walk, err := v.childWalk(ctx, f.Item.FullPath())
	if err != nil {
		return "", nil, err
	}
	entries := []map[string]any{}
	pages := kernel.NewPages(walk.read, kernel.MaxLimit)
	for {
		children, ok, err := pages.Next(ctx)
		if err != nil {
			return "", nil, err //nolint:wrapcheck // the walk names the tree it was reading
		}
		if !ok {
			break
		}
		for _, c := range children {
			entries = append(entries, childEntry(c))
		}
	}
	body["children"] = entries
	return mediaFolderInfo, body, nil
}

// folderInfo is a folder's FolderInfo, its children written a store page
// per tree at a time (decision 0043, "Streaming complete lists") in the
// order info collects them.
func (v storageView) folderInfo(ctx context.Context, f itemkit.Found) (*httpkit.ListStream, error) {
	walk, err := v.childWalk(ctx, f.Item.FullPath())
	if err != nil {
		return nil, err
	}
	body := v.infoBody(ctx, f.Item)
	body["children"] = []map[string]any{}
	// A document of strings and an empty array, which always splits.
	env, _ := httpkit.EnvelopeOf(body, "children")
	//nolint:wrapcheck // a store error the handler maps, as info's is
	return httpkit.StreamList(ctx, kernel.NewPages(walk.read, kernel.MaxLimit),
		func(c model.Item) (map[string]any, bool, error) { return childEntry(c), true, nil },
		httpkit.ListOptions{ContentType: mediaFolderInfo, Envelope: env})
}

// childEntry is a folder's child as FolderInfo lists it.
func childEntry(c model.Item) map[string]any {
	return map[string]any{"uri": "/" + c.Name, "folder": c.Folder}
}

// infoBody is the item info document without a folder's children: the
// FileInfo of a file, the rest of a folder's FolderInfo.
func (v storageView) infoBody(ctx context.Context, it model.Item) map[string]any {
	body := map[string]any{
		"uri":  v.uri(ctx, it.FullPath()),
		"repo": v.key,
		"path": it.StoragePath(),
	}
	putTime(body, "created", it.Created)
	putTime(body, "lastModified", it.Modified)
	putTime(body, "lastUpdated", it.Updated)
	if it.CreatedBy != "" {
		body["createdBy"] = it.CreatedBy
	}
	if it.ModifiedBy != "" {
		body["modifiedBy"] = it.ModifiedBy
	}
	if !it.Folder {
		body["downloadUri"] = v.downloadURI(ctx, it.FullPath())
		body["size"] = strconv.FormatInt(it.Size, 10)
		body["mimeType"] = it.MimeType
		body["checksums"] = itemkit.ChecksumsJSON(it.Actual)
		body["originalChecksums"] = itemkit.ChecksumsJSON(it.Original)
		if it.RemoteURL != "" {
			body["remoteUrl"] = it.RemoteURL
		}
	}
	return body
}

// childWalk walks a folder's children as FolderInfo lists them: each tree
// the view reads in turn, its children in name order, and a name only
// from the first tree that holds an item there the caller may read, as a
// download would resolve it. The trees are not merged by name: a virtual
// repository lists its first member's children, then the names only a
// later member holds, and never a Maven index folder, ".index", which
// 7.161.26's VirtualRepo.getChildrenNamesDeeply leaves out of every
// virtual repository's children whatever its package type.
type childWalk struct {
	v      storageView
	folder string
	trees  []itemkit.Found
	// at is the tree being walked, cur its walk, and before a walk of each
	// earlier tree, kept at or before cur's name, which tells whether an
	// earlier tree has already listed it.
	at     int
	cur    *treeWalk
	before []*treeWalk
}

func (v storageView) childWalk(ctx context.Context, folder string) (*childWalk, error) {
	trees, err := v.trees(ctx)
	if err != nil {
		return nil, err
	}
	return &childWalk{v: v, folder: folder, trees: trees}, nil
}

// children is a tree's walk of the folder's children, in name order.
func (m *childWalk) children(t itemkit.Found) *treeWalk {
	return &treeWalk{t: t, pages: kernel.NewPages(
		func(ctx context.Context, p kernel.Page) ([]model.Item, string, error) {
			return m.v.st.Items().Children(ctx, t.Repo.Key, t.Item.Cached, m.folder, p)
		},
		kernel.MaxLimit,
	)}
}

// nexusIndexDir is MavenNaming.NEXUS_INDEX_DIR, the folder a Maven
// repository's index is published under.
const nexusIndexDir = ".index"

func (m *childWalk) readable(t itemkit.Found, it model.Item) bool {
	return m.v.acc.Item(t.Repo, it.FullPath(), it.Folder, api.ActionRead)
}

// read is the walk as a PageFunc: up to limit children, and a cursor while
// more may follow, which only marks that, as fileListWalk's does.
func (m *childWalk) read(ctx context.Context, p kernel.Page) ([]model.Item, string, error) {
	var out []model.Item
	for len(out) < max(p.Limit, 1) {
		if m.cur == nil {
			if m.at == len(m.trees) {
				return out, "", nil
			}
			m.cur, m.before = m.children(m.trees[m.at]), nil
			for _, t := range m.trees[:m.at] {
				m.before = append(m.before, m.children(t))
			}
		}
		it, ok, err := m.cur.head(ctx, m.folder)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			m.cur = nil
			m.at++
			continue
		}
		m.cur.rows = m.cur.rows[1:]
		if !m.readable(m.cur.t, it) ||
			m.v.repo.Class == repo.Virtual && it.Name == nexusIndexDir {
			continue
		}
		listed, err := m.listedBefore(ctx, it.Name)
		if err != nil {
			return nil, "", err
		}
		if !listed {
			out = append(out, it)
		}
	}
	return out, kernel.Cursor("more"), nil
}

// listedBefore reports whether an earlier tree holds a readable item named
// name, moving each earlier tree's walk up to it: the names come in order.
func (m *childWalk) listedBefore(ctx context.Context, name string) (bool, error) {
	for _, b := range m.before {
		for {
			h, ok, err := b.head(ctx, m.folder)
			if err != nil {
				return false, err
			}
			if !ok || h.Name > name {
				break
			}
			if h.Name == name && m.readable(b.t, h) {
				return true, nil
			}
			b.rows = b.rows[1:]
		}
	}
	return false, nil
}

// list answers the FileList view of a folder: the items below it in every
// tree the view reads, sorted by uri, which is their full paths' byte
// order. It is written a store page per tree at a time (decision 0043,
// "Streaming complete lists"): the trees' walks in that order are merged,
// so the answer is the one render would sort, never held whole.
func (v storageView) list(
	ctx context.Context,
	f itemkit.Found,
	q url.Values,
) (*httpkit.ListStream, error) {
	if err := v.listsFolder(f.Item); err != nil {
		return nil, err
	}
	l, err := parseListing(q)
	if err != nil {
		return nil, err
	}
	folder := f.Item.FullPath()
	walk, err := v.mergedWalk(ctx, folder, l.isDeep)
	if err != nil {
		return nil, err
	}
	walk.self, walk.root = f.Item, l.shouldIncludeRootPath
	// A constant document, which always splits.
	env, _ := httpkit.EnvelopeOf(map[string]any{
		"uri": v.uri(ctx, folder), "created": httpkit.FormatTime(time.Now()),
		"files": []map[string]any{},
	}, "files")
	//nolint:wrapcheck // a store error the handler maps, as below's was
	return httpkit.StreamList(ctx, kernel.NewPages(walk.read, kernel.MaxLimit),
		func(e fileListEntry) (map[string]any, bool, error) {
			if e.self {
				return l.rootEntry(e.item), true, nil
			}
			m, ok := l.entry(folder, e.listed)
			return m, ok, nil
		},
		httpkit.ListOptions{ContentType: mediaFileList, Envelope: env})
}

// mergedWalk starts the merged walk of what lies below folder in every
// tree the view reads: its children, or (deep) every descendant.
func (v storageView) mergedWalk(
	ctx context.Context,
	folder string,
	deep bool,
) (*fileListWalk, error) {
	trees, err := v.trees(ctx)
	if err != nil {
		return nil, err
	}
	walk := &fileListWalk{v: v, folder: folder}
	for _, t := range trees {
		list := v.st.Items().Children
		if deep {
			list = v.st.Items().DescendantsByFullPath
		}
		walk.trees = append(walk.trees, &treeWalk{
			t: t,
			pages: kernel.NewPages(
				func(ctx context.Context, p kernel.Page) ([]model.Item, string, error) {
					return list(ctx, t.Repo.Key, t.Item.Cached, folder, p)
				},
				kernel.MaxLimit,
			),
		})
	}
	return walk, nil
}

// fileListEntry is an entry a file list's walk yields: an item below the
// folder, from the tree at index tree, or (self) the folder itself, which
// includeRootPath lists first.
type fileListEntry struct {
	listed
	tree int
	self bool
}

// fileListWalk merges the walks of a file list's trees in full path order.
// Where several trees hold an item at one path, the first tree's that the
// caller may read is listed and the others are skipped, as below keeps the
// first readable one.
type fileListWalk struct {
	v      storageView
	folder string
	self   model.Item
	root   bool
	trees  []*treeWalk
}

// treeWalk is one tree's walk: its store pages and the rows of the current
// page not yet merged.
type treeWalk struct {
	t     itemkit.Found
	pages *kernel.Pages[model.Item]
	rows  []model.Item
	done  bool
}

// head is the tree's next item, reading its next page when the current
// one is used up; false once the tree has no more.
func (w *treeWalk) head(ctx context.Context, folder string) (model.Item, bool, error) {
	for len(w.rows) == 0 && !w.done {
		rows, ok, err := w.pages.Next(ctx)
		if err != nil {
			return model.Item{}, false, fmt.Errorf("list %s/%s: %w", w.t.Repo.Key, folder, err)
		}
		w.rows, w.done = rows, !ok
	}
	if len(w.rows) == 0 {
		return model.Item{}, false, nil
	}
	return w.rows[0], true, nil
}

// read is the merged walk as a PageFunc: up to limit entries in uri order,
// and a cursor while more may follow. The cursor only marks that more
// follows, so it is a constant that never ends the walk early; the walk
// keeps its own place.
func (m *fileListWalk) read(ctx context.Context, p kernel.Page) ([]fileListEntry, string, error) {
	var out []fileListEntry
	if m.root {
		// "/" sorts before every other entry's uri, which all begin with it.
		out, m.root = append(out, fileListEntry{listed: listed{item: m.self}, self: true}), false
	}
	for len(out) < max(p.Limit, 1) {
		least, found := "", false
		for _, w := range m.trees {
			it, ok, err := w.head(ctx, m.folder)
			if err != nil {
				return nil, "", err
			}
			if ok && (!found || it.FullPath() < least) {
				least, found = it.FullPath(), true
			}
		}
		if !found {
			return out, "", nil
		}
		picked := false
		for i, w := range m.trees {
			if len(w.rows) == 0 || w.rows[0].FullPath() != least {
				continue
			}
			it := w.rows[0]
			w.rows = w.rows[1:]
			// What the caller may not see is left out; a folder shows when
			// something below it is readable.
			if !picked && m.v.acc.Item(w.t.Repo, least, it.Folder, api.ActionRead) {
				out, picked = append(
					out,
					fileListEntry{listed: listed{path: least, item: it}, tree: i},
				), true
			}
		}
	}
	return out, kernel.Cursor("more"), nil
}

// listing holds the FileList view's parameters.
type listing struct {
	isDeep, shouldListFolders, shouldIncludeMDTimestamps, shouldIncludeRootPath bool
	// depth bounds the levels a deep listing reaches below the listed
	// folder; 0 is unlimited, as a deep listing without it.
	depth int
}

func parseListing(q url.Values) (listing, error) {
	var l listing
	for _, f := range []struct {
		name string
		on   *bool
	}{
		{"deep", &l.isDeep},
		{"listFolders", &l.shouldListFolders},
		{"mdTimestamps", &l.shouldIncludeMDTimestamps},
		{"includeRootPath", &l.shouldIncludeRootPath},
	} {
		var err error
		if *f.on, err = flag(q, f.name, false); err != nil {
			return l, err
		}
	}
	if d := q.Get("depth"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return l, httpkit.BadRequestf("depth must be a non-negative integer")
		}
		l.depth = n
	}
	return l, nil
}

// listed is an item below the listed folder and its full path in the tree
// listed, which for the trash can is not the item's own.
type listed struct {
	path string
	item model.Item
}

// render is the FileList body of folder, whose own item is self, over the
// items below it. The trash can's list answers with it; a repository's is
// written as list walks it, with the same entries in the same order.
func (l listing) render(uri, folder string, self model.Item, items []listed) map[string]any {
	files := make([]map[string]any, 0, len(items)+1)
	if l.shouldIncludeRootPath {
		files = append(files, l.rootEntry(self))
	}
	for _, it := range items {
		if e, ok := l.entry(folder, it); ok {
			files = append(files, e)
		}
	}
	slices.SortFunc(files, func(a, b map[string]any) int {
		return strings.Compare(
			a["uri"].(string),
			b["uri"].(string),
		) //nolint:forcetypeassert // set above
	})
	return map[string]any{"uri": uri, "created": httpkit.FormatTime(time.Now()), "files": files}
}

// rootEntry is the listed folder itself, at "/" relative to itself. The
// Terraform provider's TestAccDataSourceFileList_include_root_path expects
// this entry first at a repository root, with size -1.
func (l listing) rootEntry(self model.Item) map[string]any {
	return listEntry(self, "/", l.shouldIncludeMDTimestamps)
}

// entry is the FileList entry of an item below folder, with its uri
// relative to folder; false for a folder without listFolders or an item
// deeper than depth.
func (l listing) entry(folder string, it listed) (map[string]any, bool) {
	if it.item.Folder && !l.shouldListFolders ||
		l.depth > 0 && segments(it.path)-segments(folder) > l.depth {
		return nil, false
	}
	rel := "/" + strings.TrimPrefix(strings.TrimPrefix(it.path, folder), "/")
	return listEntry(it.item, rel, l.shouldIncludeMDTimestamps), true
}

// listEntry is one entry of a FileList. size is a JSON number, -1 for a
// folder: the Terraform provider decodes it as an int64 and fails on a
// string. mdTimestamps, when asked for, is left out for an item without
// properties, as 6.20's FileListElement creates the map only when it adds a
// time to it.
func listEntry(it model.Item, uri string, mdTimestamps bool) map[string]any {
	entry := map[string]any{"uri": uri, "folder": it.Folder, "size": int64(-1)}
	putTime(entry, "lastModified", it.Modified)
	if !it.Folder {
		entry["size"] = it.Size
		entry["sha1"] = it.Actual.SHA1
		entry["sha2"] = it.Actual.SHA256
	}
	if mdTimestamps && !it.PropertiesModified.IsZero() {
		entry["mdTimestamps"] = map[string]string{
			"properties": httpkit.FormatTime(it.PropertiesModified),
		}
	}
	return entry
}

func segments(p string) int {
	if p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// properties renders an item's properties, or only the keys named in
// "k1,k2"; nothing to show is a 404.
func (v storageView) properties(
	ctx context.Context,
	f itemkit.Found,
	names string,
) (string, any, error) {
	if f.Item.ID == 0 {
		return "", nil, noProperties()
	}
	props, err := v.st.Items().Properties(ctx, f.Item.ID)
	if err != nil {
		return "", nil, fmt.Errorf("properties: %w", err)
	}
	return v.propertiesView(ctx, f.Item, props, names)
}

// propertiesView renders props, the properties of it, or only the keys
// named in "k1,k2"; nothing to show is a 404.
func (v storageView) propertiesView(
	ctx context.Context,
	it model.Item,
	props model.Properties,
	names string,
) (string, any, error) {
	if names != "" {
		want := pkgroute.SplitValues(names)
		for k := range props {
			if !slices.Contains(want, k) {
				delete(props, k)
			}
		}
	}
	if len(props) == 0 {
		return "", nil, noProperties()
	}
	return mediaProperties, map[string]any{
		"uri":        v.uri(ctx, it.FullPath()),
		"properties": props,
	}, nil
}

// stats is the download statistics view, as 7.161.26's
// prepareStatsResponse writes it: the item named by its download URL under
// the key as given, and for a folder or the root, which keep no
// statistics, 200 with zero counts (new ItemStatsInfo(uri, 0, 0, null)).
// A virtual repository's are getVirtualStorage's.
func (v storageView) stats(ctx context.Context, f itemkit.Found) (string, any, error) {
	var st model.ItemStats
	if !f.Item.Folder {
		var err error
		if st, err = v.st.Items().Stats(ctx, f.Item.ID); err != nil {
			return "", nil, fmt.Errorf("stats: %w", err)
		}
	}
	return mediaStats, statsBody(v.requestDownloadURI(ctx), st), nil
}

// statsBody is ItemStatsInfo as 7.x's JSON provider writes it, leaving out
// nulls (JsonInclude.Include.NON_NULL): the remote download count and time
// are longs, so they are always there, 0 for a file no smart remote has
// reported (RTFACT-9987 and RTFACT-21564 show both at 0); who downloaded
// is left out until someone has; and origin, which ItemStatsInfo does not
// copy from the statistics it is built from, never appears.
func statsBody(uri string, st model.ItemStats) map[string]any {
	body := map[string]any{
		"uri":                  uri,
		"downloadCount":        st.DownloadCount,
		"lastDownloaded":       unixMilli(st.LastDownloaded),
		"remoteDownloadCount":  st.RemoteDownloadCount,
		"remoteLastDownloaded": unixMilli(st.RemoteLastDownloaded),
	}
	if st.LastDownloadedBy != "" {
		body["lastDownloadedBy"] = st.LastDownloadedBy
	}
	if st.RemoteLastDownloadedBy != "" {
		body["remoteLastDownloadedBy"] = st.RemoteLastDownloadedBy
	}
	return body
}

// unixMilli is t in Unix milliseconds, 0 for never.
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// lastModified renders a file's modification time, or for a folder the
// newest item below it that the caller may read, which is what the view
// reports.
//
// A folder's is folded over the merged walk of its descendants, a store
// page per tree at a time, rather than asked of the store as an aggregate:
// what the caller may read is decided per path by permission patterns the
// store does not hold, and the item reported among several equally new
// ones is the one the collected read met first. That read went in (path,
// name) order, with the folder itself first, so a tie goes to the folder,
// then the lesser (path, name), whatever order the walk meets them in. A
// virtual repository, which has several trees, never gets here: 7.x
// answers its last modified view 400 (getVirtualStorage).
func (v storageView) lastModified(ctx context.Context, f itemkit.Found) (string, any, error) {
	newest := f.Item
	if f.Item.Folder {
		walk, err := v.mergedWalk(ctx, f.Item.FullPath(), true)
		if err != nil {
			return "", nil, err
		}
		pages := kernel.NewPages(walk.read, kernel.MaxLimit)
		var best *fileListEntry
		for {
			entries, ok, err := pages.Next(ctx)
			if err != nil {
				return "", nil, err //nolint:wrapcheck // the walk names the tree it was reading
			}
			if !ok {
				break
			}
			for _, e := range entries {
				switch it := e.item; {
				case it.Modified.After(newest.Modified),
					it.Modified.Equal(newest.Modified) && best != nil && metBefore(e, *best):
					newest, best = it, &e
				}
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

// metBefore reports whether the collected read met e before than: by tree,
// then by the stored (path, name), the order Descendants lists in, in which
// a root-level item's path is ".".
func metBefore(e, than fileListEntry) bool {
	return cmp.Or(
		cmp.Compare(e.tree, than.tree),
		strings.Compare(e.item.Path, than.item.Path),
		strings.Compare(e.item.Name, than.item.Name),
	) < 0
}

// permissions renders who holds which actions on the item, in the legacy
// letters: grants whose target names the item's repository and whose include
// and exclude patterns admit its path.
func (v storageView) permissions(ctx context.Context, f itemkit.Found) (string, any, error) {
	targets := []string{f.Repo.Key, api.TargetAny}
	switch f.Repo.Class {
	case repo.Local, repo.Federated:
		targets = append(targets, api.TargetAnyLocal)
	case repo.Remote:
		targets = append(targets, api.TargetAnyRemote)
	}
	users, groups, err := v.s.cfg.Access.Grants.ItemGrants(
		ctx,
		targets,
		f.Item.FullPath(),
		f.Item.Folder,
	)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // ItemGrants names the target it was reading
	}
	letters := func(m map[string]api.Actions) map[string][]string {
		out := map[string][]string{}
		for name, a := range m {
			if l := a.Letters(); len(l) > 0 {
				out[name] = l
			}
		}
		return out
	}
	return "application/json", map[string]any{
		"uri": v.uri(ctx, f.Item.FullPath()),
		"principals": map[string]any{
			"users":  letters(users),
			"groups": letters(groups),
		},
	}, nil
}

// propertyEdit is a property change: keys to set and keys to remove. stats,
// sent only to PATCH /api/metadata, is merged into the item's download
// statistics (mergeStats); noProps leaves the properties alone.
type propertyEdit struct {
	set                  model.Properties
	remove               []string
	stats                map[string]json.RawMessage
	shouldSkipProperties bool
}

// setProperties answers PUT /api/storage/{repo}/{path}?properties=k=v;k2=v2.
func (s ArtifactoryAPI) setProperties(w http.ResponseWriter, r *http.Request) {
	s.editProperties(w, r, storagePrefix, "recursive", func(q url.Values) (propertyEdit, error) {
		props, err := parseProperties(q.Get("properties"))
		return propertyEdit{set: props}, err
	})
}

// deleteProperties answers DELETE /api/storage/{repo}/{path}?properties=k1,k2.
// Removing a key the item lacks is not an error and answers 204, because
// jf rt delete-props accepts only 204: the CLI tolerates the "No properties
// could be found" 404 when it reads properties, never on a delete
// (research 09).
func (s ArtifactoryAPI) deleteProperties(w http.ResponseWriter, r *http.Request) {
	s.editProperties(w, r, storagePrefix, "recursive", func(q url.Values) (propertyEdit, error) {
		var keys []string
		for _, k := range pkgroute.SplitValues(q.Get("properties")) {
			if k != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return propertyEdit{}, httpkit.BadRequestf("properties names no keys to delete")
		}
		return propertyEdit{remove: keys}, nil
	})
}

// patchMetadata answers PATCH /api/metadata/{repo}/{path} with
// {"props":{"k":"v","k2":["a","b"],"k3":null},"stats":{"downloadCount":3}}:
// in props, strings and arrays replace a key's values and null removes the
// key; stats is merged into a file's download statistics. Either may be left
// out, not both. The edit is always applied in one transaction, so
// atomicProperties=0 behaves like 1.
func (s ArtifactoryAPI) patchMetadata(w http.ResponseWriter, r *http.Request) {
	s.editProperties(
		w,
		r,
		metadataPrefix,
		"recursiveProperties",
		func(url.Values) (propertyEdit, error) {
			return parseMetadataPatch(r)
		},
	)
}

// editProperties applies a property edit to an item and, when recursive (the
// default) and it is a folder, to everything below it, in one transaction
// with the format's Annotated hook for each. Only local repositories and
// remote caches hold editable properties. The repository root is edited too:
// its record is made by the first edit that sets a key, and an edit that only
// removes keys from a root without one has nothing to remove.
func (s ArtifactoryAPI) editProperties(
	w http.ResponseWriter,
	r *http.Request,
	prefix, recursiveParam string,
	parse func(url.Values) (propertyEdit, error),
) {
	ctx := r.Context()
	v, p, q, err := s.storageRequest(r, prefix)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	if !v.acc.File(v.repo, v.path, api.ActionAnnotate) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	switch {
	case v.repo.Class == repo.Virtual:
		// 7.x's answer is [I]: its properties resource is closed code,
		// JFrog's property pages name local and cache repositories only,
		// RTFACT-17097 saw a 6.0 set through a virtual answer success and
		// change nothing, and RTFACT-29704 asked 7.x to refuse it.
		httpkit.WriteErr(
			w,
			r,
			httpkit.BadRequestf("properties of virtual repository '%s' cannot be changed; "+
				"address the member that holds the item", v.key),
		)
		return
	case v.repo.Class == repo.Remote && !v.isCached:
		httpkit.WriteErr(
			w,
			r,
			httpkit.BadRequestf("properties of remote repository '%s' are set on its cache, '%s'",
				v.key, v.repo.Key+model.CacheSuffix),
		)
		return
	}
	recursive, err := flag(q, recursiveParam, true)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	edit, err := parse(q)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	cached := v.isCached || v.repo.Class == repo.Remote
	items := s.Items(v.st)
	err = s.Tx(ctx, func(ctx context.Context) error {
		p, acc, err := s.ItemCaller(ctx, v.st)
		if err != nil {
			return err
		}
		if !acc.File(v.repo, v.path, api.ActionAnnotate) {
			return httpkit.Denied(p)
		}
		v.acc = acc
		apply := func(it model.Item) error {
			return items.Annotate(ctx, v.repo, it, edit.set, edit.remove)
		}
		target, ok, err := s.editTarget(ctx, v, cached, len(edit.set) > 0, p.Name)
		if err != nil {
			return err
		}
		if edit.stats != nil {
			if err := setStats(ctx, v.st, target, edit.stats); err != nil {
				return err
			}
		}
		if edit.shouldSkipProperties {
			return nil
		}
		if ok {
			if err := apply(target); err != nil {
				return err //nolint:wrapcheck // mapped by ItemError
			}
		}
		if !recursive || !target.Folder {
			return nil
		}
		page := kernel.Page{Limit: kernel.MaxLimit}
		for {
			batch, next, err := v.st.Items().
				Descendants(ctx, v.repo.Key, cached, target.FullPath(), page)
			if err != nil {
				return fmt.Errorf("descendants: %w", err)
			}
			for _, it := range batch {
				if err := apply(it); err != nil {
					return err //nolint:wrapcheck // as above
				}
			}
			if next == "" {
				return nil
			}
			page.After = next
		}
	})
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "edit properties", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// editTarget is the item a property edit names: the item at v.path, or for
// the root its record, made first when create is set. ok is false for a root
// without a record, which the edit passes over; the item returned is then
// the root, so a recursive edit still reaches everything below it.
func (s ArtifactoryAPI) editTarget(
	ctx context.Context,
	v storageView,
	cached, create bool,
	by string,
) (model.Item, bool, error) {
	if v.path != "" {
		it, err := v.st.Items().Get(ctx, v.repo.Key, cached, v.path)
		return it, err == nil, err //nolint:wrapcheck // mapped by ItemError
	}
	if create {
		it, err := s.Items(v.st).EnsureRoot(ctx, item.Target{Repo: v.repo, Cached: cached}, by)
		return it, err == nil, err //nolint:wrapcheck // the item service wraps its own
	}
	it, err := v.st.Items().Root(ctx, v.repo.Key, cached)
	if errors.Is(err, kernel.ErrNotFound) {
		return model.RootItem(v.repo.Key, cached), false, nil
	}
	return it, err == nil, err //nolint:wrapcheck // mapped by ItemError
}

// parseProperties parses the properties API's "k=v1,v2;k2=v3": ";" or "|"
// between properties, "," between values, and a backslash escaping any of
// them or "=" inside a value. A key repeated adds values.
func parseProperties(s string) (model.Properties, error) {
	props := model.Properties{}
	for _, p := range splitUnescaped(s, ";|") {
		if p == "" {
			continue
		}
		kv := splitUnescaped(p, "=")
		key := unescapeBackslashes(kv[0])
		if err := model.ValidatePropertyKey(key); err != nil {
			return nil, httpkit.BadRequestf("%v", err)
		}
		if len(kv) < 2 {
			return nil, httpkit.BadRequestf("property %q has no value", key)
		}
		props[key] = append(props[key], pkgroute.SplitValues(strings.Join(kv[1:], "="))...)
	}
	if len(props) == 0 {
		return nil, httpkit.BadRequestf("properties names nothing to set")
	}
	return props, nil
}

// splitUnescaped splits s at each byte of seps not escaped by a backslash,
// keeping the escapes for the next level to read.
func splitUnescaped(s, seps string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case strings.IndexByte(seps, s[i]) >= 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unescapeBackslashes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseMetadataPatch reads {"props":{...},"stats":{...}}. An empty array
// removes a key, as a key with no values is not stored. A body with neither
// field is refused with 6.20's message (ArtifactMetadataResource); null
// counts as absent.
func parseMetadataPatch(r *http.Request) (propertyEdit, error) {
	var body struct {
		Props map[string]json.RawMessage `json:"props"`
		Stats map[string]json.RawMessage `json:"stats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return propertyEdit{}, httpkit.BadRequestf("malformed JSON body: %v", err)
	}
	if body.Props == nil && body.Stats == nil {
		return propertyEdit{}, httpkit.BadRequestf("props or stats fields required")
	}
	// "props":{} changes nothing, so it walks nothing either.
	edit := propertyEdit{set: model.Properties{}, shouldSkipProperties: len(body.Props) == 0}
	if body.Stats != nil {
		// Checked against a zero record now, so a bad field answers 400
		// before the transaction starts.
		if err := mergeStats(&model.ItemStats{}, body.Stats); err != nil {
			return propertyEdit{}, err
		}
		edit.stats = body.Stats
	}
	for key, raw := range body.Props {
		if err := model.ValidatePropertyKey(key); err != nil {
			return propertyEdit{}, httpkit.BadRequestf("%v", err)
		}
		var one string
		var many []string
		switch {
		case string(raw) == "null":
			edit.remove = append(edit.remove, key)
		case json.Unmarshal(raw, &one) == nil:
			edit.set[key] = []string{one}
		case json.Unmarshal(raw, &many) == nil && len(many) == 0:
			edit.remove = append(edit.remove, key)
		case json.Unmarshal(raw, &many) == nil:
			edit.set[key] = many
		default:
			return propertyEdit{}, httpkit.BadRequestf(
				"the value of %q must be a string, an array of strings or null", key)
		}
	}
	slices.Sort(edit.remove)
	return edit, nil
}
