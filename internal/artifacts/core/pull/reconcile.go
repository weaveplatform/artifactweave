package pull

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (s selectionState) selects(path string, selection api.PullSelection) bool {
	return (selection.Root == "" || path == selection.Root || strings.HasPrefix(path, selection.Root+"/")) &&
		s.include.Match(path) && (s.exclude == nil || !s.exclude.Match(path)) &&
		repo.PatternsOf(s.request.Repo).Accepts(path, false)
}

func (s Source) candidateState(ctx context.Context, candidate api.PullCacheCandidate) (selectionState, error) {
	_, _, _, pathErr := kernel.SplitItemPath(candidate.Path)
	if pathErr != nil || candidate.Path == "" || strings.Trim(candidate.Path, "/") != candidate.Path {
		return selectionState{}, kernel.ErrInvalid
	}
	state, err := s.prepare(ctx, candidate.Repository.Repository.Key, candidate.Selection)
	if err != nil {
		return selectionState{}, err
	}
	if state.repository != candidate.Repository {
		return selectionState{}, source.ErrSuperseded
	}
	if !state.selects(candidate.Path, candidate.Selection) {
		return selectionState{}, kernel.ErrForbidden
	}
	if err := state.peer.CheckRead(ctx, candidate.Path); err != nil {
		return selectionState{}, fmt.Errorf("current cache selection authority: %w", err)
	}
	return state, nil
}

type removalSnapshot struct {
	paths   []string
	version string
	rawRoot bool
}

func (s Source) removalSnapshot(ctx context.Context, state selectionState, path string) (removalSnapshot, error) {
	identity, err := s.Content.Store.RemoteCache().Source(ctx, state.request.Repo.Key)
	if err != nil {
		return removalSnapshot{}, fmt.Errorf("guard cache reconciliation source: %w", err)
	}
	if string(identity.Repository) != state.repository.Repository.Incarnation ||
		string(identity.Generation) != state.repository.Repository.Generation {
		return removalSnapshot{}, source.ErrSuperseded
	}
	paths := []string{path}
	companions, err := s.Content.SidecarsBeside(ctx, item.Target{Repo: state.request.Repo, Cached: true, Path: path}, path)
	if err != nil {
		return removalSnapshot{}, fmt.Errorf("read cache reconciliation companions: %w", err)
	}
	if len(companions) > 64 {
		return removalSnapshot{}, source.ErrInvalidResult
	}
	for _, companion := range companions {
		paths = append(paths, companion.From)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	snapshot := removalSnapshot{paths: paths}
	var facts []any
	positive := false
	for _, selected := range paths {
		it, err := s.Content.Store.Items().Get(ctx, state.request.Repo.Key, true, selected)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return removalSnapshot{}, fmt.Errorf("read cache reconciliation item: %w", err)
		}
		if it.Folder {
			return removalSnapshot{}, kernel.ErrInvalid
		}
		entries, next, err := s.Content.Store.RemoteCache().Exact(ctx, state.request.Repo.Key, selected, kernel.Page{Limit: 64})
		if err != nil {
			return removalSnapshot{}, fmt.Errorf("read cache reconciliation representations: %w", err)
		}
		if next != "" {
			return removalSnapshot{}, source.ErrInvalidResult
		}
		versions := make([]string, 0, len(entries))
		for _, entry := range entries {
			version, err := cacheVersion(entry)
			if err != nil {
				return removalSnapshot{}, err
			}
			versions = append(versions, version)
			// A fresh run can retire bytes from an earlier source generation in
			// this repository lifetime. Current upstream absence, Delete rights
			// and the complete unchanged cache snapshot still decide removal.
			if selected == path && entry.Token.Source.Repository == identity.Repository &&
				entry.Token.Source.Kind == identity.Kind && entry.Token.Source.ID == identity.ID && entry.Blob.Checksums.SHA256 != "" {
				positive = true
				snapshot.rawRoot = snapshot.rawRoot || entry.ItemID == 0
			}
		}
		facts = append(facts, []any{selected, it.ID, it.Size, it.Actual, it.Updated, it.PropertiesModified, versions})
	}
	if !positive {
		return removalSnapshot{}, source.ErrSuperseded
	}
	snapshot.version, err = fingerprint([]any{state.repository, facts})
	return snapshot, err
}

// CachedItems returns local reconciliation candidates, not upstream absence.
// Pages retain their continuation even when current scope filters every row.
func (s Source) CachedItems(ctx context.Context, repository api.PullRepository, selection api.PullSelection,
	page kernel.Page,
) ([]api.PullCacheCandidate, string, error) {
	if s.Content == nil || s.Content.Store == nil {
		return nil, "", kernel.ErrStore
	}
	page, err := selectionPage(repository.Revision+":cache-v1", page)
	if err != nil {
		return nil, "", err
	}
	var result []api.PullCacheCandidate
	var next string
	err = s.Content.Store.Tx(ctx, func(ctx context.Context) error {
		state, err := s.prepare(ctx, repository.Repository.Key, selection)
		if err != nil {
			return err
		}
		if state.repository != repository {
			return source.ErrSuperseded
		}
		entries, continuation, err := s.Content.Store.RemoteCache().List(ctx, repository.Repository.Key, selection.Root, page)
		if err != nil {
			return fmt.Errorf("enumerate cache reconciliation: %w", err)
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			path := entry.Token.Key.Path
			if seen[path] || entry.Blob.Checksums.SHA256 == "" || !state.selects(path, selection) {
				continue
			}
			seen[path] = true
			if err := state.peer.CheckRead(ctx, path); err != nil {
				if errors.Is(err, source.ErrDenied) {
					continue
				}
				return fmt.Errorf("authorize cache reconciliation path: %w", err)
			}
			snapshot, err := s.removalSnapshot(ctx, state, path)
			if err != nil {
				return err
			}
			result = append(result, api.PullCacheCandidate{Repository: repository, Selection: selection, Path: path, Version: snapshot.version})
		}
		next = selectionNext(repository.Revision+":cache-v1", continuation)
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("current cache reconciliation page: %w", err)
	}
	return result, next, nil
}

func deleteAuthority(ctx context.Context, state selectionState, selection api.PullSelection, paths []string) error {
	for _, path := range paths {
		if !state.selects(path, selection) || state.request.Access == nil ||
			!state.request.Access.Allows(accessapi.ResourceArtifact,
				accessapi.Repo{Key: state.request.Repo.Key, Class: string(repo.Remote)}, path, accessapi.ActionDelete) {
			return kernel.ErrForbidden
		}
		if err := state.peer.CheckRead(ctx, path); err != nil {
			return fmt.Errorf("current cache removal authority: %w", err)
		}
	}
	return nil
}

func upstreamRemovalPath(state selectionState, canonical string) (string, error) {
	if !state.mapped {
		return canonical, nil
	}
	upstream, err := layout.RemotePath(state.mapping.Canonical(), state.mapping.Upstream(), canonical)
	if err != nil {
		return "", source.ErrUnsupported
	}
	back, ok, err := state.mapping.MapFile(upstream)
	if err != nil || !ok || back != canonical {
		return "", source.ErrUnsupported
	}
	return upstream, nil
}

// Remove performs no upstream mutation. The namespace/absence observation is
// outside SQL; current config/caller/cache facts and final ownership are checked
// again in the local deletion/checkpoint transaction.
func (s Source) Remove(ctx context.Context, candidate api.PullCacheCandidate, publication api.PullPublication,
	record func(context.Context, []string) error,
) ([]string, error) {
	if s.InTx == nil || s.InTx(ctx) || publication.Check == nil || publication.Fence == nil || candidate.Version == "" {
		return nil, kernel.ErrInvalid
	}
	if err := publication.Check(ctx); err != nil {
		return nil, fmt.Errorf("current cache removal execution: %w", err)
	}
	state, err := s.candidateState(ctx, candidate)
	if err != nil {
		return nil, err
	}
	upstream, err := upstreamRemovalPath(state, candidate.Path)
	if err != nil {
		return nil, err
	}
	if err := deleteAuthority(ctx, state, candidate.Selection, []string{candidate.Path}); err != nil {
		return nil, err
	}
	absent, err := state.peer.Absent(ctx, upstream)
	if err != nil {
		return nil, fmt.Errorf("verify exact pull source absence: %w", err)
	}
	var removed []string
	err = s.Content.Store.Tx(ctx, func(ctx context.Context) error {
		if err := publication.Check(ctx); err != nil {
			return fmt.Errorf("current cache deletion publication: %w", err)
		}
		state, err := s.candidateState(ctx, candidate)
		if err != nil {
			return err
		}
		snapshot, err := s.removalSnapshot(ctx, state, candidate.Path)
		if err != nil {
			return err
		}
		if snapshot.version != candidate.Version {
			return source.ErrSuperseded
		}
		if !absent {
			// Presence is ordinary reconciliation, not changed source evidence.
			// Acknowledge only retention, after checking the selected cache is
			// still current; no item, index, blob or property is mutated.
			if err := deleteAuthority(ctx, state, candidate.Selection, []string{candidate.Path}); err != nil {
				return err
			}
			if record != nil {
				if err := record(ctx, nil); err != nil {
					return fmt.Errorf("record selected cache retention: %w", err)
				}
			}
			return publication.Fence(ctx) //nolint:wrapcheck // retained observations need the same final ownership fence
		}
		if err := deleteAuthority(ctx, state, candidate.Selection, snapshot.paths); err != nil {
			return err
		}
		result, err := s.Content.Delete(ctx, item.Target{
			Repo: state.request.Repo, Cached: true, Path: candidate.Path, ExactCache: true,
			CheckCurrent: func(ctx context.Context, path string, folder bool, _ accessapi.Actions) error {
				if folder {
					return kernel.ErrForbidden
				}
				return deleteAuthority(ctx, state, candidate.Selection, []string{path})
			},
		}, state.request.Subject.Name)
		if err != nil {
			return fmt.Errorf("remove selected cache file: %w", err)
		}
		if snapshot.rawRoot {
			removed = append(removed, candidate.Path)
		}
		for _, it := range result.Removed {
			if !it.Folder {
				removed = append(removed, it.FullPath())
			}
		}
		slices.Sort(removed)
		removed = slices.Compact(removed)
		if record != nil {
			if err := record(ctx, slices.Clone(removed)); err != nil {
				return fmt.Errorf("record selected cache removal: %w", err)
			}
		}
		return publication.Fence(ctx) //nolint:wrapcheck // last local execution fence
	})
	if err != nil {
		return nil, fmt.Errorf("publish selected cache removal: %w", err)
	}
	return removed, nil
}
