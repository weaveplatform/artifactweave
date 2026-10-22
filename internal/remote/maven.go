package remote

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MavenBackend opts only Maven-family routes into format-aware source retrieval.
// Other adapters keep the shared policy's existing behavior.
type MavenBackend struct {
	Policy *Policy
	Eager  func(context.Context, source.Request, source.Permit)
}

func (m *MavenBackend) Read(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (source.Result, error) {
	if !maven.Serves(q.Repo.PackageType) {
		return m.Policy.Read(ctx, q, permit)
	}
	prepared, err := m.prepare(ctx, q)
	if err != nil {
		return source.Result{}, err
	}
	result, err := m.Policy.Read(ctx, prepared, permit)
	if err == nil && result.Item != nil && m.Eager != nil && q.Resource.Method == http.MethodGet &&
		permit.UpstreamNetworkAllowed &&
		!enabled(q.Repo.Offline) &&
		(q.Repo.StoreArtifactsLocally == nil || *q.Repo.StoreArtifactsLocally) {
		m.Eager(ctx, q, permit)
	}
	return result, err
}

func (m *MavenBackend) prepare(ctx context.Context, q source.Request) (source.Request, error) {
	local, err := m.Policy.Items.Layout(ctx, cmp.Or(q.Repo.RepoLayoutRef, repo.LayoutMaven2))
	if err != nil {
		return q, source.ErrInvalidRequest
	}
	r := &q.Resource
	r.Variant = "maven-source-v1"
	if maven.IsMetadata(r.Path) {
		r.Kind = source.Metadata
		r.Accept = "application/xml"
		r.EscapedPath = (&url.URL{Path: r.Path}).EscapedPath()
		r.ExpectedContentType = "application/xml"
	} else {
		r.Mutable = maven.IsNonUniqueSnapshot(r.Path)
	}
	if q.Repo.RemoteRepoLayoutRef != "" && q.Repo.RemoteRepoLayoutRef != q.Repo.RepoLayoutRef {
		upstream, err := m.Policy.Items.Layout(ctx, q.Repo.RemoteRepoLayoutRef)
		if err != nil {
			return q, source.ErrInvalidRequest
		}
		mapped, err := mavenUpstreamPath(local, upstream, r.Path)
		if err != nil {
			return q, source.ErrInvalidRequest
		}
		r.UpstreamPath = (&url.URL{Path: mapped}).EscapedPath()
	}
	canonical := r.Path
	r.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := maven.ValidateRemote(ctx, q.Repo, local, canonical, body, size); err != nil {
			return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.IntegrityFailure}
		}
		return nil
	}
	return q, nil
}

// Equivalent layout definitions preserve metadata paths; otherwise metadata has
// no lossless artifact-coordinate translation and fails before any network call.
func mavenUpstreamPath(local, upstream *layout.Layout, p string) (string, error) {
	a, b := local.Definition(), upstream.Definition()
	a.Name, b.Name = "", ""
	if a == b {
		return p, nil
	}
	mapped, err := layout.Translate(local, upstream, p)
	if err != nil {
		return "", fmt.Errorf("translate Maven source path: %w", err)
	}
	return mapped, nil
}

// PrepareUpload retrieves optional external checksum files only for a newly
// fetched representation. Warm cache hits need no sidecar requests; pass-thru
// additionally preserves each available upstream digest. Every auxiliary call uses the same guarded transport and deadline.
func (m *MavenBackend) PrepareUpload(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	u *item.CacheUpload,
) error {
	if !maven.Serves(q.Repo.PackageType) ||
		q.Repo.RemoteRepoChecksumPolicyType == repo.IgnoreAndGenerate ||
		u.Declared != (types.Checksums{}) && q.Repo.RemoteRepoChecksumPolicyType != repo.PassThru {
		return nil
	}
	client, err := m.Policy.Pool.Client(q.Repo, opts)
	if err != nil {
		return policyError(err)
	}
	request := transportRequest(q, types.RemoteCacheEntry{})
	request.Method, request.Metadata = http.MethodGet, true
	request.Header = nil
	algorithms := []item.Algorithm{item.SHA1, item.MD5}
	if q.Repo.RemoteRepoChecksumPolicyType == repo.PassThru {
		algorithms = append(algorithms, item.SHA256, item.SHA512)
	}
	for _, alg := range algorithms {
		if alg.Of(u.Declared) != "" {
			continue
		}
		checksumRequest := request
		checksumRequest.Path += "." + string(alg)
		res, err := client.Do(ctx, checksumRequest)
		if err != nil {
			return policyError(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, 1025))
		_ = res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			continue
		}
		if res.StatusCode != http.StatusOK {
			return responseError(res, m.Policy.now())
		}
		if readErr != nil || len(data) > 1024 {
			return item.ErrBadChecksum
		}
		value := strings.Fields(string(data))
		if len(value) == 0 {
			return item.ErrBadChecksum
		}
		declared := types.Checksums{}
		switch alg {
		case item.SHA1:
			declared.SHA1 = value[0]
		case item.MD5:
			declared.MD5 = value[0]
		case item.SHA256:
			declared.SHA256 = value[0]
		case item.SHA512:
			declared.SHA512 = value[0]
		}
		parsed, err := item.ParseChecksums(declared)
		if err != nil {
			return fmt.Errorf("maven source checksum: %w", err)
		}
		switch alg {
		case item.SHA1:
			u.Declared.SHA1 = parsed.SHA1
		case item.MD5:
			u.Declared.MD5 = parsed.MD5
		case item.SHA256:
			u.Declared.SHA256 = parsed.SHA256
		case item.SHA512:
			u.Declared.SHA512 = parsed.SHA512
		}
		if q.Repo.RemoteRepoChecksumPolicyType != repo.PassThru {
			break
		}
	}
	return m.Policy.check(ctx, q, permit, opts.Revision)
}

// MavenMetadata reads source metadata for both token resolution and the #213
// upstream-version consumer. It never populates a binary to discover a version.
func MavenMetadata(
	ctx context.Context,
	service *source.Service,
	q source.Request,
	metadataPath string,
) (maven.SourceMetadata, error) {
	q.Resource = source.Resource{Kind: source.Metadata, Path: metadataPath, Method: http.MethodGet}
	result, err := service.Read(ctx, q)
	if err != nil {
		return maven.SourceMetadata{}, fmt.Errorf("maven source metadata: %w", err)
	}
	if result.Stream == nil {
		return maven.SourceMetadata{}, source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	metadata, err := maven.ReadSourceMetadata(result.Stream.Body)
	if err != nil {
		return metadata, fmt.Errorf("decode Maven source metadata: %w", err)
	}
	if result.Revalidate != nil {
		if err = result.Revalidate(ctx); err != nil {
			return maven.SourceMetadata{}, fmt.Errorf("maven metadata delivery: %w", err)
		}
	}
	return metadata, nil
}

// ResolveMavenSource uses authoritative metadata for Maven's changing version
// names. The caller must authorize the returned canonical path before fetching.
func ResolveMavenSource(
	ctx context.Context,
	service *source.Service,
	items *item.Service,
	q source.Request,
) (string, error) {
	p := q.Resource.Path
	if !maven.IsNonUniqueSnapshot(p) && !strings.Contains(p, "[RELEASE]") &&
		!strings.Contains(p, "[LATEST]") {
		return p, nil
	}
	l, err := items.Layout(ctx, cmp.Or(q.Repo.RepoLayoutRef, repo.LayoutMaven2))
	if err != nil {
		return p, fmt.Errorf("resolve Maven layout: %w", err)
	}
	if !maven.IsMavenLayout(l) {
		return p, nil
	}
	if maven.IsNonUniqueSnapshot(p) {
		metadata, err := MavenMetadata(
			ctx,
			service,
			q,
			path.Join(path.Dir(p), "maven-metadata.xml"),
		)
		if errors.Is(err, store.ErrNotFound) {
			return p, nil
		}
		if err != nil {
			return "", err
		}
		co, ok := maven.Read(l, p)
		if !ok {
			return p, nil
		}
		for _, snapshot := range metadata.Versioning.Snapshots {
			if snapshot.Extension == co.Ext && snapshot.Classifier == co.Classifier &&
				validVersion(snapshot.Value) {
				return path.Join(
					path.Dir(p),
					strings.Replace(path.Base(p), co.Version, snapshot.Value, 1),
				), nil
			}
		}
		snapshot := metadata.Versioning.Snapshot
		if snapshot.BuildNumber > 0 {
			if _, err := time.Parse("20060102.150405", snapshot.Timestamp); err == nil {
				version := fmt.Sprintf(
					"%s-%s-%d",
					co.BaseRev,
					snapshot.Timestamp,
					snapshot.BuildNumber,
				)
				return path.Join(
					path.Dir(p),
					strings.Replace(path.Base(p), co.Version, version, 1),
				), nil
			}
		}
		return p, nil
	}
	dir := path.Dir(path.Dir(p))
	metadata, err := MavenMetadata(ctx, service, q, path.Join(dir, "maven-metadata.xml"))
	if err != nil {
		return "", err
	}
	token, version := "[RELEASE]", metadata.Versioning.Release
	if strings.Contains(p, "[LATEST]") {
		token, version = "[LATEST]", metadata.Versioning.Latest
	}
	if !validVersion(version) {
		return "", store.ErrNotFound
	}
	resolved := strings.ReplaceAll(p, token, version)
	if maven.IsNonUniqueSnapshot(resolved) {
		q.Resource.Path = resolved
		return ResolveMavenSource(ctx, service, items, q)
	}
	return resolved, nil
}

func validVersion(version string) bool {
	return version != "" && version != "." && version != ".." &&
		!strings.ContainsAny(version, "/\\\\\r\n;[]")
}
