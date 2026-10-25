// Package bazelmodules serves the Bazel index registry protocol.
package bazelmodules

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	fmtbazel "github.com/weaveplatform/artifactweave/internal/format/bazelmodules"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const metadataLimit = 4 << 20

type virtualSource struct{}

func (virtualSource) Item(context.Context, store.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func Protocol(remote *source.Service) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: virtualSource{},
	}
	if remote != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: remote}
	}
	return pkgroute.Protocol{
		Name: "bazelmodules", Types: []repo.PackageType{repo.BazelModules},
		Surface: apierror.Artifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct{ k *pkgroute.Kit }
type requestPath struct{ name, version, kind, file string }

func parsePath(raw string) (requestPath, bool) {
	p := strings.Split(raw, "/")
	if raw == "bazel_registry.json" {
		return requestPath{kind: "registry"}, true
	}
	if len(p) == 3 && p[0] == "modules" && fmtbazel.ValidName(p[1]) && p[2] == "metadata.json" {
		return requestPath{name: p[1], kind: "metadata"}, true
	}
	if len(p) == 4 && p[0] == "modules" && fmtbazel.ValidName(p[1]) && fmtbazel.ValidVersion(p[2]) {
		switch p[3] {
		case "MODULE.bazel":
			return requestPath{name: p[1], version: p[2], kind: "module"}, true
		case "source.json":
			return requestPath{name: p[1], version: p[2], kind: "source"}, true
		}
	}
	if len(p) == 5 && p[0] == "modules" && fmtbazel.ValidName(p[1]) && fmtbazel.ValidVersion(p[2]) && p[3] == "patches" && safeBase(p[4]) {
		return requestPath{name: p[1], version: p[2], kind: "patch", file: p[4]}, true
	}
	if len(p) == 4 && p[0] == "archives" && fmtbazel.ValidName(p[1]) && fmtbazel.ValidVersion(p[2]) && safeBase(p[3]) {
		return requestPath{name: p[1], version: p[2], kind: "archive", file: p[3]}, true
	}
	return requestPath{}, false
}

func safeBase(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 255 && path.Base(s) == s && !strings.ContainsAny(s, "\\%\x00\r\n")
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Bazel Modules query"))
		return
	}
	clean, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	q, ok := parsePath(clean.String())
	if !ok {
		h.k.Error(w, r, missing())
		return
	}
	if generic.CheckDownload(pkgroute.RequestFrom(r).Repo, clean.String(), false) != nil {
		h.k.Error(w, r, missing())
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		h.virtualRead(w, r, q)
		return
	}
	h.memberRead(w, r, q)
}

func missing() error {
	return apierror.New(http.StatusNotFound, "Could not find Bazel module resource")
}
func invalid() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Bazel Modules response")
}

func moduleFolder(q requestPath) string { return "modules/" + q.name + "/" + q.version }
func modulePath(q requestPath) string   { return moduleFolder(q) + "/MODULE.bazel" }
func sourcePath(q requestPath) string   { return moduleFolder(q) + "/source.json" }

func (h handler) memberRead(w http.ResponseWriter, r *http.Request, q requestPath) {
	c := pkgroute.RequestFrom(r).Repo
	policyPath := routePath(q)
	if generic.CheckDownload(c, policyPath, false) != nil {
		h.k.Error(w, r, missing())
		return
	}
	if q.kind == "registry" {
		writeJSON(w, r, []byte("{}"))
		return
	}
	if c.Class == repo.Remote {
		h.remoteRead(w, r, q)
		return
	}
	switch q.kind {
	case "metadata":
		h.localMetadata(w, r, q)
	case "module":
		if !h.localComplete(r, q) {
			h.k.Error(w, r, missing())
			return
		}
		h.k.ServeItem(w, r, modulePath(q))
	case "source":
		h.localSource(w, r, q)
	case "archive":
		file, ok := h.localArchive(r, q)
		if !ok || file != q.file {
			h.k.Error(w, r, missing())
			return
		}
		h.k.ServeItem(w, r, moduleFolder(q)+"/"+file)
	default:
		h.k.Error(w, r, missing())
	}
}

func routePath(q requestPath) string {
	switch q.kind {
	case "registry":
		return "bazel_registry.json"
	case "metadata":
		return "modules/" + q.name + "/metadata.json"
	case "module":
		return modulePath(q)
	case "source":
		return sourcePath(q)
	case "archive":
		return "archives/" + q.name + "/" + q.version + "/" + q.file
	case "patch":
		return moduleFolder(q) + "/patches/" + q.file
	default:
		return ""
	}
}

func (h handler) localComplete(r *http.Request, q requestPath) bool {
	if !h.k.Can(r, modulePath(q), types.ActionRead) {
		return false
	}
	_, ok := h.localArchive(r, q)
	if !ok {
		return false
	}
	_, err := h.k.Item(r, modulePath(q))
	return err == nil
}

func (h handler) localArchive(r *http.Request, q requestPath) (string, bool) {
	if !h.k.CanFolder(r, moduleFolder(q), types.ActionRead) {
		return "", false
	}
	items, _, err := h.k.DB().Items().Children(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, moduleFolder(q), store.Page{Limit: store.MaxLimit})
	if err != nil {
		return "", false
	}
	archive := ""
	for _, it := range items {
		if it.Folder {
			continue
		}
		if !h.k.Can(r, it.FullPath(), types.ActionRead) {
			continue
		}
		f, ok := fmtbazel.ParseFile(it.FullPath())
		if ok && f.Kind == "archive" {
			if archive != "" {
				return "", false
			}
			archive = path.Base(it.FullPath())
		}
	}
	return archive, archive != ""
}

func (h handler) localMetadata(w http.ResponseWriter, r *http.Request, q requestPath) {
	if !h.k.RequireFolder(w, r, "modules/"+q.name, types.ActionRead) {
		return
	}
	items, _, err := h.k.DB().Items().Children(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, "modules/"+q.name, store.Page{Limit: store.MaxLimit})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.k.Error(w, r, err)
		return
	}
	versions := []string{}
	for _, it := range items {
		if !it.Folder || !fmtbazel.ValidVersion(it.Name) {
			continue
		}
		candidate := requestPath{name: q.name, version: it.Name}
		if h.localComplete(r, candidate) {
			versions = append(versions, it.Name)
		}
	}
	if len(versions) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	slices.Sort(versions)
	body, _ := json.Marshal(map[string]any{"versions": versions})
	writeJSON(w, r, body)
}

func (h handler) localSource(w http.ResponseWriter, r *http.Request, q requestPath) {
	if !h.localComplete(r, q) {
		h.k.Error(w, r, missing())
		return
	}
	file, _ := h.localArchive(r, q)
	p := moduleFolder(q) + "/" + file
	if !h.k.Require(w, r, p, types.ActionRead) {
		return
	}
	f, err := h.k.Item(r, p)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	rc, info, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	strip, err := fmtbazel.ArchiveStripPrefix(rc, info.Size, file)
	_ = rc.Close()
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	archiveType := strings.TrimPrefix(path.Ext(file), ".")
	if strings.HasSuffix(file, ".tar.gz") {
		archiveType = "tar.gz"
	}
	integrity, err := fmtbazel.Integrity(f.Item.Actual.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	base := baseurl.From(r.Context())
	if base == "" {
		h.k.Error(w, r, errors.New("missing public base URL"))
		return
	}
	uri := base + "/artifactory/api/bazelmodules/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) + "/archives/" + q.name + "/" + url.PathEscape(q.version) + "/" + url.PathEscape(file)
	body, _ := json.Marshal(map[string]any{"type": "archive", "url": uri, "integrity": integrity, "archive_type": archiveType, "strip_prefix": strip})
	writeJSON(w, r, body)
}

func writeJSON(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// A remote metadata result remains source-fenced until immediately before
// disclosure. The response is bounded and parsed before it is returned.
func (h handler) remoteBytes(r *http.Request, path string, validate func([]byte) error) ([]byte, source.Result, error) {
	resource := source.Resource{Kind: source.Metadata, Path: path, Method: http.MethodGet, Variant: "bazelmodules-metadata-v1", PermissionFolder: true, PermissionPath: parent(path), ValidateMetadata: func(_ context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
		if size < 0 || size > metadataLimit {
			return invalid()
		}
		data, err := io.ReadAll(io.LimitReader(body, metadataLimit+1))
		if err != nil || len(data) > metadataLimit {
			return invalid()
		}
		return validate(data)
	}}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, result, err
	}
	if result.Stream == nil {
		return nil, result, invalid()
	}
	defer result.Stream.Body.Close()
	data, err := io.ReadAll(io.LimitReader(result.Stream.Body, metadataLimit+1))
	if err != nil || len(data) > metadataLimit || validate(data) != nil {
		return nil, result, invalid()
	}
	result.Stream = nil
	return data, result, nil
}

func parent(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

func (h handler) remoteRead(w http.ResponseWriter, r *http.Request, q requestPath) {
	switch q.kind {
	case "metadata":
		h.remoteMetadata(w, r, q)
	case "module":
		h.remoteModule(w, r, q)
	case "source":
		h.remoteSource(w, r, q)
	case "archive":
		h.remoteArchive(w, r, q)
	case "patch":
		h.remotePatch(w, r, q)
	default:
		h.k.Error(w, r, missing())
	}
}

func (h handler) remoteModule(w http.ResponseWriter, r *http.Request, q requestPath) {
	data, results, err := h.remoteVersion(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err := revalidateAll(r, results...); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeContent(w, r, "text/plain; charset=utf-8", data)
	for _, result := range results {
		use(r, result)
	}
}

func (h handler) remoteVersion(r *http.Request, q requestPath) ([]byte, []source.Result, error) {
	metadataPath := "modules/" + q.name + "/metadata.json"
	meta, metaResult, err := h.remoteBytes(r, metadataPath, func(body []byte) error { _, e := parseMetadata(body); return e })
	if err != nil {
		return nil, nil, err
	}
	versions, _ := parseMetadata(meta)
	if !slices.Contains(versions, q.version) {
		return nil, nil, missing()
	}
	module, moduleResult, err := h.remoteBytes(r, modulePath(q), func(body []byte) error {
		name, version, e := fmtbazel.ParseModule(body)
		if e != nil || name != q.name || version != q.version {
			return invalid()
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return module, []source.Result{metaResult, moduleResult}, nil
}

func (h handler) remotePatch(w http.ResponseWriter, r *http.Request, q requestPath) {
	_, versionResults, err := h.remoteVersion(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	doc, metadata, err := h.upstreamSource(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	integrity, ok := doc.Patches[q.file]
	if !ok {
		h.k.Error(w, r, missing())
		return
	}
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, "sha256-"))
	if err != nil || len(digest) != 32 {
		h.k.Error(w, r, invalid())
		return
	}
	p := moduleFolder(q) + "/patches/" + q.file
	resource := pkgroute.Resource(r, p, p)
	resource.Checksums = types.Checksums{SHA256: hex.EncodeToString(digest)}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		for _, fence := range append(versionResults, metadata) {
			if fence.Revalidate != nil {
				if err := fence.Revalidate(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) remoteMetadata(w http.ResponseWriter, r *http.Request, q requestPath) {
	p := "modules/" + q.name + "/metadata.json"
	data, result, err := h.remoteBytes(r, p, func(body []byte) error { _, e := parseMetadata(body); return e })
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	versions, _ := parseMetadata(data)
	allowed := []string{}
	for _, v := range versions {
		if h.k.Can(r, "modules/"+q.name+"/"+v+"/MODULE.bazel", types.ActionRead) {
			allowed = append(allowed, v)
		}
	}
	if len(allowed) == 0 {
		h.k.Error(w, r, missing())
		return
	}
	body, _ := json.Marshal(map[string]any{"versions": allowed})
	if err := revalidate(r, result); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, r, body)
	use(r, result)
}

func parseMetadata(data []byte) ([]string, error) {
	var doc struct {
		Versions []string `json:"versions"`
	}
	if json.Unmarshal(data, &doc) != nil || doc.Versions == nil || len(doc.Versions) > 10000 {
		return nil, invalid()
	}
	seen := map[string]bool{}
	for _, v := range doc.Versions {
		if !fmtbazel.ValidVersion(v) || seen[v] {
			return nil, invalid()
		}
		seen[v] = true
	}
	return doc.Versions, nil
}

func revalidate(r *http.Request, result source.Result) error {
	if result.Revalidate != nil {
		return result.Revalidate(r.Context())
	}
	return nil
}
func use(r *http.Request, result source.Result) {
	if r.Method == http.MethodGet && result.Used != nil {
		_ = result.Used(r.Context())
	}
}

func (h handler) remoteSource(w http.ResponseWriter, r *http.Request, q requestPath) {
	_, versionResults, err := h.remoteVersion(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	doc, result, err := h.upstreamSource(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.k.Can(r, modulePath(q), types.ActionRead) {
		h.k.Error(w, r, apierror.New(403, "module read denied"))
		return
	}
	base := baseurl.From(r.Context())
	if base == "" {
		h.k.Error(w, r, errors.New("missing public base URL"))
		return
	}
	doc.URL = base + "/artifactory/api/bazelmodules/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) + "/archives/" + q.name + "/" + url.PathEscape(q.version) + "/" + doc.archiveFile()
	body, _ := json.Marshal(doc)
	if err := revalidateAll(r, append(versionResults, result)...); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, r, body)
	for _, fence := range versionResults {
		use(r, fence)
	}
	use(r, result)
}

func (h handler) remoteArchive(w http.ResponseWriter, r *http.Request, q requestPath) {
	_, versionResults, err := h.remoteVersion(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	doc, metadata, err := h.upstreamSource(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.file != doc.archiveFile() || !h.k.Can(r, modulePath(q), types.ActionRead) {
		h.k.Error(w, r, missing())
		return
	}
	resource := source.Resource{Kind: source.Artifact, Path: "archives/" + q.name + "/" + q.version + "/" + q.file, ExternalURL: doc.URL, Method: r.Method, Variant: "bazelmodules-archive-v1", Checksums: types.Checksums{SHA256: doc.sha256()}, CheckURL: sourceURLGuard(pkgroute.RequestFrom(r).Repo, doc.URL)}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		for _, fence := range versionResults {
			if fence.Revalidate != nil {
				if err := fence.Revalidate(ctx); err != nil {
					return err
				}
			}
		}
		if metadata.Revalidate != nil {
			return metadata.Revalidate(ctx)
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}

func revalidateAll(r *http.Request, results ...source.Result) error {
	for _, result := range results {
		if err := revalidate(r, result); err != nil {
			return err
		}
	}
	return nil
}

func writeContent(w http.ResponseWriter, r *http.Request, mime string, body []byte) {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

func (h handler) upstreamSource(r *http.Request, q requestPath) (sourceDocument, source.Result, error) {
	p := sourcePath(q)
	data, result, err := h.remoteBytes(r, p, func(body []byte) error { _, e := parseSource(body, pkgroute.RequestFrom(r).Repo); return e })
	if err != nil {
		return sourceDocument{}, result, err
	}
	doc, err := parseSource(data, pkgroute.RequestFrom(r).Repo)
	return doc, result, err
}

func (h handler) virtualMembers(r *http.Request) ([]*http.Request, error) {
	v := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{v.Key: true}
	members := []*http.Request{}
	for _, key := range v.Repositories {
		if seen[key] {
			return nil, apierror.New(400, "invalid virtual Bazel Modules member")
		}
		seen[key] = true
		c, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if c.Class != repo.Local && c.Class != repo.Remote && c.Class != repo.Federated {
			return nil, apierror.New(400, "invalid virtual Bazel Modules member class")
		}
		mr, err := h.k.MemberRequest(r, c)
		if err != nil {
			return nil, err
		}
		members = append(members, mr)
	}
	return members, nil
}

func (h handler) virtualRead(w http.ResponseWriter, r *http.Request, q requestPath) {
	if q.kind == "registry" {
		writeJSON(w, r, []byte("{}"))
		return
	}
	if !h.k.RequireFolder(w, r, "modules/"+q.name, types.ActionRead) {
		return
	}
	members, err := h.virtualMembers(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.kind == "metadata" {
		all := map[string]bool{}
		for _, mr := range members {
			if !h.k.CanFolder(mr, "modules/"+q.name, types.ActionRead) {
				continue
			}
			if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
				data, result, e := h.remoteBytes(mr, "modules/"+q.name+"/metadata.json", func(b []byte) error { _, e := parseMetadata(b); return e })
				if isMissing(e) {
					continue
				}
				if e != nil {
					h.k.Error(w, r, e)
					return
				}
				versions, _ := parseMetadata(data)
				if e = revalidate(mr, result); e != nil {
					h.k.Error(w, r, e)
					return
				}
				for _, v := range versions {
					if h.k.Can(mr, "modules/"+q.name+"/"+v+"/MODULE.bazel", types.ActionRead) {
						all[v] = true
					}
				}
			} else {
				items, _, e := h.k.DB().Items().Children(mr.Context(), pkgroute.RequestFrom(mr).Repo.Key, false, "modules/"+q.name, store.Page{Limit: store.MaxLimit})
				if e != nil && !errors.Is(e, store.ErrNotFound) {
					h.k.Error(w, r, e)
					return
				}
				for _, it := range items {
					if it.Folder && fmtbazel.ValidVersion(it.Name) && h.localComplete(mr, requestPath{name: q.name, version: it.Name}) {
						all[it.Name] = true
					}
				}
			}
		}
		versions := []string{}
		for v := range all {
			versions = append(versions, v)
		}
		slices.Sort(versions)
		if len(versions) == 0 {
			h.k.Error(w, r, missing())
			return
		}
		body, _ := json.Marshal(map[string]any{"versions": versions})
		writeJSON(w, r, body)
		return
	}
	// Select a single member for every representation of a version. A member
	// that has MODULE.bazel but lacks a valid source cannot shadow a later one.
	for _, mr := range members {
		if !h.k.Can(mr, modulePath(q), types.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			_, _, e := h.upstreamSource(mr, q)
			if isMissing(e) {
				continue
			}
			if e != nil {
				h.k.Error(w, r, e)
				return
			}
		} else if !h.localComplete(mr, q) {
			continue
		}
		// URL rewriting must retain the virtual key even while content comes
		// from this concrete member.
		if q.kind == "source" {
			h.virtualSource(w, r, mr, q)
			return
		}
		h.memberRead(w, mr, q)
		return
	}
	h.k.Error(w, r, missing())
}

func isMissing(err error) bool {
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	var api *apierror.Error
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

func (h handler) virtualSource(w http.ResponseWriter, r, mr *http.Request, q requestPath) {
	buf := &capture{header: http.Header{}}
	h.memberRead(buf, mr, q)
	if buf.status != 200 {
		h.k.Error(w, r, missing())
		return
	}
	var doc sourceDocument
	if json.Unmarshal(buf.body.Bytes(), &doc) != nil {
		h.k.Error(w, r, invalid())
		return
	}
	archiveName := path.Base(doc.URL)
	if !safeBase(archiveName) {
		h.k.Error(w, r, invalid())
		return
	}
	doc.URL = baseurl.From(r.Context()) + "/artifactory/api/bazelmodules/" + url.PathEscape(pkgroute.RequestFrom(r).Repo.Key) + "/archives/" + q.name + "/" + url.PathEscape(q.version) + "/" + archiveName
	body, _ := json.Marshal(doc)
	writeJSON(w, r, body)
}

type capture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *capture) Header() http.Header    { return c.header }
func (c *capture) WriteHeader(status int) { c.status = status }
func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	return c.body.Write(b)
}
