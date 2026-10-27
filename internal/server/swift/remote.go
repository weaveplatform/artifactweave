package swift

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	swiftfmt "github.com/weaveplatform/artifactweave/internal/format/swift"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteMetadataLimit = 2 << 20

func remoteInvalid() error {
	return bad(http.StatusBadGateway, "invalid upstream Swift registry response")
}

func parseReleases(body io.Reader) (releases, error) {
	var out releases
	data, err := io.ReadAll(io.LimitReader(body, remoteMetadataLimit+1))
	if err != nil || len(data) == 0 || len(data) > remoteMetadataLimit ||
		json.Unmarshal(data, &out) != nil ||
		out.Releases == nil {
		return releases{}, remoteInvalid()
	}
	for version := range out.Releases {
		if !swiftfmt.ValidVersion(version) {
			return releases{}, remoteInvalid()
		}
	}
	return out, nil
}

func parseReleaseInfo(body io.Reader, p swiftfmt.Package) (releaseInfo, error) {
	var out releaseInfo
	data, err := io.ReadAll(io.LimitReader(body, remoteMetadataLimit+1))
	if err != nil || len(data) == 0 || len(data) > remoteMetadataLimit ||
		json.Unmarshal(data, &out) != nil ||
		out.ID != p.ID() ||
		out.Version != p.Version {
		return releaseInfo{}, remoteInvalid()
	}
	found := false
	for _, resource := range out.Resources {
		if resource.Name != "source-archive" {
			continue
		}
		if found || resource.Type != "application/zip" || len(resource.Checksum) != 64 {
			return releaseInfo{}, remoteInvalid()
		}
		if _, err := hex.DecodeString(resource.Checksum); err != nil {
			return releaseInfo{}, remoteInvalid()
		}
		found = true
	}
	if !found {
		return releaseInfo{}, remoteInvalid()
	}
	return out, nil
}

func validateJSON(
	limit int64,
	parse func(io.Reader) error,
) func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error {
	return func(ctx context.Context, body io.ReadSeeker, size int64, _ source.MetadataResponse) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("swift: metadata validation: %w", err)
		}
		if size <= 0 || size > limit || parse(body) != nil {
			return remoteInvalid()
		}
		return nil
	}
}

func (h handler) remoteMetadata(
	r *http.Request,
	p swiftfmt.Package,
	kind string,
) (source.Result, error) {
	upstream := p.Scope + "/" + p.Name
	path := upstream + "/.swift-releases.json"
	parse := func(reader io.Reader) error { _, err := parseReleases(reader); return err }
	variant := "swift-releases-v1"
	if kind == "release" {
		upstream += "/" + p.Version
		path = p.Scope + "/" + p.Name + "/.swift-release-" + p.Version + ".json"
		parse = func(reader io.Reader) error { _, err := parseReleaseInfo(reader, p); return err }
		variant = "swift-release-v1"
	}
	resource := source.Resource{
		Kind:         source.ResourceKindMetadata,
		Path:         path,
		UpstreamPath: upstream,
		Method:       http.MethodGet,
		Accept:       "application/vnd.swift.registry.v1+json, application/json",
		ExpectedContentTypes: []string{
			"application/vnd.swift.registry.v1+json",
			"application/json",
		},
		PermissionFolder: true,
		PermissionPath:   p.Scope + "/" + p.Name,
		Variant:          variant,
		ValidateMetadata: validateJSON(remoteMetadataLimit, parse),
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("swift: remote metadata: %w", err)
	}
	if result.CheckRead == nil {
		if result.Stream != nil && result.Stream.Body != nil {
			_ = result.Stream.Body.Close()
		}
		return source.Result{}, remoteInvalid()
	}
	return h.metadataStream(r, result, remoteMetadataLimit)
}

func (h handler) metadataStream(
	r *http.Request,
	result source.Result,
	limit int64,
) (source.Result, error) {
	if result.Stream == nil && result.Item != nil {
		if result.Item.Size <= 0 || result.Item.Size > limit {
			return source.Result{}, remoteInvalid()
		}
		body, _, err := h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return source.Result{}, fmt.Errorf("swift: cached metadata blob: %w", err)
		}
		result.Stream = &source.Stream{
			Status: http.StatusOK,
			Body:   body,
			Representation: source.Representation{
				Size:      result.Item.Size,
				MediaType: result.Item.MimeType,
			},
		}
	}
	if result.Stream == nil {
		return source.Result{}, remoteInvalid()
	}
	return result, nil
}

func (h handler) remoteReleases(
	r *http.Request,
	p swiftfmt.Package,
) (releases, source.Result, error) {
	result, err := h.remoteMetadata(r, p, "releases")
	if err != nil {
		return releases{}, source.Result{}, err
	}
	defer result.Stream.Body.Close()
	out, err := parseReleases(result.Stream.Body)
	return out, result, err
}

func (h handler) remoteInfo(
	r *http.Request,
	p swiftfmt.Package,
) (releaseInfo, source.Result, error) {
	result, err := h.remoteMetadata(r, p, "release")
	if err != nil {
		return releaseInfo{}, source.Result{}, err
	}
	defer result.Stream.Body.Close()
	out, err := parseReleaseInfo(result.Stream.Body, p)
	return out, result, err
}

func archiveChecksum(info releaseInfo) string {
	for _, resource := range info.Resources {
		if resource.Name == "source-archive" {
			return strings.ToLower(resource.Checksum)
		}
	}
	return ""
}

func (h handler) remoteArchive(r *http.Request, p swiftfmt.Package) (source.Result, error) {
	path := p.ArchivePath()
	if !h.k.Can(r, path, types.ActionRead) {
		return source.Result{}, source.ErrDenied
	}
	info, metadata, err := h.remoteInfo(r, p)
	if err != nil {
		return source.Result{}, err
	}
	if metadata.CheckRead == nil {
		return source.Result{}, remoteInvalid()
	}
	if err := metadata.CheckRead(r.Context(), path); err != nil {
		return source.Result{}, fmt.Errorf("swift: archive permission: %w", err)
	}
	resource := pkgroute.Resource(r, path, "")
	resource.UpstreamPath = p.Scope + "/" + p.Name + "/" + p.Version + ".zip"
	resource.Query, resource.Accept = "", "application/vnd.swift.registry.v1+zip"
	resource.Checksums.SHA256 = archiveChecksum(info)
	resource.Variant = "swift-source-archive-v1"
	resource.Validate = func(ctx context.Context, reader io.ReadSeeker, size int64) error {
		_, err := swiftfmt.InspectArchive(ctx, reader, size)
		if err != nil {
			return remoteInvalid()
		}
		return nil
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("swift: remote archive: %w", err)
	}
	fence(&result, metadata, []string{path})
	return result, nil
}

func fence(result *source.Result, metadata source.Result, paths []string) {
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		if metadata.Revalidate != nil {
			if err := metadata.Revalidate(ctx); err != nil {
				return fmt.Errorf("swift: revalidate release metadata: %w", err)
			}
		}
		if metadata.CheckRead == nil {
			return source.ErrDenied
		}
		for _, path := range paths {
			if err := metadata.CheckRead(ctx, path); err != nil {
				return fmt.Errorf("swift: recheck archive permission: %w", err)
			}
		}
		return nil
	}
}

func remoteJSONResult(result source.Result, body any) (source.Result, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return source.Result{}, fmt.Errorf("swift: encode metadata: %w", err)
	}
	digest := sha256.Sum256(data)
	result.Stream = &source.Stream{
		Status: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)),
		Representation: source.Representation{
			MediaType: "application/json",
			Size:      int64(len(data)),
			ETag: fmt.Sprintf(
				`"%x"`,
				digest,
			),
			Checksums: types.Checksums{SHA256: hex.EncodeToString(digest[:])},
		},
	}
	return result, nil
}

func (h handler) remoteRead(w http.ResponseWriter, r *http.Request, q request) {
	p := q.packageID
	if q.kind != "releases" && !h.k.Require(w, r, p.ArchivePath(), types.ActionRead) {
		return
	}
	switch q.kind {
	case "releases":
		if !h.k.RequireFolder(w, r, p.Scope+"/"+p.Name, types.ActionRead) {
			return
		}
		out, result, err := h.remoteReleases(r, p)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		paths := []string{}
		for version := range out.Releases {
			p.Version = version
			path := p.ArchivePath()
			if !h.k.Can(r, path, types.ActionRead) {
				delete(out.Releases, version)
				continue
			}
			if err := result.CheckRead(r.Context(), path); errors.Is(err, source.ErrDenied) {
				delete(out.Releases, version)
				continue
			} else if err != nil {
				h.k.Error(w, r, err)
				return
			}
			out.Releases[version] = release{URL: releaseURL(r, p)}
			paths = append(paths, path)
		}
		if len(out.Releases) == 0 {
			h.k.Error(w, r, bad(http.StatusNotFound, "Swift package not found"))
			return
		}
		fence(&result, result, paths)
		result, err = remoteJSONResult(result, out)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Version", "1")
		h.k.ServeResult(w, r, result)
	case "release":
		info, result, err := h.remoteInfo(r, p)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		fence(&result, result, []string{p.ArchivePath()})
		result, err = remoteJSONResult(result, info)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Version", "1")
		h.k.ServeResult(w, r, result)
	case "archive":
		result, err := h.remoteArchive(r, p)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Version", "1")
		w.Header().Set("Content-Type", "application/zip")
		h.k.ServeResult(w, r, result)
	case "manifest":
		h.remoteManifest(w, r, p)
	default:
		h.k.Error(w, r, store.ErrNotFound)
	}
}

func (h handler) remoteManifest(w http.ResponseWriter, r *http.Request, p swiftfmt.Package) {
	// Registry manifests are a separate upstream representation and may differ
	// from the default manifest in a release's archive.
	upstream := p.Scope + "/" + p.Name + "/" + p.Version + "/Package.swift"
	cachePath := p.Scope + "/" + p.Name + "/.swift-manifest-" + p.Version
	variant := "swift-manifest-v1"
	if version := r.URL.Query().Get("swift-version"); version != "" {
		cachePath += "-" + version
		variant += ":" + version
	}
	resource := source.Resource{
		Kind:                 source.ResourceKindMetadata,
		Path:                 cachePath,
		UpstreamPath:         upstream,
		Method:               http.MethodGet,
		Query:                r.URL.RawQuery,
		Accept:               "application/vnd.swift.registry.v1+swift",
		ExpectedContentTypes: []string{"text/x-swift", "text/plain"},
		PermissionFolder:     true,
		PermissionPath:       p.Scope + "/" + p.Name,
		Variant:              variant,
		ValidateMetadata: func(ctx context.Context, reader io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("swift: manifest validation: %w", err)
			}
			if size <= 0 || size > 1<<20 {
				return remoteInvalid()
			}
			data, err := io.ReadAll(io.LimitReader(reader, 1<<20+1))
			if err != nil || int64(len(data)) != size ||
				!bytes.Contains(data, []byte("import PackageDescription")) {
				return remoteInvalid()
			}
			return nil
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	result, err = h.metadataStream(r, result, 1<<20)
	if err != nil {
		h.k.Error(w, r, remoteInvalid())
		return
	}
	if result.CheckRead == nil {
		_ = result.Stream.Body.Close()
		h.k.Error(w, r, remoteInvalid())
		return
	}
	fence(&result, result, []string{p.ArchivePath()})
	result.Stream.MediaType = "text/x-swift"
	w.Header().Set("Content-Version", "1")
	h.k.ServeResult(w, r, result)
}
