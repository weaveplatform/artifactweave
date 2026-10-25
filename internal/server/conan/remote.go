package conan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	conanfmt "github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxRemoteMetadata = 8 << 20

func recipeURL(r conanfmt.Reference) string {
	return strings.Join([]string{"v2", "conans", r.Name, r.Version, r.User, r.Channel}, "/")
}

func recipeRevisionURL(r conanfmt.Reference) string {
	return recipeURL(r) + "/revisions/" + r.Revision
}

func fileURL(f conanfmt.FilePath) string {
	base := recipeRevisionURL(f.Ref)
	if f.Package {
		base += "/packages/" + f.ID + "/revisions/" + f.PRev
	}
	return base + "/files/" + escapePath(f.File)
}

func escapePath(value string) string {
	parts := strings.Split(value, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// metadata accepts only complete, bounded JSON objects before the shared
// source may cache them. The caller then validates endpoint-specific fields
// and filters every name against its own permission snapshot.
func validateJSON(_ context.Context, body io.ReadSeeker, size int64) error {
	if size < 0 || size > maxRemoteMetadata {
		return source.ErrInvalidResult
	}
	data, err := io.ReadAll(io.LimitReader(body, maxRemoteMetadata+1))
	if err != nil || len(data) > maxRemoteMetadata || !json.Valid(data) {
		return source.ErrInvalidResult
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return source.ErrInvalidResult
	}
	return nil
}

func (h handler) remoteJSON(r *http.Request, rest, folder string) ([]byte, source.Result, error) {
	resource := source.Resource{
		Kind: source.Metadata, Path: ".conan/" + rest,
		EscapedPath:  ".conan/" + escapePath(rest),
		UpstreamPath: rest, Method: http.MethodGet, Query: r.URL.RawQuery,
		PermissionFolder: true, PermissionPath: folder,
		ExpectedContentType: "application/json", Variant: "conan-json-v1",
		Validate: validateJSON,
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, source.Result{}, fmt.Errorf(
			"read Conan metadata: %w",
			pkgroute.SourceError(err, false),
		)
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			return nil, source.Result{}, fmt.Errorf(
				"revalidate Conan metadata: %w",
				pkgroute.SourceError(err, false),
			)
		}
	}
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil:
		reader, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, source.Result{}, fmt.Errorf("open Conan metadata: %w", err)
		}
	default:
		return nil, source.Result{}, source.ErrInvalidResult
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maxRemoteMetadata+1))
	if err != nil || len(data) > maxRemoteMetadata || !json.Valid(data) {
		return nil, source.Result{}, source.ErrInvalidResult
	}
	return data, result, nil
}

func (h handler) remoteRevisions(
	r *http.Request,
	ref conanfmt.Reference,
	id string,
) ([]revision, error) {
	rest, folder := recipeURL(
		ref,
	)+"/revisions", strings.Join(
		[]string{ref.User, ref.Name, ref.Version, ref.Channel},
		"/",
	)
	if id != "" {
		rest = recipeRevisionURL(ref) + "/packages/" + id + "/revisions"
		folder = ref.Root() + "/package/" + id
	}
	data, result, err := h.remoteJSON(r, rest, folder)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Revisions []revision `json:"revisions"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || len(wire.Revisions) > 10000 {
		return nil, source.ErrInvalidResult
	}
	var out []revision
	for _, rev := range wire.Revisions {
		if !conanfmt.Component(rev.Revision) || rev.Time.IsZero() {
			return nil, source.ErrInvalidResult
		}
		if h.k.CanFolder(r, folder, types.ActionRead) {
			if result.CheckRead != nil {
				candidate := ref
				candidate.Revision = rev.Revision
				path := candidate.RecipeRoot() + "/" + conanfmt.Manifest
				if id != "" {
					path = ref.PackageRoot(id, rev.Revision) + "/" + conanfmt.Manifest
				}
				if err := result.CheckRead(r.Context(), path); err != nil {
					continue
				}
			}
			out = append(out, rev)
		}
	}
	if len(out) == 0 {
		return nil, apierror.New(http.StatusNotFound, "Conan revision not found")
	}
	slices.SortFunc(out, func(a, b revision) int {
		if n := b.Time.Compare(a.Time); n != 0 {
			return n
		}
		return strings.Compare(b.Revision, a.Revision)
	})
	if result.Used != nil {
		_ = result.Used(r.Context())
	}
	return out, nil
}

func (h handler) remoteFileList(w http.ResponseWriter, r *http.Request, root string) {
	f, ok := conanfmt.ParseStored(root + "/" + conanfmt.Manifest)
	if !ok {
		h.bad(w, r, "invalid Conan revision root")
		return
	}
	if _, err := h.remoteManifest(r, f); err != nil {
		h.error(w, r, err)
		return
	}
	rest := recipeRevisionURL(f.Ref)
	if f.Package {
		rest += "/packages/" + f.ID + "/revisions/" + f.PRev
	}
	data, result, err := h.remoteJSON(r, rest+"/files", root)
	if err != nil {
		h.error(w, r, err)
		return
	}
	files, err := h.validRemoteFiles(r, root, data, result)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"files": files})
	if result.Used != nil {
		_ = result.Used(r.Context())
	}
}

func (h handler) validRemoteFiles(
	r *http.Request, root string, data []byte, result source.Result,
) (map[string]any, error) {
	var wire struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || len(wire.Files) > 10000 {
		return nil, source.ErrInvalidResult
	}
	if _, ok := wire.Files[conanfmt.Manifest]; !ok {
		return nil, source.ErrInvalidResult
	}
	out := map[string]any{}
	for name := range wire.Files {
		if !conanfmt.SafeFile(name) {
			return nil, source.ErrInvalidResult
		}
		path := root + "/" + name
		if !h.k.Can(r, path, types.ActionRead) {
			return nil, denied(r)
		}
		if result.CheckRead != nil {
			if err := result.CheckRead(r.Context(), path); err != nil {
				return nil, fmt.Errorf(
					"check Conan file permission: %w",
					pkgroute.SourceError(err, false),
				)
			}
		}
		out[name] = map[string]any{}
	}
	return out, nil
}

func (h handler) remoteFile(w http.ResponseWriter, r *http.Request, f conanfmt.FilePath) {
	// A manifest supplies the expected digest of every downloadable Conan
	// file. Fetching and parsing it through the source boundary prevents a
	// mutable upstream from publishing bytes that disagree with its revision.
	checksums := types.Checksums{}
	var archiveEntries map[string]string
	if f.File != conanfmt.Manifest {
		manifest := f
		manifest.File = conanfmt.Manifest
		body, err := h.remoteManifest(r, manifest)
		if err != nil {
			h.error(w, r, err)
			return
		}
		if strings.HasSuffix(f.File, ".tgz") {
			archiveEntries = body
		} else {
			sum, ok := body[f.File]
			if !ok {
				h.notFound(w, r)
				return
			}
			checksums.MD5 = sum
		}
	}
	resource := source.Resource{
		Kind: source.Artifact, Path: f.Path(), EscapedPath: escapePath(f.Path()),
		UpstreamPath: fileURL(f), Method: http.MethodGet,
		Checksums: checksums, Variant: "conan-file-v1",
	}
	if f.File == conanfmt.Manifest {
		resource.Validate = validateRemoteManifest
	} else if archiveEntries != nil {
		resource.Validate = func(_ context.Context, reader io.ReadSeeker, size int64) error {
			if size < 0 || size > maxUpload {
				return source.ErrInvalidResult
			}
			files, err := archiveDigests(reader)
			if err != nil {
				return source.ErrInvalidResult
			}
			if len(files) == 0 && !f.Package {
				return source.ErrInvalidResult
			}
			for name, sum := range files {
				if archiveEntries[name] != sum {
					return source.ErrInvalidResult
				}
			}
			if f.Package {
				for name := range archiveEntries {
					if name != "conaninfo.txt" && files[name] == "" {
						return source.ErrInvalidResult
					}
				}
			}
			return nil
		}
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.k.ServeResult(w, r, result)
}

func validateRemoteManifest(_ context.Context, reader io.ReadSeeker, size int64) error {
	if size < 0 || size > maxManifest {
		return source.ErrInvalidResult
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxManifest+1))
	if err != nil || len(data) > maxManifest {
		return source.ErrInvalidResult
	}
	_, err = parseManifest(data)
	if err != nil {
		return source.ErrInvalidResult
	}
	return nil
}

func (h handler) remoteManifest(r *http.Request, f conanfmt.FilePath) (map[string]string, error) {
	resource := source.Resource{
		Kind: source.Artifact, Path: f.Path(), EscapedPath: escapePath(f.Path()),
		UpstreamPath: fileURL(f), Method: http.MethodGet,
		Variant: "conan-manifest-v1", Validate: validateRemoteManifest,
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, fmt.Errorf("read Conan manifest: %w", pkgroute.SourceError(err, false))
	}
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil:
		reader, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("open Conan manifest: %w", err)
		}
	default:
		return nil, source.ErrInvalidResult
	}
	defer func() { _ = reader.Close() }()
	body, err := io.ReadAll(io.LimitReader(reader, maxManifest+1))
	if err != nil || len(body) > maxManifest {
		return nil, source.ErrInvalidResult
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(r.Context()); err != nil {
			return nil, fmt.Errorf(
				"revalidate Conan manifest: %w",
				pkgroute.SourceError(err, false),
			)
		}
	}
	entries, err := parseManifest(body)
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	needed := "conanfile.py"
	if f.Package {
		needed = "conaninfo.txt"
	}
	if entries[needed] == "" {
		return nil, source.ErrInvalidResult
	}
	return entries, nil
}

func (h handler) remoteSearch(w http.ResponseWriter, r *http.Request) {
	data, result, err := h.remoteJSON(r, "v2/conans/search", "")
	if err != nil {
		h.error(w, r, err)
		return
	}
	var wire struct {
		Results []string `json:"results"`
	}
	if err := json.Unmarshal(data, &wire); err != nil || len(wire.Results) > 10000 {
		h.error(w, r, source.ErrInvalidResult)
		return
	}
	var results []string
	for _, value := range wire.Results {
		ref, ok := parseReference(value)
		if !ok {
			h.error(w, r, source.ErrInvalidResult)
			return
		}
		folder := strings.Join([]string{ref.User, ref.Name, ref.Version, ref.Channel}, "/")
		if !h.k.CanFolder(r, folder, types.ActionRead) {
			continue
		}
		if result.CheckFolder != nil {
			if err := result.CheckFolder(r.Context(), folder); err != nil {
				continue
			}
		}
		results = append(results, value)
	}
	slices.Sort(results)
	writeJSON(w, map[string]any{"results": slices.Compact(results)})
	if result.Used != nil {
		_ = result.Used(r.Context())
	}
}

func parseReference(value string) (conanfmt.Reference, bool) {
	main, uc, hasUC := strings.Cut(value, "@")
	name, version, ok := strings.Cut(main, "/")
	if !ok {
		return conanfmt.Reference{}, false
	}
	r := conanfmt.Reference{Name: name, Version: version, User: "_", Channel: "_"}
	if hasUC {
		r.User, r.Channel, ok = strings.Cut(uc, "/")
	}
	return r, ok && conanfmt.Component(r.Name) && conanfmt.Component(r.Version) &&
		conanfmt.Component(r.User) && conanfmt.Component(r.Channel)
}

func (h handler) remotePackageSearch(
	w http.ResponseWriter,
	r *http.Request,
	ref conanfmt.Reference,
) {
	rest := recipeURL(ref)
	if ref.Revision != "" {
		rest = recipeRevisionURL(ref)
	}
	folder := strings.Join([]string{ref.User, ref.Name, ref.Version, ref.Channel}, "/")
	if ref.Revision != "" {
		folder = ref.Root()
	}
	data, result, err := h.remoteJSON(r, rest+"/search", folder)
	if err != nil {
		h.error(w, r, err)
		return
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) > 10000 {
		h.error(w, r, source.ErrInvalidResult)
		return
	}
	filtered := map[string]json.RawMessage{}
	for id, raw := range entries {
		if !conanfmt.Component(id) || !json.Valid(raw) {
			h.error(w, r, source.ErrInvalidResult)
			return
		}
		path := folder + "/package/" + id
		if !h.k.CanFolder(r, path, types.ActionRead) {
			continue
		}
		if result.CheckFolder != nil {
			if err := result.CheckFolder(r.Context(), path); err != nil {
				continue
			}
		}
		filtered[id] = raw
	}
	writeJSON(w, filtered)
	if result.Used != nil {
		_ = result.Used(r.Context())
	}
}
