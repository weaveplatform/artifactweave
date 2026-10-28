package puppet

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
	"reflect"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format/generic"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/format/puppet"
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
	Version    string             `json:"version"`
	FileURI    string             `json:"file_uri"`
	FileSHA256 string             `json:"file_sha256"`
	FileSize   int64              `json:"file_size"`
	Metadata   puppetfmt.Manifest `json:"metadata"`
}
type upstreamDocument struct {
	Results    []upstreamVersion `json:"results"`
	Pagination *pagination       `json:"pagination"`
}

func invalidUpstream() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream Puppet metadata")
}

func parseVersions(data []byte, name, responseBase string, offset int) ([]release, bool, error) {
	if len(data) == 0 || len(data) > maxMetadataBytes {
		return nil, false, invalidUpstream()
	}
	var doc upstreamDocument
	if json.Unmarshal(data, &doc) != nil || doc.Pagination == nil || len(doc.Results) > 1000 ||
		doc.Pagination.Offset != offset || doc.Pagination.Total < offset+len(doc.Results) ||
		doc.Pagination.Total > maxVersions {
		return nil, false, invalidUpstream()
	}
	base, err := url.Parse(responseBase)
	if err != nil || !validArchiveURL(base) || !strings.HasSuffix(base.Path, "/v3/releases") {
		return nil, false, invalidUpstream()
	}
	out := make([]release, 0, len(doc.Results))
	seen := map[string]bool{}
	for _, v := range doc.Results {
		if !puppetfmt.ValidateManifest(v.Metadata) || v.Metadata.Name != name ||
			v.Version != v.Metadata.Version ||
			seen[v.Version] ||
			!validSHA256(v.FileSHA256) ||
			v.FileSize <= 0 ||
			v.FileSize > puppetfmt.MaxArchiveBytes {
			return nil, false, invalidUpstream()
		}
		seen[v.Version] = true
		u, err := url.Parse(v.FileURI)
		if err != nil || v.FileURI == "" {
			return nil, false, invalidUpstream()
		}
		u = base.ResolveReference(u)
		if !archiveWithinFeed(base, u) {
			return nil, false, invalidUpstream()
		}
		out = append(
			out,
			release{
				meta: puppetfmt.Metadata{
					Name:     name,
					Version:  v.Version,
					Path:     puppetfmt.Path(name, v.Version),
					SHA256:   v.FileSHA256,
					Size:     v.FileSize,
					Manifest: v.Metadata,
				},
				remoteURL: u.String(),
			},
		)
	}
	more := offset+len(out) < doc.Pagination.Total
	// Never follow an upstream-supplied pagination URL. Its presence must agree
	// with the bounded total; subsequent pages use our canonical query.
	if more && (len(out) != 1000 || doc.Pagination.Next == nil) ||
		!more && doc.Pagination.Next != nil {
		return nil, false, invalidUpstream()
	}
	return out, more, nil
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
	all := []release{}
	seen := map[string]bool{}
	for offset := 0; offset < maxVersions; offset += 1000 {
		found, more, err := h.remotePage(r, name, offset)
		if err != nil {
			return nil, err
		}
		for _, rel := range found {
			if seen[rel.meta.Version] {
				return nil, invalidUpstream()
			}
			seen[rel.meta.Version] = true
			all = append(all, rel)
		}
		if !more {
			sortReleases(all)
			return all, nil
		}
	}
	return nil, invalidUpstream()
}

func (h handler) remotePage(r *http.Request, name string, offset int) ([]release, bool, error) {
	// Forge query parameters identify a protocol resource; they are not caller
	// query propagation. An explicit same-source URL keeps them in cache identity
	// even when propagateQueryParams is false.
	base, err := url.Parse(pkgroute.RequestFrom(r).Repo.URL)
	if err != nil || !validArchiveURL(base) {
		return nil, false, invalidUpstream()
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v3/releases"
	base.RawPath = ""
	query := base.Query()
	query.Set("module", name)
	query.Set("limit", "1000")
	query.Set("offset", strconv.Itoa(offset))
	query.Set("sort_by", "version")
	base.RawQuery = query.Encode()
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		Path:              "v3/releases/" + name,
		ExternalURL:       base.String(),
		NoCrossOriginAuth: true,
		CheckURL: func(_ context.Context, target *url.URL) error {
			return checkRegistryURL(pkgroute.RequestFrom(r).Repo, target)
		},
		Method:              http.MethodGet,
		PermissionFolder:    true,
		PermissionPath:      name,
		Variant:             "puppet-forge-v3-releases-v1",
		ExpectedContentType: mediaType,
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, meta source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
			if err != nil {
				return invalidUpstream()
			}
			if _, _, ok := withinRegistry(pkgroute.RequestFrom(r).Repo, meta.BaseURL); !ok {
				return invalidUpstream()
			}
			_, _, err = parseVersions(data, name, meta.BaseURL, offset)
			return err
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return nil, false, fmt.Errorf("puppet: metadata: %w", pkgroute.SourceError(err, false))
	}
	data, err := h.metadataBytes(r, result)
	if err != nil {
		return nil, false, err
	}
	if result.CheckRead == nil || result.Revalidate == nil {
		return nil, false, fmt.Errorf(
			"puppet: missing authority: %w",
			pkgroute.SourceError(source.ErrInvalidResult, false),
		)
	}
	if _, _, ok := withinRegistry(pkgroute.RequestFrom(r).Repo, result.Metadata.BaseURL); !ok {
		return nil, false, invalidUpstream()
	}
	releases, more, err := parseVersions(data, name, result.Metadata.BaseURL, offset)
	if err != nil {
		return nil, false, err
	}
	if err = result.Revalidate(r.Context()); err != nil {
		return nil, false, fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
	}
	out := releases[:0]
	for _, rel := range releases {
		if !h.k.Can(r, rel.meta.Path, types.ActionRead) ||
			generic.CheckDownload(pkgroute.RequestFrom(r).Repo, rel.meta.Path, false) != nil {
			continue
		}
		if err = result.CheckRead(r.Context(), rel.meta.Path); err != nil {
			if errors.Is(err, source.ErrDenied) {
				continue
			}
			return nil, false, fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
		}
		rel.revalidate = result.Revalidate
		rel.checkRead = result.CheckRead
		out = append(out, rel)
	}
	return out, more, nil
}

func (h handler) metadataBytes(r *http.Request, result source.Result) ([]byte, error) {
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil && h.blobs != nil:
		rc, _, err := h.blobs.Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return nil, fmt.Errorf("puppet: open cached metadata: %w", err)
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
		Kind: source.ResourceKindArtifact, Path: rel.meta.Path, Method: http.MethodGet,
		Variant:   "puppet-forge-v3-archive-v1",
		Checksums: types.Checksums{SHA256: rel.meta.SHA256},
		Validate: func(_ context.Context, body io.ReadSeeker, size int64) error {
			if size != rel.meta.Size || size <= 0 || size > puppetfmt.MaxArchiveBytes {
				return source.ErrInvalidResult
			}
			data, err := io.ReadAll(io.LimitReader(body, puppetfmt.MaxArchiveBytes+1))
			if err != nil || len(data) > puppetfmt.MaxArchiveBytes {
				return source.ErrInvalidResult
			}
			{
				actual := sha256.Sum256(data)
				if hex.EncodeToString(actual[:]) != rel.meta.SHA256 {
					return source.ErrInvalidResult
				}
			}
			meta, err := puppetfmt.ReadArchive(bytes.NewReader(data), int64(len(data)))
			if err != nil || meta.Name != rel.meta.Name || meta.Version != rel.meta.Version ||
				!reflect.DeepEqual(meta.Manifest, rel.meta.Manifest) {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	resource.ExternalURL = rel.remoteURL
	resource.NoCrossOriginAuth = true
	resource.CheckURL = func(_ context.Context, target *url.URL) error {
		return checkRegistryURL(pkgroute.RequestFrom(member).Repo, target)
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

// withinRegistry constrains metadata and archives to the configured upstream root.
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

func archiveWithinFeed(base, target *url.URL) bool {
	if !validArchiveURL(target) || target.Scheme != base.Scheme || target.Host != base.Host {
		return false
	}
	root := strings.TrimSuffix(base.Path, "/v3/releases") + "/"
	if !strings.HasPrefix(target.Path, root) {
		return false
	}
	for _, segment := range strings.Split(target.Path, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	return true
}

func checkRegistryURL(c repo.Config, target *url.URL) error {
	if target == nil {
		return source.ErrInvalidRequest
	}
	if _, _, ok := withinRegistry(c, target.String()); !ok {
		return source.ErrDenied
	}
	return nil
}
