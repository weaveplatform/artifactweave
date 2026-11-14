package maven

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

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// MavenBackend opts only Maven-family routes into format-aware source retrieval.
// Other adapters keep the shared policy's existing behavior.
type MavenBackend struct {
	Policy *remote.Policy
	Eager  func(context.Context, source.Request, source.Permit)
}

// Read answers q through the shared policy. For a Maven-family repository
// it first prepares the request: maven-metadata.xml is fetched as metadata
// and a non-unique snapshot as mutable, the path is mapped to the
// upstream's layout when the repository names another, and the body must
// pass ValidateRemote before it is cached, or the read fails with a 502
// integrity failure. After a GET or HEAD that answered with the cached
// file, while the upstream may be reached, it hands the read to Eager to
// queue the file's companion. Other package types, and metadata a protocol
// adapter has already prepared, go to the policy unchanged.
func (m *MavenBackend) Read(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (source.Result, error) {
	if !Serves(q.Repo.PackageType) || preparedMetadata(q.Resource) {
		//nolint:wrapcheck // preserves upstream error identity
		return m.Policy.Read(ctx, q, permit)
	}
	prepared, err := m.prepare(ctx, q)
	if err != nil {
		return source.Result{}, err
	}
	result, err := m.Policy.Read(ctx, prepared, permit)
	// A HEAD schedules as a GET does: on a remote that stores artifacts
	// locally a cold HEAD downloads the file (#1391), and 7.161.26's
	// RemoteRepoBase.doDownloadAndSave runs beforeResourceDownload, which
	// starts the eager fetch, before every download whatever the method.
	// A checksum request's cold read keeps nothing and returns no item, so
	// it schedules nothing, as 7.x's respondForChecksumRequest never
	// downloads the file.
	if err == nil && result.Item != nil && m.Eager != nil &&
		permit.UpstreamNetworkAllowed &&
		!enabled(q.Repo.Offline) &&
		(q.Repo.StoreArtifactsLocally == nil || *q.Repo.StoreArtifactsLocally) {
		m.Eager(ctx, q, permit)
	}
	//nolint:wrapcheck // preserves upstream error identity
	return result, err
}

func (m *MavenBackend) prepare(ctx context.Context, q source.Request) (source.Request, error) {
	local, err := m.Policy.Items.Layout(ctx, cmp.Or(q.Repo.RepoLayoutRef, repo.LayoutMaven2))
	if err != nil {
		return q, source.ErrInvalidRequest
	}
	r := &q.Resource
	validationLayout, validationPath := local, r.Path
	r.Variant = "maven-source-v1"
	if IsMetadata(r.Path) {
		r.Kind = source.ResourceKindMetadata
		r.Accept = "application/xml"
		r.EscapedPath = (&url.URL{Path: r.Path}).EscapedPath()
		r.ExpectedContentType = "application/xml"
	} else {
		r.Mutable = IsNonUniqueSnapshot(r.Path)
	}
	if q.Repo.RemoteRepoLayoutRef != "" && q.Repo.RemoteRepoLayoutRef != q.Repo.RepoLayoutRef {
		upstream, err := m.Policy.Items.Layout(ctx, q.Repo.RemoteRepoLayoutRef)
		if err != nil {
			return q, source.ErrInvalidRequest
		}
		// A checksum file is the file it names, mapped, with its extension,
		// as 7.161.26's HttpRepo.downloadResource translates the path it
		// downloads (convertRequestPathIfNeeded).
		file, ext := r.Path, ""
		if r.ChecksumOnly {
			subject, ok := source.ChecksumSubject(r.Path)
			if !ok {
				return q, source.ErrInvalidRequest
			}
			file, ext = subject, strings.TrimPrefix(r.Path, subject)
		}
		mapped, err := mavenUpstreamPath(local, upstream, file)
		if err != nil {
			return q, source.ErrInvalidRequest
		}
		r.UpstreamPath = (&url.URL{Path: mapped + ext}).EscapedPath()
		if IsPom(mapped) && !IsPom(r.Path) && !r.ChecksumOnly {
			// Descriptor projection can change .pom to ivy-*.xml. Preserve
			// the actual upstream POM's admission rules despite that spelling.
			validationLayout, validationPath = upstream, mapped
		}
	}
	r.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := ValidateRemote(
			ctx,
			q.Repo,
			validationLayout,
			validationPath,
			body,
			size,
		); err != nil {
			return &source.HTTPError{
				Status: http.StatusBadGateway,
				Kind:   source.FailureKindIntegrity,
			}
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
	mapped, err := layout.RemotePath(local, upstream, p)
	if err != nil {
		return "", fmt.Errorf("translate maven source path: %w", err)
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
	opts remote.Options,
	u *item.CacheUpload,
) error {
	if Serves(q.Repo.PackageType) && q.Resource.ChecksumOnly {
		// The upstream's checksum file is read as it is: 7.161.26's
		// getRemoteChecksumFromPath downloads it under no checksum policy
		// and asks for no checksum of it, so fail's demand that the
		// upstream declare one does not apply.
		u.Repo.RemoteRepoChecksumPolicyType = repo.IgnoreAndGenerate
		return nil
	}
	if !Serves(q.Repo.PackageType) || preparedMetadata(q.Resource) ||
		q.Repo.RemoteRepoChecksumPolicyType == repo.IgnoreAndGenerate ||
		u.Declared != (kernel.Checksums{}) && q.Repo.RemoteRepoChecksumPolicyType != repo.PassThru {
		return nil
	}
	conn, err := m.Policy.Pool.Connection(q.Repo, opts)
	if err != nil {
		return remote.PolicyError(err)
	}
	// A checksum file is asked for at its own path without the request's
	// matrix parameters, which TransportRequest would put after the
	// artifact's name: 7.x reads it with downloadResource(path) and a null
	// request context (RemoteRepoBase.getRemoteChecksumFromPath, 7.161.26).
	bare := q
	bare.MatrixParams = ""
	request := remote.TransportRequest(bare, model.RemoteCacheEntry{})
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
		res, err := conn.Do(ctx, checksumRequest)
		if err != nil {
			return remote.PolicyError(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, 1025))
		_ = res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			continue
		}
		if res.StatusCode != http.StatusOK {
			return remote.ResponseError(res, policyNow(m.Policy))
		}
		if readErr != nil || len(data) > 1024 {
			return item.ErrBadChecksum
		}
		value := strings.Fields(string(data))
		if len(value) == 0 {
			return item.ErrBadChecksum
		}
		declared := kernel.Checksums{}
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
	//nolint:wrapcheck // preserves upstream error identity
	return m.Policy.Check(ctx, q, permit, opts.Revision)
}

// A protocol adapter's bounded metadata admission is already complete. Treating
// a directory listing or peer-discovery response as Maven XML would replace its
// validator and ask the source for unrelated checksum sidecars.
func preparedMetadata(r source.Resource) bool {
	return r.Kind == source.ResourceKindMetadata && r.Variant != "" &&
		(r.ValidateMetadata != nil || r.RequireSmart && r.Validate != nil)
}

// MavenMetadata reads source metadata for both token resolution and the #213
// upstream-version consumer. It never populates a binary to discover a version.
func MavenMetadata(
	ctx context.Context,
	service *source.Service,
	q source.Request,
	metadataPath string,
) (SourceMetadata, error) {
	q.Resource = source.Resource{
		Kind:   source.ResourceKindMetadata,
		Path:   metadataPath,
		Method: http.MethodGet,
	}
	result, err := service.Read(ctx, q)
	if err != nil {
		return SourceMetadata{}, fmt.Errorf("maven source metadata: %w", err)
	}
	if result.Stream == nil {
		return SourceMetadata{}, source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	metadata, err := ReadSourceMetadata(result.Stream.Body)
	if err != nil {
		return metadata, fmt.Errorf("decode maven source metadata: %w", err)
	}
	if result.Revalidate != nil {
		if err = result.Revalidate(ctx); err != nil {
			return SourceMetadata{}, fmt.Errorf("maven metadata delivery: %w", err)
		}
	}
	return metadata, nil
}

// ResolveMavenSource uses authoritative metadata for Maven's changing version
// names. The caller must authorize the returned canonical path before fetching.
// Metadata the remote repository's own download policy refuses, as an
// exclude pattern of **/maven-metadata.xml does, counts as absent and the
// path is fetched as asked, so the file's own check decides: 6.20's
// RemoteLatestMavenVersionResolver reads it through the download service and
// takes any answer but 200 as no metadata (tryDownloadingMavenMetadata).
// 7.x's answer is to be captured (#216).
func ResolveMavenSource(
	ctx context.Context,
	service *source.Service,
	items *item.Service,
	q source.Request,
) (string, error) {
	p := q.Resource.Path
	if !IsNonUniqueSnapshot(p) && !strings.Contains(p, "[RELEASE]") &&
		!strings.Contains(p, "[LATEST]") {
		return p, nil
	}
	l, err := items.Layout(ctx, cmp.Or(q.Repo.RepoLayoutRef, repo.LayoutMaven2))
	if err != nil {
		return p, fmt.Errorf("resolve maven layout: %w", err)
	}
	if !IsMavenLayout(l) {
		return p, nil
	}
	if IsNonUniqueSnapshot(p) {
		metadata, err := MavenMetadata(
			ctx,
			service,
			q,
			path.Join(path.Dir(p), "maven-metadata.xml"),
		)
		if errors.Is(err, kernel.ErrNotFound) || refused(err) {
			return p, nil
		}
		if err != nil {
			return "", err
		}
		co, ok := Read(l, p)
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
	if refused(err) {
		// The token path is fetched as asked, as 6.20 does. Missing metadata
		// answers 404 without that fetch instead: 6.20 would ask the upstream
		// there too, a difference the download policy does not decide.
		return p, nil
	}
	if err != nil {
		return "", err
	}
	token, version := "[RELEASE]", metadata.Versioning.Release
	if strings.Contains(p, "[LATEST]") {
		token, version = "[LATEST]", metadata.Versioning.Latest
	}
	if !validVersion(version) {
		return "", kernel.ErrNotFound
	}
	resolved := strings.ReplaceAll(p, token, version)
	if IsNonUniqueSnapshot(resolved) {
		q.Resource.Path = resolved
		return ResolveMavenSource(ctx, service, items, q)
	}
	return resolved, nil
}

// refused reports a read the download policy refused: a blackout or a
// pattern of the remote repository, which the source boundary answers before
// any upstream request.
func refused(err error) bool { return errors.As(err, new(*format.RejectionError)) }

func validVersion(version string) bool {
	return version != "" && version != "." && version != ".." &&
		!strings.ContainsAny(version, "/\\\\\r\n;[]")
}

// policyNow is the shared policy's clock as (*remote.Policy).now reads it,
// which this package cannot call: the injected Now first, else the wall
// clock, in UTC. It must stay in step with that method, or a Retry-After
// read here would be judged against another clock than the policy's own.
func policyNow(p *remote.Policy) time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// enabled reports a repository switch that is set and on, as core remote's
// own helper does; an unset switch is off.
func enabled(v *bool) bool { return v != nil && *v }
