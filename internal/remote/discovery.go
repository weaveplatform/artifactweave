package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Discovery retains an atomic layout/source snapshot. Upstream paths are
// traversal inputs only; consumers receive canonical paths after lossless
// translation and current authorization.
type Discovery struct {
	store       store.Store
	repository  repo.Config
	identity    types.SourceIdentity
	canonical   *layout.Layout
	upstream    *layout.Layout
	fingerprint string
}

// DiscoveryScope is a canonical query folder. Root requires actual root Read
// when the layout cannot encode the query's coordinates in a directory; a
// grant on an unrelated descendant cannot initiate that scan.
type DiscoveryScope struct {
	Path string
	Root bool
}

func NewDiscovery(ctx context.Context, items *item.Service, c repo.Config) (*Discovery, error) {
	if items == nil || items.Store == nil || c.Class != repo.Remote {
		return nil, source.ErrInvalidRequest
	}
	d := &Discovery{store: items.Store, repository: c}
	err := items.Store.Tx(ctx, func(tx context.Context) error {
		current, err := items.Store.Repos().Get(tx, c.Key)
		if err != nil || store.RemotePolicyChanged(c, current) {
			return source.ErrSuperseded
		}
		d.identity, err = items.Store.RemoteCache().Source(tx, c.Key)
		if err != nil {
			return fmt.Errorf("read discovery source: %w", err)
		}
		d.canonical, err = items.Layout(tx, c.RepoLayoutRef)
		if err != nil {
			return source.ErrInvalidRequest
		}
		upstream := c.RemoteRepoLayoutRef
		if upstream == "" {
			upstream = c.RepoLayoutRef
		}
		d.upstream, err = items.Layout(tx, upstream)
		if err != nil {
			return source.ErrInvalidRequest
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("prepare mapped discovery: %w", err)
	}
	var definitions [][]string
	for _, l := range []*layout.Layout{d.canonical, d.upstream} {
		def := l.Definition()
		definitions = append(definitions, []string{
			def.Name, def.ArtifactPathPattern,
			strconv.FormatBool(def.DistinctiveDescriptorPathPattern), def.DescriptorPathPattern,
			def.FolderIntegrationRevisionRegExp, def.FileIntegrationRevisionRegExp,
		})
	}
	data, _ := json.Marshal(definitions)
	hash := sha256.Sum256(data)
	d.fingerprint = hex.EncodeToString(hash[:])
	return d, nil
}

func (d *Discovery) Canonical() *layout.Layout { return d.canonical }
func (d *Discovery) Upstream() *layout.Layout  { return d.upstream }

// Scopes derives authority from complete canonical directory components. A
// constraint present only in a filename cannot borrow a sibling file's grant.
func (d *Discovery) Scopes(values layout.Values) []DiscoveryScope {
	var out []DiscoveryScope
	for _, prefix := range d.canonical.FolderPrefixes(values) {
		scope := DiscoveryScope{Path: prefix.Path}
		for token, value := range values {
			if value != "" && prefix.Bound[token] != value {
				scope = DiscoveryScope{Root: true}
				break
			}
		}
		if scope.Path == "" {
			scope.Root = true
		}
		if !slices.Contains(out, scope) {
			out = append(out, scope)
		}
	}
	return out
}

// VersionScope proves a version-specific artifact directory without guessing
// an extension, classifier or POM. Filename-only versions need actual files.
func (d *Discovery) VersionScope(group, module, version string) (DiscoveryScope, bool) {
	values := layout.Values{
		layout.TokenOrg: group, layout.TokenModule: module, layout.TokenBaseRev: version,
		layout.TokenFolderItegRev: "", layout.TokenFileItegRev: "",
	}
	if strings.HasSuffix(version, "-SNAPSHOT") {
		values[layout.TokenBaseRev] = strings.TrimSuffix(version, "-SNAPSHOT")
		values[layout.TokenFolderItegRev], values[layout.TokenFileItegRev] = "SNAPSHOT", "SNAPSHOT"
	}
	prefix := d.canonical.FolderPrefixes(values)[0]
	for _, token := range []string{layout.TokenOrg, layout.TokenModule, layout.TokenBaseRev, layout.TokenFolderItegRev} {
		if prefix.Bound[token] != values[token] {
			return DiscoveryScope{}, false
		}
	}
	return DiscoveryScope{Path: prefix.Path}, prefix.Path != ""
}

func (s DiscoveryScope) Allows(q source.Request) bool {
	if q.Access == nil || !s.valid() {
		return false
	}
	allows := q.Access.AllowsFolder
	if s.Root {
		allows = q.Access.Allows
	}
	return authz.PatternsOf(q.Repo).Accepts(s.Path, true) && allows(types.ResourceArtifact,
		authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)}, s.Path, types.ActionRead)
}

func (s DiscoveryScope) valid() bool {
	if s.Root {
		return s.Path == ""
	}
	if s.Path == "" || s.Path != strings.Trim(s.Path, "/") {
		return false
	}
	_, _, _, err := types.SplitItemPath(s.Path)
	return err == nil
}

func (s DiscoveryScope) contains(full string) bool {
	return s.Path == "" || full == s.Path || strings.HasPrefix(full, s.Path+"/")
}

// MapFile refuses lossy or ambiguous coordinates instead of keeping the raw
// upstream path. Non-artifact metadata/auxiliary names are not file results.
func (d *Discovery) MapFile(upstream string) (string, bool, error) {
	if _, ok := d.upstream.Read(upstream); !ok {
		return "", false, nil
	}
	canonical, err := layout.Translate(d.upstream, d.canonical, upstream)
	if err != nil {
		return "", false, discoveryMappingError()
	}
	back, err := layout.RemotePath(d.canonical, d.upstream, canonical)
	if err != nil || back != upstream {
		return "", false, discoveryMappingError()
	}
	if _, _, _, err = types.SplitItemPath(canonical); err != nil {
		return "", false, discoveryMappingError()
	}
	return canonical, true, nil
}

func discoveryMappingError() error {
	return &source.HTTPError{Status: http.StatusNotImplemented, Kind: source.FailureKindProtocol}
}

func (d *Discovery) Revalidate(ctx context.Context) error {
	current, err := d.store.RemoteCache().Source(ctx, d.repository.Key)
	if err != nil || current != d.identity {
		return source.ErrSuperseded
	}
	return nil
}

// Read pins the identity before Service acquires the source. A layout edit
// between preparing the mapping and Service.Resolve cannot adopt a new source
// generation with old mapping rules, even before the first network request.
func (d *Discovery) Read(
	ctx context.Context,
	service *source.Service,
	q source.Request,
) (source.Result, error) {
	if service == nil || service.Resolve == nil || q.Repo.Key != d.repository.Key {
		return source.Result{}, source.ErrUnsupported
	}
	checked := *service
	checked.Resolve = func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
		id, err := service.Resolve(ctx, c)
		if err != nil {
			return id, fmt.Errorf("resolve mapped discovery: %w", err)
		}
		if id != d.identity {
			return id, source.ErrSuperseded
		}
		return id, nil
	}
	result, err := checked.Read(ctx, q)
	if err != nil {
		return result, fmt.Errorf("read mapped discovery: %w", err)
	}
	return result, nil
}

// MetadataResource separates the private cache/authority location from the
// upstream document. Callers must supply complete body validation before Read.
func (d *Discovery) MetadataResource(
	scope DiscoveryScope,
	upstream, variant string,
) (source.Resource, error) {
	if upstream != strings.Trim(upstream, "/") || !scope.valid() || variant == "" {
		return source.Resource{}, source.ErrInvalidRequest
	}
	if _, _, _, err := types.SplitItemPath(upstream); err != nil {
		return source.Resource{}, source.ErrInvalidRequest
	}
	hash := sha256.Sum256([]byte(upstream))
	r := source.Resource{
		Kind: source.ResourceKindMetadata, Method: http.MethodGet,
		Path:             path.Join(scope.Path, ".remote-discovery", hex.EncodeToString(hash[:])),
		PermissionFolder: !scope.Root, PermissionPath: scope.Path, PermissionRoot: scope.Root,
		UpstreamPath: (&url.URL{Path: upstream}).EscapedPath(),
		Variant:      "mapped-" + variant + "-" + d.fingerprint,
	}
	return r, nil
}
