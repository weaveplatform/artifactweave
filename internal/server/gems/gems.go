// Package gems serves RubyGems and Bundler's native repository protocol.
package gems

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	gemfmt "github.com/weaveplatform/artifactweave/internal/format/gems"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxPackages = 10000

type (
	Options       struct{ RemoteSources *source.Service }
	virtualSource struct{}
)

func (virtualSource) Item(
	context.Context,
	store.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name:     "gems",
		Types:    []repo.PackageType{repo.Gems},
		Surface:  apierror.SurfaceArtifactory,
		Sources:  sources,
		Reserved: []string{"{repoKey}/reindex", "{repoKey}/updateIndex"},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
			r.Post("/api/v1/gems", h.push)
			r.Post("/api/v1/gems/", h.push)
			r.Delete("/api/v1/yank", h.yank)
			r.Delete("/api/v1/gems/yank", h.yank)
		},
	}
}

type (
	handler struct{ k *pkgroute.Kit }
	entry   struct {
		gemfmt.Package
		request *http.Request
		at      time.Time
	}
)

func (h handler) fail(w http.ResponseWriter, r *http.Request, err error) { h.k.Error(w, r, err) }

func bad(
	msg string,
) error {
	return apierror.New(http.StatusBadRequest, msg)
}

func missing() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func (h handler) push(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote ||
		pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		h.fail(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"RubyGems push requires a local or federated repository",
			),
		)
		return
	}
	if !h.k.RequireFolder(w, r, "gems", types.ActionWrite) {
		return
	}
	info, err := h.k.Upload(r, io.LimitReader(r.Body, gemfmt.MaxGemBytes+1))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if info.Size > gemfmt.MaxGemBytes {
		h.fail(w, r, bad("gem upload exceeds size limit"))
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), info.Digests.SHA256)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := gemfmt.Read(r.Context(), rc, info.Size, "")
	_ = rc.Close()
	if err != nil {
		h.fail(w, r, bad("invalid RubyGem: "+err.Error()))
		return
	}
	path := "gems/" + gemfmt.Filename(p)
	_, err = h.k.StoreNew(
		r,
		path,
		info,
		nil,
		types.Checksums{},
		func() error { return apierror.New(http.StatusConflict, "gem version already exists") },
	)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "Successfully registered gem: %s (%s)\n", p.Name, p.Version)
}

func (h handler) yank(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Local &&
		pkgroute.RequestFrom(r).Repo.Class != repo.Federated {
		h.fail(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"yank requires a local or federated repository",
			),
		)
		return
	}
	if !h.k.RequireFolder(w, r, "gems", types.ActionDelete) {
		return
	}
	input, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(input) > 4096 {
		h.fail(w, r, bad("invalid yank form"))
		return
	}
	form, err := url.ParseQuery(string(input))
	if err != nil {
		h.fail(w, r, bad("invalid yank form"))
		return
	}
	name, version, platform := form.Get("gem_name"), form.Get("version"), form.Get("platform")
	if name == "" {
		name = r.URL.Query().Get("gem_name")
	}
	if version == "" {
		version = r.URL.Query().Get("version")
	}
	if platform == "" {
		platform = r.URL.Query().Get("platform")
	}
	if platform == "" {
		platform = "ruby"
	}
	p := gemfmt.Package{Name: name, Version: version, Platform: platform}
	path := "gems/" + gemfmt.Filename(p)
	if !gemfmt.ValidPath(path) {
		h.fail(w, r, bad("invalid gem identity"))
		return
	}
	if err := h.k.Delete(r, path); err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	//nolint:gosec // Identity is validated and this is a text/plain response.
	_, _ = fmt.Fprintf(
		w,
		"Successfully deleted gem: %s (%s)\n",
		name,
		version,
	) //nolint:gosec // Name and version are validated, and the response is text/plain.
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "/")
	if path == "" {
		h.fail(w, r, missing())
		return
	}
	if r.URL.RawQuery != "" && !strings.HasPrefix(path, "api/v1/search") {
		h.fail(w, r, bad("unsupported RubyGems query"))
		return
	}
	if strings.HasPrefix(path, "gems/") {
		if !gemfmt.ValidPath(path) {
			h.fail(w, r, missing())
			return
		}
		h.artifact(w, r, path)
		return
	}
	if !metadataPath(path) {
		h.fail(w, r, missing())
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	var entries []entry
	var err error
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		entries, err = h.remoteEntries(r)
	} else {
		entries, err = h.collect(r)
	}
	if err != nil {
		h.fail(w, r, pkgroute.SourceError(err, false))
		return
	}
	if path == "api/v1/search.json" {
		q := strings.TrimSpace(r.URL.Query().Get("query"))
		if q == "" || len(q) > 128 {
			h.fail(w, r, bad("invalid gem search query"))
			return
		}
		var found []map[string]any
		for _, row := range entries {
			if strings.Contains(strings.ToLower(row.Name), strings.ToLower(q)) {
				found = append(
					found,
					map[string]any{
						"name":     row.Name,
						"version":  row.Version,
						"platform": row.Platform,
						"sha":      row.SHA256,
						"info":     row.Description,
						"authors":  strings.Join(row.Authors, ", "),
					},
				)
			}
		}
		if found == nil {
			found = []map[string]any{}
		}
		body, _ := json.Marshal(found)
		serveMetadata(w, r, path, "application/json", body)
		return
	}
	body, media, err := render(path, entries)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	serveMetadata(w, r, path, media, body)
}

func metadataPath(path string) bool {
	if path == "names" || path == "versions" || path == "specs.4.8.gz" ||
		path == "latest_specs.4.8.gz" ||
		path == "prerelease_specs.4.8.gz" {
		return true
	}
	if strings.HasPrefix(path, "info/") && strings.Count(path, "/") == 1 {
		return true
	}
	if strings.HasPrefix(path, "quick/Marshal.4.8/") && strings.Count(path, "/") == 2 &&
		strings.HasSuffix(path, ".gemspec.rz") {
		return true
	}
	return strings.HasPrefix(path, "api/v1/gems/") && strings.HasSuffix(path, ".json") ||
		strings.HasPrefix(path, "api/v1/versions/") ||
		path == "api/v1/search.json"
}

func serveMetadata(w http.ResponseWriter, r *http.Request, path, media string, b []byte) {
	sum := sha256.Sum256(b)
	etag := gemfmt.InfoMD5(b)
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", "\""+etag+"\"")
	w.Header().Set("Repr-Digest", "sha-256=\""+base64.StdEncoding.EncodeToString(sum[:])+"\"")
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(b))
}

func render(path string, rows []entry) ([]byte, string, error) {
	packages := make([]gemfmt.Package, 0, len(rows))
	for _, row := range rows {
		packages = append(packages, row.Package)
	}
	switch path {
	case "names":
		return gemfmt.CompactNames(packages), "text/plain; charset=utf-8", nil
	case "versions":
		return gemfmt.CompactVersions(packages), "text/plain; charset=utf-8", nil
	case "specs.4.8.gz", "latest_specs.4.8.gz", "prerelease_specs.4.8.gz":
		b, err := gemfmt.Specs(
			packages,
			path == "latest_specs.4.8.gz",
			path == "prerelease_specs.4.8.gz",
		)
		if err != nil {
			return nil, "", fmt.Errorf("gems: render specs: %w", err)
		}
		return b, "application/octet-stream", nil
	}
	if name, ok := strings.CutPrefix(path, "info/"); ok {
		if !gemfmt.ValidName(name) {
			return nil, "", missing()
		}
		return gemfmt.CompactInfo(name, packages), "text/plain; charset=utf-8", nil
	}
	if file, ok := strings.CutPrefix(path, "quick/Marshal.4.8/"); ok {
		file = strings.TrimSuffix(file, ".gemspec.rz")
		for _, row := range rows {
			if strings.TrimSuffix(gemfmt.Filename(row.Package), ".gem") == file {
				b, err := gemfmt.QuickSpec(row.Package, row.at)
				if err != nil {
					return nil, "", fmt.Errorf("gems: render gemspec: %w", err)
				}
				return b, "application/octet-stream", nil
			}
		}
		return nil, "", missing()
	}
	if name, ok := strings.CutPrefix(path, "api/v1/gems/"); ok {
		name = strings.TrimSuffix(name, ".json")
		if !gemfmt.ValidName(name) {
			return nil, "", missing()
		}
		var latest *gemfmt.Package
		for i := range packages {
			if packages[i].Name == name &&
				(latest == nil || gemfmt.CompareVersions(packages[i].Version, latest.Version) > 0) {
				latest = &packages[i]
			}
		}
		if latest == nil {
			return nil, "", missing()
		}
		b, _ := json.Marshal(
			map[string]any{
				"name":     latest.Name,
				"version":  latest.Version,
				"platform": latest.Platform,
				"sha":      latest.SHA256,
				"info":     latest.Description,
				"authors":  strings.Join(latest.Authors, ", "),
			},
		)
		return b, "application/json", nil
	}
	if name, ok := strings.CutPrefix(path, "api/v1/versions/"); ok {
		name = strings.TrimSuffix(name, ".json")
		if !gemfmt.ValidName(name) {
			return nil, "", missing()
		}
		var list []map[string]any
		for _, p := range packages {
			if p.Name == name {
				list = append(
					list,
					map[string]any{
						"number":     p.Version,
						"platform":   p.Platform,
						"sha":        p.SHA256,
						"prerelease": false,
					},
				)
			}
		}
		if len(list) == 0 {
			return nil, "", missing()
		}
		b, _ := json.Marshal(list)
		return b, "application/json", nil
	}
	return nil, "", missing()
}

func (h handler) local(r *http.Request) ([]entry, error) {
	c := pkgroute.RequestFrom(r).Repo
	items, err := gemfmt.Files(r.Context(), h.k.DB(), c)
	if err != nil {
		return nil, fmt.Errorf("gems: list local items: %w", err)
	}
	if len(items) > maxPackages {
		return nil, source.ErrInvalidResult
	}
	var out []entry
	for _, it := range items {
		if !gemfmt.ValidPath(it.FullPath()) || !h.k.Can(r, it.FullPath(), types.ActionRead) {
			continue
		}
		rc, info, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("gems: open local archive: %w", err)
		}
		p, err := gemfmt.Read(r.Context(), rc, info.Size, it.FullPath())
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("stored gem %s: %w", it.FullPath(), err)
		}
		out = append(out, entry{Package: p, request: r, at: it.Created})
	}
	return out, nil
}

func (h handler) members(r *http.Request) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{c.Key: true}
	var out []*http.Request
	for _, key := range c.Repositories {
		if seen[key] {
			return nil, bad("virtual RubyGems member cycle")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("gems: get virtual member: %w", err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("gems: authorize virtual member: %w", err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) collect(r *http.Request) ([]entry, error) {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Virtual {
		return h.local(r)
	}
	members, err := h.members(r)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	var out []entry
	for _, mr := range members {
		var rows []entry
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			rows, err = h.remoteEntries(mr)
		} else {
			rows, err = h.local(mr)
		}
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if !h.k.Can(r, row.Path, types.ActionRead) ||
				!h.k.Can(mr, row.Path, types.ActionRead) ||
				selected[row.Path] {
				continue
			}
			selected[row.Path] = true
			out = append(out, row)
		}
	}
	return out, nil
}

func (h handler) artifact(w http.ResponseWriter, r *http.Request, path string) {
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	q := pkgroute.RequestFrom(r)
	if q.Repo.Class == repo.Virtual {
		rows, err := h.collect(r)
		if err != nil {
			h.fail(w, r, pkgroute.SourceError(err, false))
			return
		}
		for _, row := range rows {
			if row.Path == path {
				h.artifact(w, row.request, path)
				return
			}
		}
		h.fail(w, r, missing())
		return
	}
	if q.Repo.Class == repo.Remote {
		h.remoteArtifact(w, r, path)
		return
	}
	h.k.ServeItem(w, r, path)
}

func (h handler) remoteEntries(r *http.Request) ([]entry, error) {
	if !h.k.Can(r, "", types.ActionRead) {
		return nil, source.ErrDenied
	}
	versions, err := h.remoteBytes(r, "versions")
	if err != nil {
		return nil, err
	}
	names, err := gemfmt.ParseCompactVersions(versions)
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	if len(names) > maxPackages {
		return nil, source.ErrInvalidResult
	}
	keys := make([]string, 0, len(names))
	for name := range names {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	var out []entry
	for _, name := range keys {
		body, err := h.remoteBytes(r, "info/"+name)
		if err != nil {
			return nil, err
		}
		if names[name].MD5 != gemfmt.InfoMD5(body) {
			return nil, source.ErrInvalidResult
		}
		rows, err := gemfmt.ParseCompactInfo(name, body)
		if err != nil {
			return nil, source.ErrInvalidResult
		}
		for _, p := range rows {
			if names[name].Versions[gemfmt.VersionToken(p)] &&
				h.k.Can(r, p.Path, types.ActionRead) {
				out = append(out, entry{Package: p, request: r})
			}
		}
	}
	return out, nil
}

func (h handler) remotePackage(r *http.Request, path string) (gemfmt.Package, error) {
	if !h.k.Can(r, "", types.ActionRead) {
		return gemfmt.Package{}, source.ErrDenied
	}
	versions, err := h.remoteBytes(r, "versions")
	if err != nil {
		return gemfmt.Package{}, err
	}
	names, err := gemfmt.ParseCompactVersions(versions)
	if err != nil {
		return gemfmt.Package{}, source.ErrInvalidResult
	}
	name := ""
	for candidate := range names {
		if strings.HasPrefix(path, "gems/"+candidate+"-") && len(candidate) > len(name) {
			name = candidate
		}
	}
	if name == "" {
		return gemfmt.Package{}, store.ErrNotFound
	}
	body, err := h.remoteBytes(r, "info/"+name)
	if err != nil {
		return gemfmt.Package{}, err
	}
	if names[name].MD5 != gemfmt.InfoMD5(body) {
		return gemfmt.Package{}, source.ErrInvalidResult
	}
	packages, err := gemfmt.ParseCompactInfo(name, body)
	if err != nil {
		return gemfmt.Package{}, source.ErrInvalidResult
	}
	for _, p := range packages {
		if p.Path == path && names[name].Versions[gemfmt.VersionToken(p)] &&
			h.k.Can(r, path, types.ActionRead) {
			return p, nil
		}
	}
	return gemfmt.Package{}, store.ErrNotFound
}

func (h handler) remoteBytes(r *http.Request, path string) ([]byte, error) {
	result, err := h.remoteResult(r, path)
	if err != nil {
		return nil, err
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return nil, fmt.Errorf("gems: revalidate remote metadata: %w", err)
		}
	}
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
	case result.Item != nil:
		body, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("gems: open cached metadata: %w", err)
		}
	default:
		return nil, source.ErrInvalidResult
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, gemfmt.MaxIndexBytes+1))
	if err != nil || len(b) > gemfmt.MaxIndexBytes {
		return nil, source.ErrInvalidResult
	}
	if result.Used != nil {
		_ = result.Used(r.Context())
	}
	return b, nil
}

func (h handler) remoteResult(r *http.Request, path string) (source.Result, error) {
	resource := source.Resource{
		Kind:             source.ResourceKindMetadata,
		Path:             path,
		Method:           http.MethodGet,
		PermissionFolder: true,
		Variant:          "gems-metadata-v1",
		ValidateMetadata: func(ctx context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > gemfmt.MaxIndexBytes {
				return source.ErrInvalidResult
			}
			b, err := io.ReadAll(io.LimitReader(body, gemfmt.MaxIndexBytes+1))
			if err != nil || int64(len(b)) != size {
				return source.ErrInvalidResult
			}
			switch {
			case path == "versions":
				_, err = gemfmt.ParseCompactVersions(b)
			case strings.HasPrefix(path, "info/"):
				_, err = gemfmt.ParseCompactInfo(strings.TrimPrefix(path, "info/"), b)
			default:
				err = source.ErrInvalidResult
			}
			if err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("gems: read remote metadata: %w", err)
	}
	return result, nil
}

func (h handler) remoteArtifact(w http.ResponseWriter, r *http.Request, path string) {
	// The compact index checksum binds the remote gem body to the metadata
	// used for resolution. A missing version never authorizes a cold fetch.
	p, err := h.remotePackage(r, path)
	if err != nil {
		h.fail(w, r, pkgroute.SourceError(err, false))
		return
	}
	expected := p.SHA256
	resource := pkgroute.Resource(r, path, "")
	resource.Variant = "gems-package-v1"
	resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		p, err := gemfmt.Read(ctx, body, size, path)
		if err != nil || p.SHA256 != expected {
			return source.ErrInvalidResult
		}
		return nil
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.fail(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}
