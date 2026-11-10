package artifactshttp

import (
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

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/middleware"
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
	mediaStats        = "application/vnd.org.jfrog.storage.StatsInfo+json"
)

// noProperties is the 404 for a properties view with nothing to show. The
// wording matters: jfrog-client-go's GetItemProperties reads a 404 as "no
// properties" only when the body says this, and fails on any other 404
// (research 09).
func noProperties() error {
	return apierror.New(http.StatusNotFound, "No properties could be found.")
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
// it, the resolved configuration, and what the caller may do.
type storageView struct {
	s        ArtifactoryAPI
	st       Store
	key      string
	path     string
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

// target finds the item the request names, through a virtual repository's
// members when it is one. The root is a folder that carries only the ID of
// its record and when its properties last changed, when properties have
// given it one: the other root views describe the repository the same way
// whether or not it has a record.
func (v storageView) target(ctx context.Context) (itemkit.Found, error) {
	if v.path == "" {
		root := model.Item{RepoKey: v.repo.Key, Cached: v.isCached, Folder: true}
		if v.repo.Class != repo.Virtual {
			rec, err := v.st.Items().
				Root(ctx, v.repo.Key, v.isCached || v.repo.Class == repo.Remote)
			switch {
			case err == nil:
				root.ID, root.PropertiesModified = rec.ID, rec.PropertiesModified
			case !errors.Is(err, kernel.ErrNotFound):
				return itemkit.Found{}, fmt.Errorf("repository root: %w", err)
			}
		}
		return itemkit.Found{Repo: v.repo, Item: root}, nil
	}
	// The storage API describes the item at the path as given; only
	// downloads resolve it through the format.
	return itemkit.Lookup(ctx, v.st, v.repo, v.isCached, v.path, v.acc, nil, map[string]bool{})
}

// trees are the repository trees a folder view reads: the repository, or a
// virtual repository's readable members, in order.
func (v storageView) trees(
	ctx context.Context,
	c repo.Config,
	seen map[string]bool,
) ([]itemkit.Found, error) {
	if seen[c.Key] {
		return nil, nil
	}
	seen[c.Key] = true
	if c.Class != repo.Virtual {
		cached := c.Class == repo.Remote || (c.Key == v.repo.Key && v.isCached)
		return []itemkit.Found{{Repo: c, Item: model.Item{RepoKey: c.Key, Cached: cached}}}, nil
	}
	var out []itemkit.Found
	for _, key := range c.Repositories {
		m, err := v.st.Repos().Get(ctx, key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("member %s of %s: %w", key, c.Key, err)
		}
		if !v.acc.Folder(m, v.path, api.ActionRead) {
			continue
		}
		sub, err := v.trees(ctx, m, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// below lists what lies under folder in every tree the view reads, direct
// children or (deep) every descendant. A name that several members hold is
// reported once, from the first member, as a download would resolve it.
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
					v.acc.Item(t.Repo, it.FullPath(), it.Folder, api.ActionRead) {
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

// getStorage answers GET /api/storage/{repo}/{path}: file or folder info,
// or the view one mode parameter selects.
func (s ArtifactoryAPI) getStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, p, q, err := s.storageRequest(r, storagePrefix)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	mode, err := storageMode(q)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	// A folder check first, as for a download; the exact right once the
	// item is found.
	if !v.acc.Folder(v.repo, v.path, api.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	switch {
	case mode == "list" && p.Anonymous,
		mode == "lastModified" && !v.acc.Folder(v.repo, v.path, api.ActionWrite),
		mode == "permissions" && !p.Admin:
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	// The file list of a blacked-out repository answers 404 with
	// BlackedOutException's message (ArtifactResource.writeStreamingFileList).
	// The other views describe the item regardless, as 6.20's do: its item
	// info checks read permission alone (prepareStorageInfoResponse).
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
	if !v.acc.Item(f.Repo, f.Item.FullPath(), f.Item.Folder, api.ActionRead) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	var (
		media string
		body  any
	)
	switch mode {
	case "":
		media, body, err = v.info(ctx, f)
	case "list":
		media, body, err = v.list(ctx, f, q)
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
	c, cached, err := itemkit.ResolveRepo(ctx, st, req.Key)
	if err != nil {
		return v, p, nil, s.ItemError(ctx, "resolve repository", err)
	}
	return storageView{
		s: s, st: st, key: req.Key, path: req.Path, repo: c, isCached: cached, acc: acc,
	}, p, q, nil
}

// putTime sets key to t in Artifactory's form, leaving it out when t is
// unknown (the repository root has no stored item).
func putTime(body map[string]any, key string, t time.Time) {
	if !t.IsZero() {
		body[key] = httpkit.FormatTime(t)
	}
}

func (v storageView) info(ctx context.Context, f itemkit.Found) (string, any, error) {
	it := f.Item
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
		return mediaFileInfo, body, nil
	}
	children, err := v.below(ctx, it.FullPath(), false)
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

// list renders the FileList view of a folder.
func (v storageView) list(ctx context.Context, f itemkit.Found, q url.Values) (string, any, error) {
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
// items below it.
func (l listing) render(uri, folder string, self model.Item, items []listed) map[string]any {
	base := segments(folder)
	files := make([]map[string]any, 0, len(items)+1)
	if l.shouldIncludeRootPath {
		// The listed folder itself, at "/" relative to itself. The
		// Terraform provider's TestAccDataSourceFileList_include_root_path
		// expects this entry first at a repository root, with size -1.
		files = append(files, listEntry(self, "/", l.shouldIncludeMDTimestamps))
	}
	for _, it := range items {
		if it.item.Folder && !l.shouldListFolders ||
			l.depth > 0 && segments(it.path)-base > l.depth {
			continue
		}
		rel := "/" + strings.TrimPrefix(strings.TrimPrefix(it.path, folder), "/")
		files = append(files, listEntry(it.item, rel, l.shouldIncludeMDTimestamps))
	}
	slices.SortFunc(files, func(a, b map[string]any) int {
		return strings.Compare(
			a["uri"].(string),
			b["uri"].(string),
		) //nolint:forcetypeassert // set above
	})
	return map[string]any{"uri": uri, "created": httpkit.FormatTime(time.Now()), "files": files}
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
		"uri":        v.uri(ctx, f.Item.FullPath()),
		"properties": props,
	}, nil
}

// stats renders a file's download statistics. A folder has none.
func (v storageView) stats(ctx context.Context, f itemkit.Found) (string, any, error) {
	if f.Item.Folder {
		return "", nil, httpkit.BadRequestf("%s is a folder; statistics are kept for files",
			f.Item.StoragePath())
	}
	st, err := v.st.Items().Stats(ctx, f.Item.ID)
	if err != nil {
		return "", nil, fmt.Errorf("stats: %w", err)
	}
	var last int64
	if !st.LastDownloaded.IsZero() {
		last = st.LastDownloaded.UnixMilli()
	}
	body := map[string]any{
		"uri":            v.uri(ctx, f.Item.FullPath()),
		"downloadCount":  st.DownloadCount,
		"lastDownloaded": last,
	}
	if st.LastDownloadedBy != "" {
		body["lastDownloadedBy"] = st.LastDownloadedBy
	}
	return mediaStats, body, nil
}

// lastModified renders a file's modification time, or for a folder the
// newest item below it, which is what the view reports.
func (v storageView) lastModified(ctx context.Context, f itemkit.Found) (string, any, error) {
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
	err = v.st.Tx(ctx, func(ctx context.Context) error {
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
