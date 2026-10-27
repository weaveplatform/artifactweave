package conan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	conanfmt "github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	maxManifest = 1 << 20
	maxUpload   = 2 << 30
)

type revision struct {
	Revision string    `json:"revision"`
	Time     time.Time `json:"time"`
}

func (h handler) recipeRevisions(w http.ResponseWriter, r *http.Request, ref conanfmt.Reference) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	list, err := h.revisions(r, ref, "")
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"revisions": list})
}

func (h handler) recipeLatest(w http.ResponseWriter, r *http.Request, ref conanfmt.Reference) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	list, err := h.revisions(r, ref, "")
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, list[0])
}

func (h handler) packageRevisions(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
	id string,
) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	list, err := h.revisions(r, ref, id)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"revisions": list})
}

func (h handler) packageLatest(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
	id string,
) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	list, err := h.revisions(r, ref, id)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, list[0])
}

func (h handler) revisions(r *http.Request, ref conanfmt.Reference, id string) ([]revision, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		return h.remoteRevisions(r, ref, id)
	}
	if c.Class == repo.Virtual {
		return h.virtualRevisions(r, ref, id)
	}
	folder := strings.Join([]string{ref.User, ref.Name, ref.Version, ref.Channel}, "/")
	if id != "" {
		folder = ref.Root() + "/package/" + id
	}
	if !h.k.CanFolder(r, folder, types.ActionRead) {
		return nil, denied(r)
	}
	files, err := conanfmt.Files(r.Context(), h.k.DB(), c, folder)
	if err != nil {
		return nil, fmt.Errorf("list conan revisions: %w", err)
	}
	var list []revision
	for _, it := range files {
		if it.Folder || !h.k.Can(r, it.FullPath(), types.ActionRead) {
			continue
		}
		f, ok := conanfmt.ParseStored(it.FullPath())
		if !ok || f.Ref.Name != ref.Name || f.Ref.Version != ref.Version ||
			f.Ref.User != ref.User || f.Ref.Channel != ref.Channel ||
			f.File != conanfmt.Manifest || f.Package != (id != "") ||
			id != "" && (f.Ref.Revision != ref.Revision || f.ID != id) {
			continue
		}
		value := f.Ref.Revision
		if id != "" {
			value = f.PRev
		}
		list = append(list, revision{Revision: value, Time: it.Modified.UTC()})
	}
	if len(list) == 0 {
		return nil, apierror.New(http.StatusNotFound, "Conan revision not found")
	}
	slices.SortFunc(list, func(a, b revision) int {
		if n := b.Time.Compare(a.Time); n != 0 {
			return n
		}
		return strings.Compare(b.Revision, a.Revision)
	})
	return list, nil
}

func (h handler) fileList(w http.ResponseWriter, r *http.Request, root string) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	if !h.k.RequireFolder(w, r, root, types.ActionRead) {
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		h.remoteFileList(w, r, root)
		return
	}
	if c.Class == repo.Virtual {
		h.virtualFileList(w, r, root)
		return
	}
	if err := h.requireComplete(r, root); err != nil {
		h.error(w, r, err)
		return
	}
	items, err := conanfmt.Files(r.Context(), h.k.DB(), c, root)
	if err != nil {
		h.error(w, r, err)
		return
	}
	files := map[string]any{}
	for _, it := range items {
		if it.Folder {
			continue
		}
		if !h.k.Can(r, it.FullPath(), types.ActionRead) {
			h.error(w, r, denied(r))
			return
		}
		name, ok := strings.CutPrefix(it.FullPath(), root+"/")
		if !ok || !conanfmt.SafeFile(name) {
			continue
		}
		files[name] = map[string]any{}
	}
	writeJSON(w, map[string]any{"files": files})
}

func (h handler) file(w http.ResponseWriter, r *http.Request, f conanfmt.FilePath) {
	if !conanfmt.SafeFile(f.File) {
		h.bad(w, r, "invalid Conan filename")
		return
	}
	if r.URL.RawQuery != "" {
		h.bad(w, r, "unexpected Conan file query")
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !h.k.Require(w, r, f.Path(), types.ActionRead) {
			return
		}
		if c.Class == repo.Remote {
			h.remoteFile(w, r, f)
			return
		}
		if c.Class == repo.Virtual {
			h.virtualFile(w, r, f)
			return
		}
		if err := h.requireComplete(r, f.Root()); err != nil {
			h.error(w, r, err)
			return
		}
		h.k.ServeItem(w, r, f.Path())
	case http.MethodPut:
		if c.Class == repo.Virtual {
			h.virtualUpload(w, r, f)
			return
		}
		if c.Class == repo.Remote {
			h.method(w, r)
			return
		}
		h.upload(w, r, f)
	default:
		h.method(w, r)
	}
}

func (h handler) requireComplete(r *http.Request, root string) error {
	// A package is visible only after its own and its recipe's manifests.
	if strings.Contains(root, "/package/") {
		recipe, _, _ := strings.Cut(root, "/package/")
		if err := h.manifest(r, recipe+"/export/"+conanfmt.Manifest); err != nil {
			return err
		}
	}
	return h.manifest(r, root+"/"+conanfmt.Manifest)
}

func (h handler) manifest(r *http.Request, path string) error {
	if !h.k.Can(r, path, types.ActionRead) {
		return denied(r)
	}
	found, err := h.k.Item(r, path)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && found.Item.Folder:
		return apierror.New(http.StatusNotFound, "Conan revision not found")
	case err != nil:
		return fmt.Errorf("read conan manifest: %w", err)
	default:
		return nil
	}
}

func (h handler) upload(w http.ResponseWriter, r *http.Request, f conanfmt.FilePath) {
	if f.Package {
		if err := h.manifest(r, f.Ref.RecipeRoot()+"/"+conanfmt.Manifest); err != nil {
			h.error(w, r, err)
			return
		}
	}
	if !h.k.Require(w, r, f.Path(), types.ActionWrite) {
		return
	}
	r = r.WithContext(conanfmt.Native(r.Context()))
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxUpload)
	if f.File == conanfmt.Manifest {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManifest))
		if err != nil {
			h.bad(w, r, "Conan manifest exceeds limit")
			return
		}
		if err := h.validateManifest(r, f, data); err != nil {
			h.error(w, r, err)
			return
		}
		body = bytes.NewReader(data)
	}
	declared := pkgroute.DeclaredChecksums(r.Header)
	var it types.Item
	var err error
	if strings.HasPrefix(f.File, "metadata/") {
		it, err = h.k.Store(r, f.Path(), body, nil, declared)
	} else {
		it, err = h.k.StoreOnce(r, f.Path(), body, nil, declared, func(types.Item) error {
			return apierror.New(
				http.StatusConflict,
				"Conan revision file already has different content",
			)
		})
	}
	if err != nil {
		h.error(w, r, err)
		return
	}
	pkgroute.SetChecksums(w.Header(), it.Actual)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
}

func (h handler) validateManifest(r *http.Request, f conanfmt.FilePath, data []byte) error {
	entries, err := parseManifest(data)
	if err != nil {
		return err
	}
	archiveName := "conan_export.tgz"
	if f.Package {
		archiveName = "conan_package.tgz"
	}
	var archived map[string]string
	for name, sum := range entries {
		it, err := h.k.DB().
			Items().
			Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, f.Root()+"/"+name)
		if err == nil && !it.Folder && strings.EqualFold(it.Actual.MD5, sum) {
			continue
		}
		if archived == nil {
			archive, archiveErr := h.k.DB().
				Items().
				Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, f.Root()+"/"+archiveName)
			if archiveErr != nil || archive.Folder {
				return apierror.New(
					http.StatusUnprocessableEntity,
					"Conan manifest references missing content",
				)
			}
			rc, _, openErr := h.k.Blobs().Open(r.Context(), archive.Actual.SHA256)
			if openErr != nil {
				return fmt.Errorf("open conan archive: %w", openErr)
			}
			archived, openErr = archiveDigests(rc)
			_ = rc.Close()
			if openErr != nil {
				return openErr
			}
		}
		if !strings.EqualFold(archived[name], sum) {
			return apierror.New(
				http.StatusUnprocessableEntity,
				"Conan manifest references missing or changed content",
			)
		}
	}
	for name, sum := range archived {
		if entries[name] != sum {
			return apierror.New(
				http.StatusUnprocessableEntity,
				"Conan archive has unlisted or changed content",
			)
		}
	}
	needed := "conanfile.py"
	if f.Package {
		needed = "conaninfo.txt"
	}
	if _, ok := entries[needed]; !ok {
		return apierror.New(http.StatusBadRequest, "Conan manifest lacks required file")
	}
	if f.Package && archived == nil {
		archive, err := h.k.DB().
			Items().
			Get(r.Context(), pkgroute.RequestFrom(r).Repo.Key, false, f.Root()+"/conan_package.tgz")
		if err != nil || archive.Folder {
			return apierror.New(http.StatusUnprocessableEntity, "Conan package archive is missing")
		}
		rc, _, err := h.k.Blobs().Open(r.Context(), archive.Actual.SHA256)
		if err != nil {
			return fmt.Errorf("open conan archive: %w", err)
		}
		archived, err = archiveDigests(rc)
		_ = rc.Close()
		if err != nil {
			return err
		}
		for name, sum := range archived {
			if entries[name] != sum {
				return apierror.New(
					http.StatusUnprocessableEntity,
					"Conan archive has unlisted or changed content",
				)
			}
		}
	}
	return nil
}

func (h handler) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		h.remoteSearch(w, r)
		return
	}
	if c.Class == repo.Virtual {
		h.virtualSearch(w, r)
		return
	}
	pattern := r.URL.Query().Get("q")
	if len(pattern) > 256 {
		h.bad(w, r, "Conan search pattern exceeds limit")
		return
	}
	match, err := glob(pattern, r.URL.Query().Get("ignorecase") != "False")
	if err != nil {
		h.bad(w, r, "invalid Conan search pattern")
		return
	}
	items, err := conanfmt.Files(r.Context(), h.k.DB(), c, "")
	if err != nil {
		h.error(w, r, err)
		return
	}
	var results []string
	for _, it := range items {
		f, ok := conanfmt.ParseStored(it.FullPath())
		if !ok || it.Folder || f.Package || f.File != conanfmt.Manifest ||
			!h.k.Can(r, it.FullPath(), types.ActionRead) {
			continue
		}
		name := f.Ref.Name + "/" + f.Ref.Version + "@" + f.Ref.User + "/" + f.Ref.Channel
		if match(name) {
			results = append(results, name)
		}
	}
	slices.Sort(results)
	results = slices.Compact(results)
	writeJSON(w, map[string]any{"results": results})
}

func glob(pattern string, ignoreCase bool) (func(string) bool, error) {
	if pattern == "" {
		pattern = "*"
	}
	parts := strings.Split(regexp.QuoteMeta(pattern), `\*`)
	expr := "^" + strings.Join(parts, ".*") + "$"
	expr = strings.ReplaceAll(expr, `\?`, ".")
	if ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("conan: search pattern: %w", err)
	}
	return re.MatchString, nil
}

// Package search answers a map of package IDs to their conaninfo.txt. Conan
// filters the package settings locally; a list_only query omits that content.
func (h handler) packageSearch(w http.ResponseWriter, r *http.Request, ref conanfmt.Reference) {
	if r.Method != http.MethodGet {
		h.method(w, r)
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		h.remotePackageSearch(w, r, ref)
		return
	}
	if c.Class == repo.Virtual {
		h.virtualPackageSearch(w, r, ref)
		return
	}
	if ref.Revision == "" {
		list, err := h.revisions(r, ref, "")
		if err != nil {
			h.error(w, r, err)
			return
		}
		ref.Revision = list[0].Revision
	}
	root := ref.Root() + "/package"
	if !h.k.RequireFolder(w, r, root, types.ActionRead) {
		return
	}
	items, err := conanfmt.Files(r.Context(), h.k.DB(), c, root)
	if err != nil {
		h.error(w, r, err)
		return
	}
	latest := map[string]types.Item{}
	for _, it := range items {
		f, ok := conanfmt.ParseStored(it.FullPath())
		if !ok || it.Folder || !f.Package || f.File != conanfmt.Manifest ||
			!h.k.Can(r, it.FullPath(), types.ActionRead) {
			continue
		}
		if old, ok := latest[f.ID]; !ok || old.Modified.Before(it.Modified) {
			latest[f.ID] = it
		}
	}
	results := map[string]any{}
	for id, marker := range latest {
		if r.URL.Query().Get("list_only") == "True" {
			results[id] = map[string]any{}
			continue
		}
		path := strings.TrimSuffix(marker.FullPath(), conanfmt.Manifest) + "conaninfo.txt"
		if !h.k.Can(r, path, types.ActionRead) {
			continue
		}
		it, err := h.k.DB().Items().Get(r.Context(), c.Key, false, path)
		if err != nil || it.Folder {
			continue
		}
		rc, _, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
		if err != nil {
			h.error(w, r, err)
			return
		}
		body, err := io.ReadAll(io.LimitReader(rc, 1<<20+1))
		_ = rc.Close()
		if err != nil || len(body) > 1<<20 {
			h.error(w, r, apierror.New(http.StatusBadGateway, "Invalid Conan package metadata"))
			return
		}
		results[id] = map[string]any{"content": string(body)}
	}
	writeJSON(w, results)
}

func denied(r *http.Request) error {
	// The kit's Require uses the same status split, but metadata walkers need
	// an error value before they can choose a member or return a representation.
	if p, ok := auth.FromContext(r.Context()); ok && !p.Anonymous {
		return apierror.New(http.StatusForbidden, "Forbidden")
	}
	return apierror.New(http.StatusUnauthorized, "Unauthorized")
}
