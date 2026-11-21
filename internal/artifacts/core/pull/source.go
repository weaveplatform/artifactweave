package pull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
)

// Source binds the artifacts API to runtime-owned P9 services. Open refreshes
// caller/configuration under the project guard without network I/O. Peer opens
// the closed storage adapter, not another transport or administrator credential.
type Source struct {
	Content *item.Service
	InTx    func(context.Context) bool
	Open    func(context.Context, string, string) (*source.Service, source.Request, error)
	Peer    func(context.Context, *source.Service, source.Request, string, remote.DiscoveryScope) (Peer, error)
	Read    func(context.Context, source.Request) (source.Result, error)
}

var _ api.PullSource = Source{}

type selectionState struct {
	repository api.PullRepository
	request    source.Request
	peer       Peer
	mapping    *remote.Discovery
	mapped     bool
	include    antpath.Pattern
	exclude    *antpath.Pattern
}

func validSelection(selection api.PullSelection) bool {
	for _, value := range []string{selection.Root, selection.Include, selection.Exclude} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return false
		}
	}
	if selection.Root == "" {
		return true
	}
	_, _, _, err := kernel.SplitItemPath(selection.Root)
	return err == nil && selection.Root == strings.Trim(selection.Root, "/") && selection.Root != "."
}

func fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode private pull snapshot: %w", kernel.ErrInvalid)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s Source) prepare(ctx context.Context, key string, selection api.PullSelection) (selectionState, error) {
	if s.Content == nil || s.Content.Store == nil || s.Open == nil || s.Peer == nil || s.InTx == nil {
		return selectionState{}, fmt.Errorf("remote pull contracts unavailable: %w", kernel.ErrStore)
	}
	if !validSelection(selection) {
		return selectionState{}, fmt.Errorf("remote pull selection: %w", kernel.ErrInvalid)
	}
	service, q, err := s.Open(ctx, key, selection.Root)
	if err != nil {
		return selectionState{}, fmt.Errorf("open owning remote for pull: %w", err)
	}
	if service == nil || q.Repo.Key != key || q.Repo.Class != repo.Remote || q.CacheOnly {
		return selectionState{}, source.ErrInvalidRequest
	}
	if q.Repo.StoreArtifactsLocally != nil && !*q.Repo.StoreArtifactsLocally {
		return selectionState{}, api.ErrPullNotStored
	}
	if q.Repo.Offline != nil && *q.Repo.Offline {
		return selectionState{}, api.ErrPullOffline
	}
	state := selectionState{request: q, include: antpath.CompilePattern(selection.Include)}
	if selection.Exclude != "" {
		exclude := antpath.CompilePattern(selection.Exclude)
		state.exclude = &exclude
	}
	root, scope, layoutRevision := selection.Root, remote.DiscoveryScope{Path: selection.Root}, "identity-paths-v1"
	if maven.Serves(q.Repo.PackageType) {
		state.mapping, err = remote.NewDiscovery(ctx, s.Content, q.Repo)
		if err != nil {
			return selectionState{}, fmt.Errorf("prepare pull layout: %w", err)
		}
		if q.Repo.RemoteRepoLayoutRef != "" && q.Repo.RemoteRepoLayoutRef != q.Repo.RepoLayoutRef {
			// A canonical directory may not identify an upstream directory.
			// Root scanning requires actual root Read and each returned file is
			// separately mapped and authorized in its canonical selection.
			state.mapped, root, scope = true, "", remote.DiscoveryScope{Root: true}
		}
		resource, err := state.mapping.MetadataResource(scope, ".pull-selection", "pull-selection-v1")
		if err != nil {
			return selectionState{}, fmt.Errorf("bind pull layout: %w", err)
		}
		layoutRevision = resource.Variant
	}
	state.peer, err = s.Peer(ctx, service, state.request, root, scope)
	if err != nil {
		return selectionState{}, fmt.Errorf("bind pull metadata reader: %w", err)
	}
	if state.peer == nil || state.peer.Binding() == "" {
		return selectionState{}, source.ErrUnavailable
	}
	if service.Resolve == nil {
		return selectionState{}, source.ErrUnavailable
	}
	identity, err := service.Resolve(ctx, q.Repo)
	if err != nil {
		return selectionState{}, fmt.Errorf("resolve selected pull source: %w", err)
	}
	state.repository = api.PullRepository{Repository: api.TransferRepository{
		Key: key, Incarnation: string(identity.Repository), Generation: string(identity.Generation), PackageType: q.Repo.PackageType,
	}}
	state.repository.Revision, err = fingerprint([]any{state.repository.Repository, state.peer.Binding(), layoutRevision, selection})
	if err != nil {
		return selectionState{}, err
	}
	return state, state.current(ctx)
}

func (s selectionState) current(ctx context.Context) error {
	if s.mapping != nil {
		if err := s.mapping.Revalidate(ctx); err != nil {
			return fmt.Errorf("current pull layout: %w", err)
		}
	}
	if err := s.peer.Revalidate(ctx); err != nil {
		return fmt.Errorf("current pull peer: %w", err)
	}
	return nil
}

// Repository pins descriptive source/layout/credential revisions without
// making a peer request. A saved binding never bypasses a later prepare call.
func (s Source) Repository(ctx context.Context, key string, selection api.PullSelection) (api.PullRepository, error) {
	state, err := s.prepare(ctx, key, selection)
	if err != nil {
		return api.PullRepository{}, err
	}
	return state.repository, nil
}

// Items returns one bounded upstream page filtered under current canonical
// authority. No caller metadata transaction is allowed to span discovery.
func (s Source) Items(ctx context.Context, repository api.PullRepository, selection api.PullSelection, page kernel.Page) ([]api.PullItem, string, error) {
	if s.InTx == nil || s.InTx(ctx) {
		return nil, "", fmt.Errorf("pull enumeration requires an external context: %w", kernel.ErrInvalid)
	}
	page, err := selectionPage(repository.Revision, page)
	if err != nil {
		return nil, "", err
	}
	state, err := s.prepare(ctx, repository.Repository.Key, selection)
	if err != nil {
		return nil, "", err
	}
	if state.repository != repository {
		return nil, "", source.ErrSuperseded
	}
	entries, next, err := state.peer.Files(ctx, page)
	if err != nil {
		return nil, "", fmt.Errorf("enumerate pull selection: %w", err)
	}
	if len(entries) > page.Limit {
		return nil, "", source.ErrInvalidResult
	}
	items := make([]api.PullItem, 0, len(entries))
	for _, entry := range entries {
		selected, visible, err := state.selected(ctx, repository, selection, entry)
		if err != nil {
			return nil, "", err
		}
		if visible {
			items = append(items, selected)
		}
	}
	if err = state.current(ctx); err != nil {
		return nil, "", err
	}
	return items, selectionNext(repository.Revision, next), nil
}

func (s selectionState) selected(ctx context.Context, repository api.PullRepository, selection api.PullSelection, entry Entry) (api.PullItem, bool, error) {
	path := entry.Path
	if entry.Folder {
		return api.PullItem{}, false, source.ErrInvalidResult
	}
	if s.mapped {
		mapped, ok, err := s.mapping.MapFile(path)
		if err != nil {
			return api.PullItem{}, false, fmt.Errorf("map selected pull path: %w", err)
		}
		if !ok {
			// Do not advance a resumable cursor past an addressed metadata file
			// which cannot yet be translated without losing its coordinates.
			return api.PullItem{}, false, source.ErrUnsupported
		}
		path = mapped
	}
	if selection.Root != "" && path != selection.Root && !strings.HasPrefix(path, selection.Root+"/") ||
		!s.include.Match(path) || s.exclude != nil && s.exclude.Match(path) ||
		!repo.PatternsOf(s.request.Repo).Accepts(path, false) {
		return api.PullItem{}, false, nil
	}
	if err := s.peer.CheckRead(ctx, path); err != nil {
		if errors.Is(err, source.ErrDenied) {
			return api.PullItem{}, false, nil
		}
		return api.PullItem{}, false, fmt.Errorf("authorize selected pull path: %w", err)
	}
	selected := api.PullItem{
		Repository: repository, Selection: selection, Path: path, UpstreamPath: entry.Path,
		Size: entry.Size, Actual: entry.Checksums, Modified: entry.Modified, Updated: entry.Updated,
	}
	if selection.Properties {
		properties, err := s.peer.Properties(ctx, entry.Path)
		if err != nil {
			return api.PullItem{}, false, fmt.Errorf("read selected pull properties: %w", err)
		}
		selected.Properties = map[string][]string(properties.Clone())
		for key, values := range selected.Properties {
			slices.Sort(values)
			selected.Properties[key] = slices.Compact(values)
		}
		current, err := s.peer.Inspect(ctx, entry.Path)
		if err != nil || current != entry {
			return api.PullItem{}, false, source.ErrSuperseded
		}
	}
	version, err := fingerprint(selected)
	if err != nil {
		return api.PullItem{}, false, err
	}
	selected.Version = version
	return selected, true, nil
}
