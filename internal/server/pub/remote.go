package pub

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
	"net/url"
	"strings"

	pubfmt "github.com/weaveplatform/artifactweave/internal/format/pub"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	maxMetadataBytes = 16 << 20
	maxVersions      = 4096
)

type upstreamVersion struct {
	Version       string         `json:"version"`
	ArchiveURL    string         `json:"archive_url"`
	ArchiveSHA256 string         `json:"archive_sha256"`
	Pubspec       map[string]any `json:"pubspec"`
}

type upstreamDocument struct {
	Name     string            `json:"name"`
	Versions []upstreamVersion `json:"versions"`
}

func invalidUpstream() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Dart pub metadata")
}

func parseVersions(data []byte, name, responseBase string) ([]release, error) {
	if len(data) == 0 || len(data) > maxMetadataBytes {
		return nil, invalidUpstream()
	}
	var doc upstreamDocument
	if json.Unmarshal(data, &doc) != nil || doc.Name != name ||
		len(doc.Versions) == 0 || len(doc.Versions) > maxVersions {
		return nil, invalidUpstream()
	}
	base, err := url.Parse(responseBase)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, invalidUpstream()
	}
	out := make([]release, 0, len(doc.Versions))
	seen := map[string]bool{}
	for _, v := range doc.Versions {
		if !pubfmt.ValidVersion(v.Version) || seen[v.Version] ||
			!validPubspec(v.Pubspec, name, v.Version) ||
			v.ArchiveSHA256 != "" && !validSHA256(v.ArchiveSHA256) {
			return nil, invalidUpstream()
		}
		seen[v.Version] = true
		u, err := url.Parse(v.ArchiveURL)
		if err != nil || v.ArchiveURL == "" {
			return nil, invalidUpstream()
		}
		u = base.ResolveReference(u)
		if !validArchiveURL(u) {
			return nil, invalidUpstream()
		}
		out = append(out, release{
			meta: pubfmt.Metadata{
				Name: name, Version: v.Version,
				Path: pubfmt.Path(name, v.Version), SHA256: v.ArchiveSHA256,
				Pubspec: v.Pubspec,
			},
			remoteURL: u.String(),
		})
	}
	sortReleases(out)
	return out, nil
}

func validPubspec(doc map[string]any, name, version string) bool {
	if doc == nil {
		return false
	}
	gotName, okName := doc["name"].(string)
	gotVersion, okVersion := doc["version"].(string)
	return okName && okVersion && gotName == name && gotVersion == version
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validArchiveURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") &&
		u.Host != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func (h handler) remoteVersions(r *http.Request, name string) ([]release, error) {
	path := "api/packages/" + name
	resource := source.Resource{
		Kind:                 source.Metadata,
		Path:                 path,
		UpstreamPath:         path,
		Method:               http.MethodGet,
		PermissionFolder:     true,
		PermissionPath:       name,
		Variant:              "dart-pub-v2-versions-v1",
		ExpectedContentTypes: []string{mediaType, "application/json"},
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
			if err != nil || len(data) > maxMetadataBytes {
				return invalidUpstream()
			}
			_, err = parseVersions(data, name, meta.BaseURL)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, fmt.Errorf("pub: read remote metadata: %w", pkgroute.SourceError(err, false))
	}
	data, err := h.metadataBytes(r, result)
	if err != nil {
		return nil, err
	}
	if result.CheckRead == nil || result.Revalidate == nil {
		return nil, fmt.Errorf("pub: metadata authority missing: %w",
			pkgroute.SourceError(source.ErrInvalidResult, false))
	}
	releases, err := parseVersions(data, name, result.Metadata.BaseURL)
	if err != nil {
		return nil, err
	}
	if err := result.Revalidate(r.Context()); err != nil {
		return nil, fmt.Errorf(
			"pub: revalidate remote metadata: %w",
			pkgroute.SourceError(err, false),
		)
	}
	out := releases[:0]
	for _, rel := range releases {
		if !h.k.Can(r, rel.meta.Path, types.ActionRead) {
			continue
		}
		if err := result.CheckRead(r.Context(), rel.meta.Path); err != nil {
			if errors.Is(err, source.ErrDenied) {
				continue
			}
			return nil, fmt.Errorf(
				"pub: check remote version read: %w",
				pkgroute.SourceError(err, false),
			)
		}
		rel.revalidate = result.Revalidate
		rel.checkRead = result.CheckRead
		out = append(out, rel)
	}
	return out, nil
}

func (h handler) metadataBytes(r *http.Request, result source.Result) ([]byte, error) {
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil && h.blobs != nil:
		rc, _, err := h.blobs.Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("pub: open cached metadata: %w", err)
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

func (h handler) remoteArchive(w http.ResponseWriter, r *http.Request, rel release) {
	member := r
	if rel.member != nil {
		member = rel.member
	}
	if rel.checkRead == nil || rel.revalidate == nil {
		h.k.Error(w, r, pkgroute.SourceError(source.ErrInvalidResult, false))
		return
	}
	if err := rel.checkRead(member.Context(), rel.meta.Path); err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	resource := source.Resource{
		Kind: source.Artifact, Path: rel.meta.Path, Method: http.MethodGet,
		Variant:   "dart-pub-v2-archive-v1",
		Checksums: types.Checksums{SHA256: rel.meta.SHA256},
		Validate: func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size <= 0 || size > pubfmt.MaxArchiveBytes {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(body, pubfmt.MaxArchiveBytes+1))
			if err != nil || len(data) > pubfmt.MaxArchiveBytes {
				return source.ErrInvalidResult
			}
			if rel.meta.SHA256 != "" {
				actual := sha256.Sum256(data)
				if hex.EncodeToString(actual[:]) != rel.meta.SHA256 {
					return source.ErrInvalidResult
				}
			}
			meta, err := pubfmt.ReadArchive(bytes.NewReader(data), int64(len(data)))
			if err != nil || meta.Name != rel.meta.Name || meta.Version != rel.meta.Version {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	if path, query, ok := withinRegistry(pkgroute.RequestFrom(member).Repo, rel.remoteURL); ok {
		resource.UpstreamPath, resource.Query = path, query
	} else {
		resource.ExternalURL = rel.remoteURL
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
		if err := rel.revalidate(ctx); err != nil {
			return err
		}
		if err := rel.checkRead(ctx, rel.meta.Path); err != nil {
			return err
		}
		return prior(ctx)
	}
	h.k.ServeResult(w, member, result)
}

// Same-origin archive links use the repository's ordinary transport. A
// metadata-selected cross-origin link needs its explicit external URL policy.
func withinRegistry(c repo.Config, target string) (string, string, bool) {
	base, baseErr := url.Parse(strings.TrimRight(c.URL, "/"))
	u, targetErr := url.Parse(target)
	if baseErr != nil || targetErr != nil || base.Scheme != u.Scheme ||
		base.Host != u.Host || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/") {
		return "", "", false
	}
	rel := strings.TrimPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/")
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return "", "", false
	}
	return rel, u.RawQuery, true
}
