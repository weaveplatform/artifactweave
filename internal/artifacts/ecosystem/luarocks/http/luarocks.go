package luarockshttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Options configures the LuaRocks protocol. RemoteSources serves remote
// repositories and fetches through them; nil leaves the remote class
// without a source, so a remote repository answers 501.
type Options struct {
	RemoteSources *source.Service
}

// Protocol returns the LuaRocks protocol for the router kit, under
// /artifactory/api/luarocks/{repoKey}: GET and HEAD of a path (a manifest
// file, a rockspec or rock at the root or, in a local or federated
// repository, any stored item), and the upload client's routes: GET of
// api/tool_version and api/1/{key}/check_rockspec, and POST of
// api/1/{key}/upload (a rockspec) and api/1/{key}/upload_rock/{versionID}
// (its rock). Local and federated repositories are served from their
// items, virtual ones from their members in order, with uploads sent to
// the default deployment repository, and remote ones through
// o.RemoteSources when it is set; a class without a source answers 501.
// The virtual class's source supports no single-item lookup, because the
// protocol resolves a virtual repository's members itself.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	// Virtual requests resolve through concrete members in virtual.go. The
	// marker source admits the class at the shared router gate.
	sources[repo.Virtual] = virtualSource{}
	return pkgroute.Protocol{
		Name:         "luarocks",
		Types:        []repo.PackageType{repo.LuaRocks},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/api/tool_version", h.toolVersion)
			r.Get("/api/1/{key}/check_rockspec", h.checkRockspec)
			r.Post("/api/1/{key}/upload", h.uploadRockspec)
			r.Post("/api/1/{key}/upload_rock/{versionID}", h.uploadRock)
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type handler struct{ k *pkgroute.Kit }

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		h.remoteRead(w, r, p.String())
		return
	}
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		h.virtualRead(w, r, p.String())
		return
	}
	if p.String() == luarocks.ManifestPath || strings.HasPrefix(p.String(), "manifest-5.") {
		if !h.k.RequireFolder(w, r, "", api.ActionRead) {
			return
		}
		f, err := h.k.ResolveItem(r, p.String())
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if f.Item.Folder {
			h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
			return
		}
		h.k.ServeFound(w, r, f)
		return
	}
	h.k.ServeItem(w, r, p.String())
}

func (h handler) toolVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"version": "1.0.0", "force_update": false})
}

func (h handler) checkRockspec(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		member, ok := h.virtualDeployment(w, r, api.ActionRead)
		if ok {
			h.checkRockspec(w, member)
		}
		return
	}
	name, version := r.URL.Query().Get("package"), r.URL.Query().Get("version")
	id, ok := luarocks.Parse(name + "-" + version + ".rockspec")
	if !ok || id.Name != name || id.Version != version {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LuaRocks package or version"))
		return
	}
	if !h.k.RequireFolder(w, r, "", api.ActionRead) {
		return
	}
	response := map[string]any{}
	p, err := h.k.DB().Packages().Find(r.Context(), pkgroute.RequestFrom(r).Repo.Key, name)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
	case err != nil:
		h.k.Error(w, r, err)
		return
	default:
		response["module"] = map[string]any{"name": name}
		v, err := h.k.DB().Packages().Version(r.Context(), p.ID, version)
		switch {
		case errors.Is(err, kernel.ErrNotFound):
		case err != nil:
			h.k.Error(w, r, err)
			return
		default:
			response["version"] = map[string]any{"id": v.ID}
		}
	}
	writeJSON(w, response)
}

func (h handler) uploadRockspec(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		member, ok := h.virtualDeployment(w, r, api.ActionWrite)
		if ok {
			h.uploadRockspec(w, member)
		}
		return
	}
	if !h.k.RequireFolder(w, r, "", api.ActionWrite) {
		return
	}
	form, err := h.k.ReadFormWith(r, "rockspec_file", "rockspec_sig")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	filename := path.Base(form.FileName)
	id, ok := luarocks.Parse(filename)
	if !ok || id.Arch != "rockspec" || filename != form.FileName {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LuaRocks rockspec filename"))
		return
	}
	if err := validateSignature(form.ExtraFiles["rockspec_sig"], filename); err != nil {
		h.k.Error(w, r, err)
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), form.File.Digests.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()
	if _, err := h.k.Store(r, filename, rc, nil, kernel.Checksums{}); err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err := h.signature(r, form.ExtraFiles["rockspec_sig"], filename); err != nil {
		h.k.Error(w, r, err)
		return
	}
	vID, err := luarocks.VersionID(r.Context(), h.k.DB(), pkgroute.RequestFrom(r).Repo, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"is_new":     true,
		"manifests":  []string{"root"},
		"module_url": "/artifactory/api/luarocks/" + responseRepoKey(r) + "/" + filename,
		"version":    map[string]any{"id": vID},
	})
}

func (h handler) uploadRock(w http.ResponseWriter, r *http.Request) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Virtual {
		member, ok := h.virtualDeployment(w, r, api.ActionWrite)
		if ok {
			h.uploadRock(w, member)
		}
		return
	}
	if !h.k.RequireFolder(w, r, "", api.ActionWrite) {
		return
	}
	n, err := strconv.ParseInt(chi.URLParam(r, "versionID"), 10, 64)
	if err != nil || n <= 0 {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LuaRocks version id"))
		return
	}
	id, err := luarocks.VersionFile(r.Context(), h.k.DB(), pkgroute.RequestFrom(r).Repo, n)
	if errors.Is(err, kernel.ErrNotFound) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "LuaRocks version not found"))
		return
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	form, err := h.k.ReadFormWith(r, "rock_file", "rock_sig")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	filename := path.Base(form.FileName)
	got, ok := luarocks.Parse(filename)
	if !ok || !strings.HasSuffix(filename, ".rock") || filename != form.FileName ||
		got.Name != id.Name ||
		got.Version != id.Version {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusBadRequest,
				"LuaRocks rock does not match the uploaded rockspec",
			),
		)
		return
	}
	if err := validateSignature(form.ExtraFiles["rock_sig"], filename); err != nil {
		h.k.Error(w, r, err)
		return
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), form.File.Digests.SHA256)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()
	if _, err := h.k.Store(r, filename, rc, nil, kernel.Checksums{}); err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err := h.signature(r, form.ExtraFiles["rock_sig"], filename); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}

func (h handler) signature(r *http.Request, sig pkgroute.FormFile, owner string) error {
	if sig.FileName == "" {
		return nil
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), sig.File.Digests.SHA256)
	if err != nil {
		return fmt.Errorf("luarocks: open signature: %w", err)
	}
	defer func() { _ = rc.Close() }()
	_, err = h.k.Store(r, sig.FileName, rc, nil, kernel.Checksums{})
	if err != nil {
		return fmt.Errorf("luarocks: store signature: %w", err)
	}
	return nil
}

func validateSignature(sig pkgroute.FormFile, owner string) error {
	if sig.FileName != "" && (sig.FileName != owner+".asc" || sig.File.Size > 1<<20) {
		return apierror.New(http.StatusBadRequest, "invalid LuaRocks signature sidecar")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// RedactAPIKey returns path as a log may record it: the upload API names
// the uploader's API key in its paths (api/1/{key}/...), and that key is a
// credential, so it is replaced by [redacted]. Any other path comes back
// unchanged.
func RedactAPIKey(path string) string {
	if rest, ok := strings.CutPrefix(path, "/artifactory/api/luarocks/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) >= 5 && parts[1] == "api" && parts[2] == "1" {
			parts[3] = "[redacted]"
			return "/artifactory/api/luarocks/" + strings.Join(parts, "/")
		}
	}
	return path
}
