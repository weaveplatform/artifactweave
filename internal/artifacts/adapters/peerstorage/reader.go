package peerstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

var _ pull.Peer = (*Reader)(nil)

// Reader retains one current source and canonical authority scope. It never
// persists management documents in the remote's artifact cache.
type Reader struct {
	service   *source.Service
	request   source.Request
	peer      remote.SmartPeer
	root      string
	authority *source.Authority
	binding   string
}

// Binding returns the private source/options/scope fingerprint, never raw
// credential material. Operations still require current authority checks.
func (r *Reader) Binding() string { return r.binding }

// New binds the configured upstream root to the owner's canonical scope. The
// caller derives scope from its layout; an unrepresentable folder needs actual
// root Read. No-store and cache aliases cannot initiate a cache-populating pull.
func New(ctx context.Context, service *source.Service, request source.Request,
	root string, scope remote.DiscoveryScope,
) (*Reader, error) {
	if service == nil || request.Repo.Class != repo.Remote || request.CacheOnly ||
		strings.HasSuffix(request.Repo.Key, model.CacheSuffix) ||
		request.Repo.StoreArtifactsLocally != nil && !*request.Repo.StoreArtifactsLocally {
		return nil, source.ErrInvalidRequest
	}
	peer, err := remote.ParseSmartPeer(request.Repo)
	if err != nil {
		return nil, fmt.Errorf("identify pull peer: %w", err)
	}
	if _, err = peer.StorageURL(root, nil); err != nil {
		return nil, fmt.Errorf("address pull root: %w", err)
	}
	request.Resource = source.Resource{
		Kind: source.ResourceKindMetadata, Method: http.MethodGet,
		Path: ".replication-storage", Variant: "replication-storage-v1",
		PermissionFolder: !scope.Root, PermissionPath: scope.Path, PermissionRoot: scope.Root,
		BlackoutOnly: true, NoCacheFile: true, Transient: true, PullStorage: true,
	}
	request, err = metadataRequest(request, peer, root, nil, func(ctx context.Context, body io.Reader) error {
		_, err := decodeInfo(ctx, body, peer.Repository, root)
		return err
	})
	if err != nil {
		return nil, err
	}
	authority, err := service.Authorize(remote.WithSmart(ctx, false), request)
	if err != nil {
		return nil, fmt.Errorf("authorize pull discovery: %w", err)
	}
	// Options revisions may incorporate credentials. Only their hash can be
	// carried by a local cursor or durable progress record.
	encoded, err := json.Marshal([]any{authority.Identity(), authority.Revision(), peer.BaseURL, peer.Repository, root, scope})
	if err != nil {
		return nil, source.ErrInvalidRequest
	}
	binding := sha256.Sum256(encoded)
	return &Reader{
		service: service, request: request, peer: peer, root: root,
		authority: authority, binding: hex.EncodeToString(binding[:]),
	}, nil
}

// Revalidate checks source, credentials, caller and repository authority without
// opening a network connection. It is required before consuming a saved page.
func (r *Reader) Revalidate(ctx context.Context) error {
	if err := r.authority.Revalidate(remote.WithSmart(ctx, false)); err != nil {
		return fmt.Errorf("revalidate pull discovery: %w", err)
	}
	return nil
}

// CheckRead authorizes one canonical descendant against the bound scope and
// current caller/source policy. Upstream traversal names grant no file access.
func (r *Reader) CheckRead(ctx context.Context, path string) error {
	if err := r.authority.CheckRead(remote.WithSmart(ctx, false), path); err != nil {
		return fmt.Errorf("authorize canonical pull file: %w", err)
	}
	return nil
}

func (r *Reader) read(ctx context.Context, path string, query url.Values,
	decode func(context.Context, io.Reader) error,
) error {
	if !within(r.root, path) {
		return source.ErrInvalidRequest
	}
	if err := r.Revalidate(ctx); err != nil {
		return err
	}
	q, err := metadataRequest(r.request, r.peer, path, query, decode)
	if err != nil {
		return err
	}
	result, err := r.service.Read(remote.WithSmart(ctx, false), q)
	if err != nil {
		return fmt.Errorf("read pull metadata: %w", err)
	}
	if result.Stream == nil || result.Stream.Body == nil {
		return source.ErrInvalidResult
	}
	defer result.Stream.Body.Close()
	if result.Stream.Status != http.StatusOK {
		return invalid()
	}
	if err = decode(ctx, result.Stream.Body); err != nil {
		return err
	}
	return r.Revalidate(ctx)
}

func metadataRequest(q source.Request, peer remote.SmartPeer, path string, query url.Values,
	decode func(context.Context, io.Reader) error,
) (source.Request, error) {
	target, err := peer.StorageURL(path, query)
	if err != nil {
		return source.Request{}, fmt.Errorf("address pull metadata: %w", err)
	}
	q.Resource.ExternalURL = target
	q.Resource.CheckURL = func(_ context.Context, candidate *url.URL) error {
		// The configured peer's management operation must not redirect to a
		// different repository, path, query or credential-bearing authority.
		if candidate.String() != target {
			return remote.ErrSecurity
		}
		return nil
	}
	q.Resource.ExpectedContentTypes = []string{
		"application/json",
		"application/vnd.org.jfrog.artifactory.storage.fileinfo+json",
		"application/vnd.org.jfrog.artifactory.storage.folderinfo+json",
		"application/vnd.org.jfrog.artifactory.storage.filelist+json",
		"application/vnd.org.jfrog.artifactory.storage.itemproperties+json",
	}
	q.Resource.ValidateMetadata = func(ctx context.Context, body io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
		return decode(ctx, body)
	}
	return q, nil
}

func within(root, path string) bool {
	return root == "" || path == root || strings.HasPrefix(path, root+"/")
}

func invalid() error {
	return &source.HTTPError{Status: http.StatusBadGateway, Kind: source.FailureKindIntegrity}
}
