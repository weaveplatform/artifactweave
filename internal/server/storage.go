package server

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

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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

// noProperties is the 404 for a properties view with nothing to show, in
// the wording the JFrog CLI tolerates on a properties delete (research 09).
func noProperties() error {
	return apierror.New(http.StatusNotFound, "No properties could be found.")
}

// registerStorageRoutes serves the storage API (/api/storage) and the
// partial property update (/api/metadata) under the patterns chiPatterns
// derives from the spec, replacing their 501 stubs. The bare /{repoKey}
// form adds the repository root, which the spec addresses with an empty
// item path.
func (s artifactoryService) registerStorageRoutes(r chi.Router) {
	for _, p := range append(chiPatterns(storagePrefix+"{repoKey}/{itemPath}"), storagePrefix+"{repoKey}") {
		r.Get(p, s.getStorage)
		r.Put(p, s.setProperties)
		r.Delete(p, s.deleteProperties)
	}
	for _, p := range append(chiPatterns(metadataPrefix+"{repoKey}/{itemPath}"), metadataPrefix+"{repoKey}") {
		r.With(bodyLimit(managementBodyLimit)).Patch(p, s.patchMetadata)
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
			return nil, badRequest("malformed query parameter %q", k)
		}
		value, err := url.QueryUnescape(v)
		if err != nil {
			return nil, badRequest("malformed value of query parameter %q", key)
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
	return false, badRequest("%s must be 0 or 1", name)
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
			return "", badRequest("only one of %s may be given", strings.Join(storageModes, ", "))
		}
		mode = m
	}
	if mode != "list" {
		for _, p := range listingParams {
			if q.Has(p) {
				return "", badRequest("%s is a listing parameter and needs list", p)
			}
		}
	}
	return mode, nil
}

// storageView is one storage request: the repository as the client named
// it, the resolved configuration, and what the caller may do.
type storageView struct {
	s      artifactoryService
	st     store.Store
	key    string
	path   string
	repo   repo.Config
	cached bool
	acc    access
}

// uri is the storage API URL of p (a full path, "" for the root) under the
// key the client used.
func (v storageView) uri(ctx context.Context, p string) string {
	u := baseurl.From(ctx) + storagePrefix + v.key
	if p != "" {
		u += "/" + escapePath(p)
	}
	return u
}

func (v storageView) downloadURI(ctx context.Context, p string) string {
	return baseurl.From(ctx) + "/artifactory/" + v.key + "/" + escapePath(p)
}

// target finds the item the request names, through a virtual repository's
// members when it is one. The root is a folder with no stored item.
func (v storageView) target(ctx context.Context) (found, error) {
	if v.path == "" {
		return found{repo: v.repo, item: types.Item{
			RepoKey: v.repo.Key, Cached: v.cached, Folder: true,
		}}, nil
	}
	// The storage API describes the item at the path as given; only
	// downloads resolve it through the format.
	return lookup(ctx, v.st, v.repo, v.cached, v.path, v.acc, nil, map[string]bool{})
}

// trees are the repository trees a folder view reads: the repository, or a
// virtual repository's readable members, in order.
func (v storageView) trees(
	ctx context.Context,
	c repo.Config,
	seen map[string]bool,
) ([]found, error) {
	if seen[c.Key] {
		return nil, nil
	}
	seen[c.Key] = true
	if c.Class != repo.Virtual {
		cached := c.Class == repo.Remote || (c.Key == v.repo.Key && v.cached)
		return []found{{repo: c, item: types.Item{RepoKey: c.Key, Cached: cached}}}, nil
	}
	var out []found
	for _, key := range c.Repositories {
		m, err := v.st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("member %s of %s: %w", key, c.Key, err)
		}
		if !v.acc.folder(m, v.path, types.ActionRead) {
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
func (v storageView) below(ctx context.Context, folder string, deep bool) ([]types.Item, error) {
	trees, err := v.trees(ctx, v.repo, map[string]bool{})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []types.Item
	for _, t := range trees {
		list := v.st.Items().Children
		if deep {
			list = v.st.Items().Descendants
		}
		page := store.Page{Limit: store.MaxLimit}
		for {
			batch, next, err := list(ctx, t.repo.Key, t.item.Cached, folder, page)
			if err != nil {
				return nil, fmt.Errorf("list %s/%s: %w", t.repo.Key, folder, err)
			}
			for _, it := range batch {
				// What the caller may not see is left out; a folder shows
				// when something below it is readable.
				if !seen[it.FullPath()] &&
					v.acc.item(t.repo, it.FullPath(), it.Folder, types.ActionRead) {
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
func (s artifactoryService) getStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, p, q, err := s.storageRequest(r, storagePrefix)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	mode, err := storageMode(q)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// A folder check first, as for a download; the exact right once the
	// item is found.
	if !v.acc.folder(v.repo, v.path, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	switch {
	case mode == "list" && p.Anonymous,
		mode == "lastModified" && !v.acc.folder(v.repo, v.path, types.ActionWrite),
		mode == "permissions" && !p.Admin:
		writeErr(w, r, denied(p))
		return
	}
	f, err := v.target(ctx)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "storage item", err))
		return
	}
	if !v.acc.item(f.repo, f.item.FullPath(), f.item.Folder, types.ActionRead) {
		writeErr(w, r, denied(p))
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
		writeErr(w, r, s.itemError(ctx, "storage "+mode, err))
		return
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// storageRequest parses a storage or metadata request and resolves its
// repository and caller.
func (s artifactoryService) storageRequest(
	r *http.Request,
	prefix string,
) (storageView, auth.Principal, url.Values, error) {
	ctx := r.Context()
	var v storageView
	st, err := s.content()
	if err != nil {
		return v, auth.Principal{}, nil, err
	}
	req, err := parseEscapedItemPath(strings.TrimPrefix(r.URL.EscapedPath(), prefix))
	if err != nil {
		return v, auth.Principal{}, nil, err
	}
	q, err := rawQuery(r.URL.RawQuery)
	if err != nil {
		return v, auth.Principal{}, nil, err
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return v, p, nil, err
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err != nil {
		return v, p, nil, s.itemError(ctx, "resolve repository", err)
	}
	return storageView{
		s: s, st: st, key: req.key, path: req.path, repo: c, cached: cached, acc: acc,
	}, p, q, nil
}

// putTime sets key to t in Artifactory's form, leaving it out when t is
// unknown (the repository root has no stored item).
func putTime(body map[string]any, key string, t time.Time) {
	if !t.IsZero() {
		body[key] = formatTime(t)
	}
}

func (v storageView) info(ctx context.Context, f found) (string, any, error) {
	it := f.item
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
		body["checksums"] = checksumsJSON(it.Actual)
		body["originalChecksums"] = checksumsJSON(it.Original)
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
func (v storageView) list(ctx context.Context, f found, q url.Values) (string, any, error) {
	if !f.item.Folder {
		return "", nil, badRequest("%s is a file; list needs a folder", f.item.StoragePath())
	}
	opts := map[string]bool{}
	for _, name := range []string{"deep", "listFolders", "mdTimestamps", "includeRootPath"} {
		on, err := flag(q, name, false)
		if err != nil {
			return "", nil, err
		}
		opts[name] = on
	}
	depth := -1
	if d := q.Get("depth"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return "", nil, badRequest("depth must be a non-negative integer")
		}
		depth = n
	}
	folder := f.item.FullPath()
	items, err := v.below(ctx, folder, opts["deep"])
	if err != nil {
		return "", nil, err
	}
	base := segments(folder)
	files := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if it.Folder && !opts["listFolders"] {
			continue
		}
		// depth counts levels below the listed folder; 0 is unlimited, as a
		// deep listing without it.
		if depth > 0 && segments(it.FullPath())-base > depth {
			continue
		}
		entry := map[string]any{"folder": it.Folder, "size": "-1"}
		entry["uri"] = "/" + strings.TrimPrefix(strings.TrimPrefix(it.FullPath(), folder), "/")
		if opts["includeRootPath"] {
			entry["uri"] = it.StoragePath()
		}
		putTime(entry, "lastModified", it.Modified)
		if !it.Folder {
			entry["size"] = strconv.FormatInt(it.Size, 10)
			entry["sha1"] = it.Actual.SHA1
		}
		if opts["mdTimestamps"] {
			// Metadata write times are not recorded per kind; the map is
			// present, as clients that ask for it expect, but empty.
			entry["mdTimestamps"] = map[string]string{}
		}
		files = append(files, entry)
	}
	slices.SortFunc(files, func(a, b map[string]any) int {
		return strings.Compare(
			a["uri"].(string),
			b["uri"].(string),
		) //nolint:forcetypeassert // set above
	})
	return mediaFileList, map[string]any{
		"uri":     v.uri(ctx, folder),
		"created": formatTime(time.Now()),
		"files":   files,
	}, nil
}

func segments(p string) int {
	if p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// properties renders an item's properties, or only the keys named in
// "k1,k2"; nothing to show is a 404.
func (v storageView) properties(ctx context.Context, f found, names string) (string, any, error) {
	if f.item.ID == 0 {
		return "", nil, noProperties()
	}
	props, err := v.st.Items().Properties(ctx, f.item.ID)
	if err != nil {
		return "", nil, fmt.Errorf("properties: %w", err)
	}
	if names != "" {
		want := splitValues(names)
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
		"uri":        v.uri(ctx, f.item.FullPath()),
		"properties": props,
	}, nil
}

// stats renders a file's download statistics. A folder has none.
func (v storageView) stats(ctx context.Context, f found) (string, any, error) {
	if f.item.Folder {
		return "", nil, badRequest("%s is a folder; statistics are kept for files",
			f.item.StoragePath())
	}
	st, err := v.st.Items().Stats(ctx, f.item.ID)
	if err != nil {
		return "", nil, fmt.Errorf("stats: %w", err)
	}
	var last int64
	if !st.LastDownloaded.IsZero() {
		last = st.LastDownloaded.UnixMilli()
	}
	body := map[string]any{
		"uri":            v.uri(ctx, f.item.FullPath()),
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
func (v storageView) lastModified(ctx context.Context, f found) (string, any, error) {
	newest := f.item
	if f.item.Folder {
		items, err := v.below(ctx, f.item.FullPath(), true)
		if err != nil {
			return "", nil, err
		}
		for _, it := range items {
			if it.Modified.After(newest.Modified) {
				newest = it
			}
		}
		if newest.Modified.IsZero() {
			return "", nil, itemNotFound()
		}
	}
	return mediaLastModified, map[string]any{
		"uri":          v.uri(ctx, newest.FullPath()),
		"lastModified": formatTime(newest.Modified),
	}, nil
}

// permissions renders who holds which actions on the item, in the legacy
// letters: grants whose target names the item's repository and whose include
// and exclude patterns admit its path.
func (v storageView) permissions(ctx context.Context, f found) (string, any, error) {
	targets := []string{f.repo.Key, types.TargetAny}
	switch f.repo.Class {
	case repo.Local, repo.Federated:
		targets = append(targets, types.TargetAnyLocal)
	case repo.Remote:
		targets = append(targets, types.TargetAnyRemote)
	}
	users, groups := map[string]types.Actions{}, map[string]types.Actions{}
	seen := map[string]bool{}
	for _, t := range targets {
		perms, err := v.st.Permissions().WithTarget(ctx, t)
		if err != nil {
			return "", nil, fmt.Errorf("permissions with target %s: %w", t, err)
		}
		for _, perm := range perms {
			res, ok := perm.Resources[types.ResourceArtifact]
			if seen[perm.Name] || !ok {
				continue
			}
			tg, named := res.Targets[t]
			if !named || !authz.TargetAllows(tg, f.item.FullPath(), f.item.Folder) {
				continue
			}
			seen[perm.Name] = true
			for u, a := range res.Actions.Users {
				users[u] |= a
			}
			for g, a := range res.Actions.Groups {
				groups[g] |= a
			}
		}
	}
	letters := func(m map[string]types.Actions) map[string][]string {
		out := map[string][]string{}
		for name, a := range m {
			if l := a.Letters(); len(l) > 0 {
				out[name] = l
			}
		}
		return out
	}
	return "application/json", map[string]any{
		"uri": v.uri(ctx, f.item.FullPath()),
		"principals": map[string]any{
			"users":  letters(users),
			"groups": letters(groups),
		},
	}, nil
}

// propertyEdit is a property change: keys to set and keys to remove.
type propertyEdit struct {
	set    types.Properties
	remove []string
}

// setProperties answers PUT /api/storage/{repo}/{path}?properties=k=v;k2=v2.
func (s artifactoryService) setProperties(w http.ResponseWriter, r *http.Request) {
	s.editProperties(w, r, storagePrefix, "recursive", func(q url.Values) (propertyEdit, error) {
		props, err := parseProperties(q.Get("properties"))
		return propertyEdit{set: props}, err
	})
}

// deleteProperties answers DELETE /api/storage/{repo}/{path}?properties=k1,k2.
// Removing a key the item lacks is not an error (the spec's contract; the
// JFrog CLI also tolerates Artifactory's 404 for it).
func (s artifactoryService) deleteProperties(w http.ResponseWriter, r *http.Request) {
	s.editProperties(w, r, storagePrefix, "recursive", func(q url.Values) (propertyEdit, error) {
		var keys []string
		for _, k := range splitValues(q.Get("properties")) {
			if k != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			return propertyEdit{}, badRequest("properties names no keys to delete")
		}
		return propertyEdit{remove: keys}, nil
	})
}

// patchMetadata answers PATCH /api/metadata/{repo}/{path} with
// {"props":{"k":"v","k2":["a","b"],"k3":null}}: strings and arrays replace a
// key's values, null removes the key. The edit is always applied in one
// transaction, so atomicProperties=0 behaves like 1.
func (s artifactoryService) patchMetadata(w http.ResponseWriter, r *http.Request) {
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
// remote caches hold editable properties.
func (s artifactoryService) editProperties(
	w http.ResponseWriter,
	r *http.Request,
	prefix, recursiveParam string,
	parse func(url.Values) (propertyEdit, error),
) {
	ctx := r.Context()
	v, p, q, err := s.storageRequest(r, prefix)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !v.acc.file(v.repo, v.path, types.ActionAnnotate) {
		writeErr(w, r, denied(p))
		return
	}
	switch {
	case v.repo.Class == repo.Virtual:
		writeErr(w, r, badRequest("properties of virtual repository '%s' cannot be changed; "+
			"address the member that holds the item", v.key))
		return
	case v.repo.Class == repo.Remote && !v.cached:
		writeErr(w, r, badRequest("properties of remote repository '%s' are set on its cache, '%s'",
			v.key, v.repo.Key+types.CacheSuffix))
		return
	}
	recursive, err := flag(q, recursiveParam, true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	edit, err := parse(q)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	cached := v.cached || v.repo.Class == repo.Remote
	items := s.items(v.st)
	err = v.st.Tx(ctx, func(ctx context.Context) error {
		apply := func(it types.Item) error {
			return items.Annotate(ctx, v.repo, it, edit.set, edit.remove)
		}
		var target types.Item
		folder := true
		if v.path != "" {
			it, err := v.st.Items().Get(ctx, v.repo.Key, cached, v.path)
			if err != nil {
				return err //nolint:wrapcheck // mapped by itemError
			}
			if err := apply(it); err != nil {
				return err //nolint:wrapcheck // as above
			}
			target, folder = it, it.Folder
		}
		if !recursive || !folder {
			return nil
		}
		page := store.Page{Limit: store.MaxLimit}
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
		writeErr(w, r, s.itemError(ctx, "edit properties", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseProperties parses the properties API's "k=v1,v2;k2=v3": ";" or "|"
// between properties, "," between values, and a backslash escaping any of
// them or "=" inside a value. A key repeated adds values.
func parseProperties(s string) (types.Properties, error) {
	props := types.Properties{}
	for _, p := range splitUnescaped(s, ";|") {
		if p == "" {
			continue
		}
		kv := splitUnescaped(p, "=")
		key := unescapeBackslashes(kv[0])
		if err := types.ValidatePropertyKey(key); err != nil {
			return nil, badRequest("invalid property key %q", key)
		}
		if len(kv) < 2 {
			return nil, badRequest("property %q has no value", key)
		}
		props[key] = append(props[key], splitValues(strings.Join(kv[1:], "="))...)
	}
	if len(props) == 0 {
		return nil, badRequest("properties names nothing to set")
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

// parseMetadataPatch reads {"props":{...}}. An empty array removes the key,
// as a key with no values is not stored.
func parseMetadataPatch(r *http.Request) (propertyEdit, error) {
	var body struct {
		Props map[string]json.RawMessage `json:"props"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return propertyEdit{}, badRequest("malformed JSON body: %v", err)
	}
	if len(body.Props) == 0 {
		return propertyEdit{}, badRequest("props is missing or empty")
	}
	edit := propertyEdit{set: types.Properties{}}
	for key, raw := range body.Props {
		if err := types.ValidatePropertyKey(key); err != nil {
			return propertyEdit{}, badRequest("invalid property key %q", key)
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
			return propertyEdit{}, badRequest(
				"the value of %q must be a string, an array of strings or null", key)
		}
	}
	slices.Sort(edit.remove)
	return edit, nil
}
