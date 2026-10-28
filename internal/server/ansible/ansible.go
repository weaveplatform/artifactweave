// Package ansible serves Galaxy v3 collection requests through the shared
// package protocol, source and item boundaries.
package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/auth"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/format/ansible"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxPageBytes = 2 << 20

func Protocol(remote *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		// Virtual metadata selects concrete members before any source read.
		repo.Virtual: pkgroute.Local{},
	}
	if remote != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: remote}
	}
	return pkgroute.Protocol{
		Name: "ansible", Types: []repo.PackageType{repo.Ansible},
		ErrorProfile: apierror.ProfileArtifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/", h.read)
			r.Get("/*", h.read)
			r.Head("/", h.read)
			r.Head("/*", h.read)
			r.Post("/api/v3/artifacts/collections", h.publish)
			r.Post("/api/v3/artifacts/collections/", h.publish)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (h handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	p, _ := auth.FromContext(r.Context())
	h.k.Error(w, r, pkgroute.SourceError(err, p.Anonymous || p.Name == ""))
}

func isMissing(err error) bool {
	var e *apierror.Error
	return errors.Is(err, store.ErrNotFound) ||
		errors.As(err, &e) && e.Status == http.StatusNotFound
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	s := p.Segments
	if len(s) == 0 {
		writeJSON(
			w,
			r,
			http.StatusOK,
			map[string]any{"available_versions": map[string]string{"v3": "api/v3/"}},
		)
		return
	}
	if len(s) < 2 || s[0] != "api" || s[1] != "v3" {
		h.fail(w, r, missing())
		return
	}
	s = s[2:]
	switch {
	case len(s) == 3 && s[0] == "collections":
		h.collection(w, r, s[1], s[2])
	case len(s) == 4 && s[0] == "collections" && s[3] == "versions":
		h.versions(w, r, s[1], s[2])
	case len(s) == 5 && s[0] == "collections" && s[3] == "versions":
		h.version(w, r, s[1], s[2], s[4])
	case len(s) == 3 && s[0] == "imports" && s[1] == "collections":
		h.importTask(w, r, s[2])
	case len(s) == 7 && slices.Equal(s[:6], []string{"plugin", "ansible", "content", "published", "collections", "artifacts"}):
		h.artifact(w, r, s[6])
	default:
		h.fail(w, r, missing())
	}
}

func missing() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func validIdentity(ns, name string) bool {
	return ansiblefmt.ValidName(ns) && ansiblefmt.ValidName(name)
}

func (h handler) checkIdentity(w http.ResponseWriter, r *http.Request, ns, name string) bool {
	if !validIdentity(ns, name) {
		h.fail(w, r, apierror.New(http.StatusBadRequest, "invalid collection identity"))
		return false
	}
	return h.k.RequireFolder(w, r, "collections/"+ns+"/"+name, types.ActionRead)
}

type release struct {
	Meta   ansiblefmt.Metadata
	At     time.Time
	Req    *http.Request
	finish func(context.Context) error
}

type catalog struct {
	items  []release
	checks []func(context.Context) error
}

func (c catalog) finish(ctx context.Context) error {
	for _, check := range c.checks {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (h handler) localVersions(r *http.Request, ns, name string) ([]release, error) {
	key := pkgroute.RequestFrom(r).Repo.Key
	p, err := h.k.DB().Packages().Find(r.Context(), key, ns+"."+name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ansible: package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return nil, fmt.Errorf("ansible: versions: %w", err)
	}
	out := make([]release, 0, len(versions))
	for _, v := range versions {
		if !h.k.Can(r, ansiblefmt.Path(ns, name, v.Version), types.ActionRead) {
			continue
		}
		var m ansiblefmt.Metadata
		if err := json.Unmarshal(v.Metadata, &m); err != nil ||
			m.Namespace != ns || m.Name != name || m.Version != v.Version {
			return nil, fmt.Errorf("ansible: corrupt package metadata: %w", ansiblefmt.ErrArchive)
		}
		it, err := h.k.Item(r, ansiblefmt.Path(ns, name, v.Version))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ansible: collection item: %w", err)
		}
		if it.Item.Folder || it.Item.Actual.SHA256 != m.SHA256 {
			return nil, fmt.Errorf("ansible: corrupt indexed collection: %w", ansiblefmt.ErrArchive)
		}
		out = append(out, release{Meta: m, At: v.Created, Req: r})
	}
	return out, nil
}

func (h handler) memberRequests(r *http.Request) ([]*http.Request, error) {
	q := pkgroute.RequestFrom(r)
	if q.Repo.Class != repo.Virtual {
		return []*http.Request{r}, nil
	}
	out := make([]*http.Request, 0, len(q.Repo.Repositories))
	for _, key := range q.Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ansible: virtual member: %w", err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("ansible: member request: %w", err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) releases(r *http.Request, ns, name string) (catalog, error) {
	members, err := h.memberRequests(r)
	if err != nil {
		return catalog{}, err
	}
	byVersion := map[string]bool{}
	var out catalog
	for _, member := range members {
		if !h.k.CanFolder(member, "collections/"+ns+"/"+name, types.ActionRead) {
			continue
		}
		var releases []release
		if pkgroute.RequestFrom(member).Repo.Class == repo.Remote {
			remote, readErr := h.remoteVersions(member, ns, name)
			releases, err = remote.items, readErr
			out.checks = append(out.checks, remote.checks...)
		} else {
			releases, err = h.localVersions(member, ns, name)
		}
		if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual &&
			(isMissing(err) || errors.Is(err, source.ErrDenied)) {
			continue
		}
		if err != nil {
			return catalog{}, err
		}
		for _, rel := range releases {
			if !byVersion[rel.Meta.Version] && h.k.Can(r,
				ansiblefmt.Path(ns, name, rel.Meta.Version), types.ActionRead) {
				out.items = append(out.items, rel)
				byVersion[rel.Meta.Version] = true
			}
		}
	}
	sort.Slice(out.items, func(i, j int) bool {
		return semver.Compare("v"+out.items[i].Meta.Version, "v"+out.items[j].Meta.Version) > 0
	})
	return out, nil
}

func (h handler) collection(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !h.checkIdentity(w, r, ns, name) {
		return
	}
	catalog, err := h.releases(r, ns, name)
	rels := catalog.items
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := catalog.finish(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}
	if len(rels) == 0 {
		h.fail(w, r, missing())
		return
	}
	created, updated := rels[0].At, rels[0].At
	for _, rel := range rels[1:] {
		if rel.At.Before(created) {
			created = rel.At
		}
		if rel.At.After(updated) {
			updated = rel.At
		}
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"namespace": map[string]string{"name": ns}, "name": name,
		"created_at": created.UTC().Format(time.RFC3339Nano),
		"updated_at": updated.UTC().Format(time.RFC3339Nano),
	})
}

func pagination(r *http.Request, count int) (offset, limit int, err error) {
	offset, limit = 0, 100
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 || offset > 100000 {
			return 0, 0, apierror.New(http.StatusBadRequest, "invalid offset")
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, apierror.New(http.StatusBadRequest, "invalid limit")
		}
	}
	return min(offset, count), limit, nil
}

func (h handler) versions(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !h.checkIdentity(w, r, ns, name) {
		return
	}
	catalog, err := h.releases(r, ns, name)
	rels := catalog.items
	if err != nil {
		h.fail(w, r, err)
		return
	}
	offset, limit, err := pagination(r, len(rels))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	end := min(offset+limit, len(rels))
	data := make([]map[string]string, 0, end-offset)
	for _, rel := range rels[offset:end] {
		data = append(data, map[string]string{"version": rel.Meta.Version})
	}
	var next *string
	if end < len(rels) {
		v := "/artifactory/api/ansible/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) +
			"/api/v3/collections/" + ns + "/" + name + "/versions/?limit=" +
			strconv.Itoa(limit) + "&offset=" + strconv.Itoa(end)
		next = &v
	}
	if err := catalog.finish(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"meta":  map[string]int{"count": len(rels)},
		"links": map[string]any{"next": next, "previous": nil}, "data": data,
	})
}

func (h handler) version(w http.ResponseWriter, r *http.Request, ns, name, version string) {
	if !h.checkIdentity(w, r, ns, name) {
		return
	}
	if !ansiblefmt.ValidVersion(version) {
		h.fail(w, r, apierror.New(http.StatusBadRequest, "invalid collection version"))
		return
	}
	rel, err := h.find(r, ns, name, version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := rel.finish(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, versionJSON(r, rel.Meta))
}

func (h handler) find(r *http.Request, ns, name, version string) (release, error) {
	catalog, err := h.releases(r, ns, name)
	rels := catalog.items
	if err != nil {
		return release{}, err
	}
	for _, rel := range rels {
		if rel.Meta.Version == version {
			rel.finish = catalog.finish
			return rel, nil
		}
	}
	if err := catalog.finish(r.Context()); err != nil {
		return release{}, err
	}
	return release{}, missing()
}

func base(r *http.Request) string {
	return strings.TrimRight(baseurl.From(r.Context()), "/") +
		"/artifactory/api/ansible/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key)
}

func versionJSON(r *http.Request, m ansiblefmt.Metadata) map[string]any {
	artifact := base(r) + "/api/v3/plugin/ansible/content/published/collections/artifacts/" +
		url.PathEscape(m.Namespace+"-"+m.Name+"-"+m.Version+".tar.gz")
	href := "/artifactory/api/ansible/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) +
		"/api/v3/collections/" + m.Namespace + "/" + m.Name + "/versions/" + m.Version + "/"
	deps := m.Dependencies
	if deps == nil {
		deps = map[string]string{}
	}
	return map[string]any{
		"namespace":  map[string]string{"name": m.Namespace},
		"collection": map[string]string{"name": m.Name},
		"version":    m.Version, "href": href, "download_url": artifact,
		"artifact":   map[string]string{"sha256": m.SHA256},
		"metadata":   map[string]any{"dependencies": deps, "description": m.Description},
		"signatures": []any{},
	}
}

func parseArtifactName(v string) (ns, name, version string, ok bool) {
	if !strings.HasSuffix(v, ".tar.gz") {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(v, ".tar.gz"), "-")
	if len(parts) < 3 || !validIdentity(parts[0], parts[1]) {
		return "", "", "", false
	}
	version = strings.Join(parts[2:], "-")
	return parts[0], parts[1], version, ansiblefmt.ValidVersion(version)
}

func (h handler) artifact(w http.ResponseWriter, r *http.Request, file string) {
	ns, name, version, ok := parseArtifactName(file)
	if !ok {
		h.fail(w, r, apierror.New(http.StatusBadRequest, "invalid collection artifact"))
		return
	}
	path := ansiblefmt.Path(ns, name, version)
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	rel, err := h.find(r, ns, name, version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if pkgroute.RequestFrom(rel.Req).Repo.Class == repo.Remote {
		h.remoteArtifact(w, r, rel)
		return
	}
	if err := rel.finish(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}
	h.k.ServeItem(w, rel.Req, path)
}

func (h handler) publish(w http.ResponseWriter, r *http.Request) {
	class := pkgroute.RequestFrom(r).Repo.Class
	if class != repo.Local && class != repo.Federated {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"collection publish requires a local or federated repository",
			),
		)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*ansiblefmt.MaxArchiveBytes+maxPageBytes)
	if !h.k.RequireFolder(w, r, "collections", types.ActionWrite) {
		return
	}
	f, err := h.k.ReadFormLimited(r, "file", ansiblefmt.MaxArchiveBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if expected := f.Fields.Get("sha256"); expected == "" ||
		!strings.EqualFold(expected, f.File.Digests.SHA256) {
		h.fail(w, r, apierror.New(http.StatusBadRequest, "collection checksum mismatch"))
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), f.File.Digests.SHA256)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	meta, readErr := ansiblefmt.ReadArchive(rc, f.File.Size)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		h.fail(w, r, apierror.New(http.StatusBadRequest, "invalid collection archive"))
		return
	}
	path := ansiblefmt.Path(meta.Namespace, meta.Name, meta.Version)
	if _, err := h.k.StoreNew(
		r,
		path,
		f.File,
		nil,
		types.Checksums{SHA256: meta.SHA256},
		func() error {
			return apierror.New(http.StatusConflict, "collection version already exists")
		},
	); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(
		w,
		r,
		http.StatusCreated,
		map[string]string{
			"task": base(r) + "/api/v3/imports/collections/" + f.File.Digests.SHA256 + "/",
		},
	)
}

func (h handler) importTask(w http.ResponseWriter, r *http.Request, task string) {
	if len(task) != 64 {
		h.fail(w, r, missing())
		return
	}
	items, _, err := h.k.DB().Items().FindByChecksum(r.Context(),
		types.Checksums{SHA256: task}, store.Page{Limit: store.MaxLimit})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for _, it := range items {
		if it.RepoKey == pkgroute.RequestFrom(r).Repo.Key && !it.Cached &&
			h.k.Can(r, it.FullPath(), types.ActionRead) {
			if _, _, _, ok := ansiblefmt.ParsePath(it.FullPath()); ok {
				writeJSON(w, r, http.StatusOK, map[string]any{
					"state": "completed", "finished_at": it.Updated.UTC().Format(time.RFC3339Nano),
					"messages": []any{},
				})
				return
			}
		}
	}
	h.fail(w, r, missing())
}

// remoteRelease is the small part of a Galaxy v3 version document needed by
// the client and source policy. Unknown fields are deliberately ignored.
type remoteRelease struct {
	finish    func(context.Context) error
	Version   string `json:"version"`
	Namespace struct {
		Name string `json:"name"`
	} `json:"namespace"`
	Collection struct {
		Name string `json:"name"`
	} `json:"collection"`
	DownloadURL string `json:"download_url"`
	Artifact    struct {
		SHA256 string `json:"sha256"`
	} `json:"artifact"`
	Metadata struct {
		Dependencies map[string]string `json:"dependencies"`
		Description  string            `json:"description"`
	} `json:"metadata"`
}

func upstreamBase(c repo.Config) string {
	base := strings.TrimRight(c.URL, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	switch {
	case strings.HasSuffix(u.Path, "/api/v3"),
		strings.HasSuffix(u.Path, "/api/v3/plugin/ansible/content/published"):
		return base
	case u.Host == "galaxy.ansible.com" && (u.Path == "" || u.Path == "/api"):
		return strings.TrimSuffix(base, "/api") + "/api/v3/plugin/ansible/content/published"
	default:
		return base + "/api/v3"
	}
}

func (h handler) remoteJSON(
	r *http.Request,
	path, cachePath, permissionPath string,
	validate func([]byte) error,
) (source.Result, error) {
	base := upstreamBase(pkgroute.RequestFrom(r).Repo)
	if base == "" {
		return source.Result{}, source.ErrInvalidRequest
	}
	resource := source.Resource{
		Kind:                 source.ResourceKindMetadata,
		Path:                 cachePath,
		Method:               http.MethodGet,
		ExternalURL:          base + path,
		Variant:              "ansible-galaxy-v3",
		PermissionFolder:     true,
		PermissionPath:       permissionPath,
		ExpectedContentTypes: []string{"application/json"},
		ValidateMetadata: func(_ context.Context, reader io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size < 0 || size > maxPageBytes {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(reader, maxPageBytes+1))
			if err != nil || len(data) > maxPageBytes {
				return source.ErrInvalidResult
			}
			return validate(data)
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("ansible: remote metadata: %w", err)
	}
	if result.Stream == nil {
		return source.Result{}, source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, maxPageBytes+1))
	if err != nil || len(data) > maxPageBytes {
		return source.Result{}, source.ErrInvalidResult
	}
	if err := validate(data); err != nil {
		return source.Result{}, err
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return source.Result{}, fmt.Errorf("ansible: remote authority: %w", err)
		}
	}
	modified := result.Stream.Modified
	if modified.IsZero() {
		modified = time.Now().UTC()
	} // A missing upstream timestamp must never pin the client's version cache forever.
	result.Stream.Modified = modified
	return result, nil
}

func (h handler) remoteVersion(r *http.Request, ns, name, version string) (remoteRelease, error) {
	path := "/collections/" + ns + "/" + name + "/versions/" + version + "/"
	permissionPath := "collections/" + ns + "/" + name
	var release remoteRelease
	validate := func(data []byte) error {
		if json.Unmarshal(data, &release) != nil || release.Version != version ||
			release.Namespace.Name != ns || release.Collection.Name != name ||
			len(release.Artifact.SHA256) != 64 || release.DownloadURL == "" {
			return source.ErrInvalidResult
		}
		if _, err := hex.DecodeString(release.Artifact.SHA256); err != nil {
			return source.ErrInvalidResult
		}
		u, err := url.Parse(release.DownloadURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil ||
			u.Fragment != "" {
			return source.ErrInvalidResult
		}
		if !ansiblefmt.ValidDependencies(release.Metadata.Dependencies) {
			return source.ErrInvalidResult
		}
		return nil
	}
	result, err := h.remoteJSON(
		r,
		path,
		permissionPath+"/versions/"+version,
		permissionPath,
		validate,
	)
	if err != nil {
		return remoteRelease{}, err
	}
	release.finish = func(ctx context.Context) error {
		if err := result.Revalidate(ctx); err != nil {
			return fmt.Errorf("ansible: version source authority: %w", err)
		}
		if err := result.CheckRead(ctx, ansiblefmt.Path(ns, name, version)); err != nil {
			return fmt.Errorf("ansible: version read authority: %w", err)
		}
		return nil
	}
	if err := release.finish(r.Context()); err != nil {
		return remoteRelease{}, err
	}
	return release, nil
}

// Remote pagination is bounded and every continuation must remain on the same
// collection endpoint. A malicious next URL cannot broaden the source request.
func (h handler) remoteVersions(r *http.Request, ns, name string) (catalog, error) {
	root := "/collections/" + ns + "/" + name + "/versions/"
	next := root + "?limit=100&offset=0"
	permissionPath := "collections/" + ns + "/" + name
	seen := map[string]bool{}
	seenVersion := map[string]bool{}
	var out catalog
	for page := 0; next != ""; page++ {
		if page >= 100 || seen[next] {
			return catalog{}, source.ErrInvalidResult
		}
		seen[next] = true
		var doc struct {
			Data []struct {
				Version string `json:"version"`
			} `json:"data"`
			Links struct {
				Next *string `json:"next"`
			} `json:"links"`
		}
		validate := func(data []byte) error {
			if json.Unmarshal(data, &doc) != nil || doc.Data == nil || len(doc.Data) > 100 {
				return source.ErrInvalidResult
			}
			for _, v := range doc.Data {
				if !ansiblefmt.ValidVersion(v.Version) {
					return source.ErrInvalidResult
				}
			}
			return nil
		}
		result, err := h.remoteJSON(
			r,
			next,
			permissionPath+"/versions/list/"+strconv.Itoa(page),
			permissionPath,
			validate,
		)
		if err != nil {
			return catalog{}, err
		}
		out.checks = append(out.checks, result.Revalidate)
		for _, v := range doc.Data {
			if seenVersion[v.Version] {
				return catalog{}, source.ErrInvalidResult
			}
			seenVersion[v.Version] = true
			if !h.k.Can(r, ansiblefmt.Path(ns, name, v.Version), types.ActionRead) {
				continue
			}
			if err := result.CheckRead(
				r.Context(),
				ansiblefmt.Path(ns, name, v.Version),
			); err != nil {
				return catalog{}, fmt.Errorf("ansible: listed version authority: %w", err)
			}
			info, err := h.remoteVersion(r, ns, name, v.Version)
			if err != nil {
				return catalog{}, err
			}
			out.checks = append(out.checks, info.finish)
			out.items = append(out.items, release{Meta: ansiblefmt.Metadata{
				Namespace: ns, Name: name, Version: v.Version,
				SHA256: info.Artifact.SHA256, Dependencies: info.Metadata.Dependencies,
				Description: info.Metadata.Description,
			}, Req: r, At: result.Stream.Modified})
		}
		next = ""
		if doc.Links.Next != nil && *doc.Links.Next != "" {
			base, err := url.Parse(upstreamBase(pkgroute.RequestFrom(r).Repo) + root)
			if err != nil {
				return catalog{}, source.ErrInvalidResult
			}
			ref, err := url.Parse(*doc.Links.Next)
			if err != nil {
				return catalog{}, source.ErrInvalidResult
			}
			target := base.ResolveReference(ref)
			if target.Scheme != base.Scheme || target.Host != base.Host || target.User != nil ||
				target.Fragment != "" ||
				target.Path != base.Path {
				return catalog{}, source.ErrInvalidResult
			}
			next = root + "?" + target.RawQuery
		}
	}
	return out, nil
}

func (h handler) remoteArtifact(w http.ResponseWriter, r *http.Request, rel release) {
	m := rel.Meta
	info, err := h.remoteVersion(rel.Req, m.Namespace, m.Name, m.Version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resource := source.Resource{
		Kind: source.ResourceKindArtifact, Path: ansiblefmt.Path(m.Namespace, m.Name, m.Version),
		Method: r.Method, ExternalURL: info.DownloadURL,
		Checksums: types.Checksums{SHA256: m.SHA256}, Variant: "ansible-collection-archive-v1",
		Validate: func(_ context.Context, reader io.ReadSeeker, size int64) error {
			got, err := ansiblefmt.ReadArchive(reader, size)
			if err != nil || got.Namespace != m.Namespace || got.Name != m.Name ||
				got.Version != m.Version ||
				!maps.Equal(got.Dependencies, m.Dependencies) {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(rel.Req, resource)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	artifactCheck := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if err := rel.finish(ctx); err != nil {
			return err
		}
		if err := info.finish(ctx); err != nil {
			return err
		}
		if artifactCheck != nil {
			return artifactCheck(ctx)
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}
