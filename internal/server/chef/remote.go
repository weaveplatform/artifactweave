package chef

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
	"strconv"
	"strings"

	cheffmt "github.com/weaveplatform/artifactweave/internal/format/chef"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxMetadataBytes = 4 << 20

type cookbookDocument struct {
	Name          string   `json:"name"`
	Maintainer    string   `json:"maintainer"`
	Description   string   `json:"description"`
	Versions      []string `json:"versions"`
	LatestVersion string   `json:"latest_version"`
}
type versionDocument struct {
	Version      string            `json:"version"`
	File         string            `json:"file"`
	Cookbook     string            `json:"cookbook"`
	Size         int64             `json:"tarball_file_size"`
	SHA256       string            `json:"sha256"`
	License      string            `json:"license"`
	Dependencies map[string]string `json:"dependencies"`
	Platforms    map[string]string `json:"platforms"`
}
type catalogDocument struct {
	Total int `json:"total"`
	Start int `json:"start"`
	Items []struct {
		Name string `json:"cookbook_name"`
	} `json:"items"`
}

func invalidUpstream() error { return apierror.New(502, "invalid upstream Chef metadata") }
func validURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Fragment != "" ||
		u.Opaque != "" ||
		strings.ContainsAny(u.Path, "\\\x00") {
		return false
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return false
		}
	}
	return true
}

func registryURL(c repo.Config, path string) (*url.URL, error) {
	u, err := url.Parse(c.URL)
	if err != nil || !validURL(u) {
		return nil, invalidUpstream()
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = ""
	return u, nil
}

func checkRegistry(c repo.Config, target *url.URL) error {
	base, err := url.Parse(c.URL)
	if err != nil || !validURL(base) || !validURL(target) || base.Scheme != target.Scheme ||
		base.Host != target.Host ||
		!strings.HasPrefix(target.Path, strings.TrimRight(base.Path, "/")+"/") {
		return source.ErrDenied
	}
	return nil
}

func decode(data []byte, v any) error {
	if len(data) == 0 || len(data) > maxMetadataBytes ||
		cheffmt.UniqueFields(
			data,
			"name",
			"maintainer",
			"description",
			"versions",
			"latest_version",
			"version",
			"file",
			"cookbook",
			"tarball_file_size",
			"sha256",
			"license",
			"dependencies",
			"platforms",
			"total",
			"start",
			"items",
		) != nil ||
		json.Unmarshal(data, v) != nil {
		return invalidUpstream()
	}
	return nil
}

func cookbookVersions(data []byte, name string, c repo.Config) (cookbookDocument, []string, error) {
	var doc cookbookDocument
	if decode(data, &doc) != nil || doc.Name != name || len(doc.Versions) == 0 ||
		len(doc.Versions) > maxVersions {
		return doc, nil, invalidUpstream()
	}
	expected, err := registryURL(c, "/api/v1/cookbooks/"+name+"/versions/")
	if err != nil {
		return doc, nil, err
	}
	versions := make([]string, 0, len(doc.Versions))
	seen := map[string]bool{}
	for _, value := range doc.Versions {
		u, err := url.Parse(value)
		if err != nil {
			return doc, nil, invalidUpstream()
		}
		u = expected.ResolveReference(u)
		if checkRegistry(c, u) != nil || u.RawQuery != "" ||
			!strings.HasPrefix(u.Path, expected.Path) {
			return doc, nil, invalidUpstream()
		}
		v := strings.ReplaceAll(strings.TrimPrefix(u.Path, expected.Path), "_", ".")
		if !cheffmt.ValidVersion(v) || seen[v] {
			return doc, nil, invalidUpstream()
		}
		seen[v] = true
		versions = append(versions, v)
	}
	return doc, versions, nil
}

func parseVersion(data []byte, name, version string, c repo.Config) (release, error) {
	var doc versionDocument
	if decode(data, &doc) != nil || doc.Version != version || doc.Size <= 0 ||
		doc.Size > cheffmt.MaxArchiveBytes {
		return release{}, invalidUpstream()
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for _, key := range []string{"dependencies", "platforms"} {
		if raw, ok := fields[key]; ok && string(raw) != "null" && cheffmt.UniqueFields(raw) != nil {
			return release{}, invalidUpstream()
		}
	}
	expected, err := registryURL(c, versionPath(name, version))
	if err != nil {
		return release{}, err
	}
	u, err := url.Parse(doc.File)
	if err != nil || doc.File == "" || u.IsAbs() && !validURL(u) {
		return release{}, invalidUpstream()
	}
	u = expected.ResolveReference(u)
	if !validURL(u) {
		return release{}, invalidUpstream()
	}
	cookbook, err := url.Parse(doc.Cookbook)
	if err != nil {
		return release{}, invalidUpstream()
	}
	cookbook = expected.ResolveReference(cookbook)
	wanted, _ := registryURL(c, "/api/v1/cookbooks/"+name)
	if cookbook.String() != wanted.String() {
		return release{}, invalidUpstream()
	}
	if doc.SHA256 != "" {
		b, e := hex.DecodeString(doc.SHA256)
		if e != nil || len(b) != 32 {
			return release{}, invalidUpstream()
		}
	}
	m := cheffmt.Manifest{
		Name:         name,
		Version:      version,
		License:      doc.License,
		Dependencies: doc.Dependencies,
		Platforms:    doc.Platforms,
	}
	if !cheffmt.ValidateManifest(m) {
		return release{}, invalidUpstream()
	}
	return release{
		meta: cheffmt.Metadata{
			Name:     name,
			Version:  version,
			Path:     cheffmt.Path(name, version),
			SHA256:   strings.ToLower(doc.SHA256),
			Size:     doc.Size,
			Manifest: m,
		},
		remoteURL: u.String(),
	}, nil
}

func (h handler) metadata(
	r *http.Request,
	path, permission string,
	query url.Values,
	validate func([]byte) error,
) ([]byte, source.Result, error) {
	c := pkgroute.RequestFrom(r).Repo
	target, err := registryURL(c, path)
	if err != nil {
		return nil, source.Result{}, err
	}
	target.RawQuery = query.Encode()
	resource := source.Resource{
		Kind:                source.ResourceKindMetadata,
		Path:                strings.TrimPrefix(path, "/"),
		ExternalURL:         target.String(),
		Method:              http.MethodGet,
		NoCrossOriginAuth:   true,
		PermissionFolder:    true,
		PermissionPath:      permission,
		Variant:             "chef-supermarket-metadata-v1",
		ExpectedContentType: mediaType,
		CheckURL:            func(_ context.Context, u *url.URL) error { return checkRegistry(c, u) },
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			u, e := url.Parse(meta.BaseURL)
			if e != nil || checkRegistry(c, u) != nil {
				return invalidUpstream()
			}
			data, e := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
			if e != nil {
				return invalidUpstream()
			}
			return validate(data)
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, result, fmt.Errorf("chef: metadata: %w", pkgroute.SourceError(err, false))
	}
	if result.CheckRead == nil || result.Revalidate == nil {
		if result.Stream != nil {
			_ = result.Stream.Body.Close()
		}
		return nil, result, invalidUpstream()
	}
	data, err := h.metadataBytes(r, result)
	if err != nil {
		return nil, result, err
	}
	finalURL, e := url.Parse(result.Metadata.BaseURL)
	if e != nil || checkRegistry(c, finalURL) != nil {
		return nil, result, invalidUpstream()
	}
	if err = validate(data); err != nil {
		return nil, result, err
	}
	if err = result.Revalidate(r.Context()); err != nil {
		return nil, result, fmt.Errorf(
			"chef: metadata authority: %w",
			pkgroute.SourceError(err, false),
		)
	}
	return data, result, nil
}

func (h handler) metadataBytes(r *http.Request, result source.Result) ([]byte, error) {
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil:
		rc, _, err := h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("chef: cached metadata: %w", err)
		}
		reader = rc
	default:
		return nil, invalidUpstream()
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxMetadataBytes+1))
	if err != nil || len(data) > maxMetadataBytes {
		return nil, invalidUpstream()
	}
	return data, nil
}

func (h handler) remoteVersions(r *http.Request, name string) ([]release, error) {
	c := pkgroute.RequestFrom(r).Repo
	data, index, err := h.metadata(
		r,
		"/api/v1/cookbooks/"+name,
		name,
		nil,
		func(data []byte) error { _, _, e := cookbookVersions(data, name, c); return e },
	)
	if err != nil {
		return nil, err
	}
	doc, versions, err := cookbookVersions(data, name, c)
	if err != nil {
		return nil, err
	}
	out := make([]release, 0, len(versions))
	for _, version := range versions {
		path := cheffmt.Path(name, version)
		if !h.k.Can(r, path, types.ActionRead) || generic.CheckDownload(c, path, false) != nil {
			continue
		}
		if err = index.CheckRead(r.Context(), path); errors.Is(err, source.ErrDenied) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("chef: version authority: %w", pkgroute.SourceError(err, false))
		}
		data, result, err := h.metadata(
			r,
			versionPath(name, version),
			name,
			nil,
			func(data []byte) error { _, e := parseVersion(data, name, version, c); return e },
		)
		if err != nil {
			return nil, err
		}
		rel, err := parseVersion(data, name, version, c)
		if err != nil {
			return nil, err
		}
		rel.meta.Manifest.Maintainer = doc.Maintainer
		rel.meta.Manifest.Description = doc.Description
		rel.revalidate = func(ctx context.Context) error {
			if e := index.Revalidate(ctx); e != nil {
				return fmt.Errorf("chef: upstream authority: %w", e)
			}
			return result.Revalidate(ctx)
		}
		rel.checkRead = func(ctx context.Context, path string) error {
			if e := index.CheckRead(ctx, path); e != nil {
				return fmt.Errorf("chef: upstream authority: %w", e)
			}
			return result.CheckRead(ctx, path)
		}
		out = append(out, rel)
	}
	sortReleases(out)
	return out, nil
}

func (h handler) remoteNames(r *http.Request) ([]string, error) {
	names := []string{}
	seen := map[string]bool{}
	for offset := 0; offset < maxVersions; offset += 1000 {
		var doc catalogDocument
		_, _, err := h.metadata(
			r,
			"/api/v1/cookbooks",
			"",
			url.Values{"start": {strconv.Itoa(offset)}, "items": {"1000"}},
			func(data []byte) error {
				if decode(data, &doc) != nil || doc.Start != offset ||
					doc.Total < offset+len(doc.Items) ||
					doc.Total > maxVersions ||
					len(doc.Items) > 1000 ||
					offset+len(doc.Items) < doc.Total && len(doc.Items) != 1000 {
					return invalidUpstream()
				}
				for _, entry := range doc.Items {
					if !cheffmt.ValidName(entry.Name) {
						return invalidUpstream()
					}
				}
				return nil
			},
		)
		if err != nil {
			return nil, err
		}
		for _, entry := range doc.Items {
			if seen[entry.Name] {
				return nil, invalidUpstream()
			}
			seen[entry.Name] = true
			names = append(names, entry.Name)
		}
		if offset+len(doc.Items) == doc.Total {
			return names, nil
		}
	}
	return nil, invalidUpstream()
}

func (h handler) remoteArchive(w http.ResponseWriter, r *http.Request, rel release) {
	member := r
	if rel.member != nil {
		member = rel.member
	}
	if rel.checkRead == nil || rel.revalidate == nil {
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	if err := rel.checkRead(r.Context(), rel.meta.Path); err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	resource := source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              rel.meta.Path,
		ExternalURL:       rel.remoteURL,
		NoCrossOriginAuth: true,
		Method:            http.MethodGet,
		Variant:           "chef-supermarket-archive-v1",
		Checksums:         types.Checksums{SHA256: rel.meta.SHA256},
		// Supermarket downloads may redirect to object storage. The source service
		// retains its DNS/SSRF and redirect limits and strips cross-origin credentials.
		CheckURL: func(_ context.Context, u *url.URL) error {
			if !validURL(u) {
				return source.ErrDenied
			}
			return nil
		},
		Validate: func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size != rel.meta.Size {
				return source.ErrInvalidResult
			}
			m, err := cheffmt.ReadArchive(body, size)
			if err != nil || m.Name != rel.meta.Name || m.Version != rel.meta.Version ||
				!maps.Equal(m.Manifest.Dependencies, rel.meta.Manifest.Dependencies) ||
				!maps.Equal(m.Manifest.Platforms, rel.meta.Manifest.Platforms) {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(member, resource)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if result.Revalidate == nil {
		if result.Stream != nil {
			_ = result.Stream.Body.Close()
		}
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	prior := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if e := rel.revalidate(ctx); e != nil {
			return e
		}
		if e := rel.checkRead(ctx, rel.meta.Path); e != nil {
			return e
		}
		return prior(ctx)
	}
	h.k.ServeResult(w, member, result)
}
