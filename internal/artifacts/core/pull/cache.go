package pull

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	platformtx "github.com/weaveplatform/artifactweave/internal/platform/tx"
)

func validItem(selected api.PullItem) bool {
	version := selected.Version
	selected.Version = ""
	actual, err := fingerprint(selected)
	_, _, _, pathErr := kernel.SplitItemPath(selected.Path)
	return err == nil && pathErr == nil && selected.Path == strings.Trim(selected.Path, "/") &&
		selected.Path != "" && selected.Size >= 0 && selected.Actual.SHA256 != "" && version == actual
}

func (s Source) selectedState(ctx context.Context, selected api.PullItem) (selectionState, error) {
	if !validItem(selected) {
		return selectionState{}, source.ErrInvalidRequest
	}
	state, err := s.prepare(ctx, selected.Repository.Repository.Key, selected.Selection)
	if err != nil {
		return selectionState{}, err
	}
	if state.repository != selected.Repository {
		return selectionState{}, source.ErrSuperseded
	}
	if err := state.peer.CheckRead(ctx, selected.Path); err != nil {
		return selectionState{}, fmt.Errorf("current pull file authority: %w", err)
	}
	if selected.Selection.Properties && (state.request.Access == nil || !state.request.Access.Allows(accessapi.ResourceArtifact,
		accessapi.Repo{Key: state.request.Repo.Key, Class: string(state.request.Repo.Class)}, selected.Path, accessapi.ActionAnnotate)) {
		return selectionState{}, kernel.ErrForbidden
	}
	return state, nil
}

func cacheVersion(entry model.RemoteCacheEntry) (string, error) {
	return fingerprint([]any{entry.Token, entry.ItemID, entry.Blob.Size, entry.Blob.Checksums})
}

func cached(selected api.PullItem, entry model.RemoteCacheEntry) (api.PullCached, error) {
	expectation := source.ContentExpectation{Size: selected.Size, Checksums: selected.Actual}
	if entry.Token.Key.RepoKey != selected.Repository.Repository.Key || entry.Token.Key.Path != selected.Path ||
		string(entry.Token.Source.Repository) != selected.Repository.Repository.Incarnation ||
		string(entry.Token.Source.Generation) != selected.Repository.Repository.Generation ||
		!expectation.Matches(entry.Blob.Size, entry.Blob.Checksums) {
		return api.PullCached{}, source.ErrSuperseded
	}
	representation, err := fingerprint(entry.Token.Key)
	if err != nil {
		return api.PullCached{}, err
	}
	version, err := cacheVersion(entry)
	if err != nil {
		return api.PullCached{}, err
	}
	return api.PullCached{Item: selected, Representation: representation, Version: version}, nil
}

// Fetch verifies a selected upstream snapshot, then uses the runtime's normal
// source and format admission. A crash after cache publication leaves reusable
// bytes but no false completed intent; the next attempt verifies the warm copy.
func (s Source) Fetch(ctx context.Context, selected api.PullItem, publication api.PullPublication) (api.PullCached, error) {
	if s.InTx == nil || s.InTx(ctx) || s.Read == nil || publication.Check == nil || publication.Fence == nil {
		return api.PullCached{}, fmt.Errorf("pull fetch needs external context and execution authority: %w", kernel.ErrInvalid)
	}
	ctx = platformtx.WithSession(ctx)
	check := func(ctx context.Context) (selectionState, error) {
		if err := publication.Check(ctx); err != nil {
			return selectionState{}, fmt.Errorf("current pull execution: %w", err)
		}
		return s.selectedState(ctx, selected)
	}
	// The source checks execution again before every upstream exchange it
	// makes outside a transaction; each check resolved the run, the target,
	// the owning remote and the caller afresh (#1895). One resolution serves
	// this fetch's early checks. Every check inside a transaction, which is
	// where the cache and the intent are published, still runs in full.
	checked := new(byte)
	early := func(ctx context.Context) (selectionState, error) {
		return platformtx.Remember(ctx, false, checked, func() (selectionState, error) {
			// Both halves read local state only; one transaction resolves
			// the caller and the remote once for them.
			var state selectionState
			err := s.Content.Store.Tx(ctx, func(ctx context.Context) error {
				var err error
				state, err = check(ctx)
				return err
			})
			return state, err //nolint:wrapcheck // check's own refusals
		})
	}
	state, err := early(ctx)
	if err != nil {
		return api.PullCached{}, err
	}
	started := time.Now().UTC()
	entry, err := state.peer.Inspect(ctx, selected.UpstreamPath)
	if err != nil {
		return api.PullCached{}, fmt.Errorf("verify selected pull file: %w", err)
	}
	current, visible, err := state.selected(ctx, state.repository, selected.Selection, entry)
	if err != nil {
		return api.PullCached{}, err
	}
	if !visible || !sameSelection(current, selected) {
		return api.PullCached{}, source.ErrSuperseded
	}
	request := state.request
	parts := strings.Split(selected.Path, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	request.Resource = source.Resource{
		Kind: source.ResourceKindArtifact, Method: http.MethodGet,
		Path: selected.Path, EscapedPath: strings.Join(parts, "/"),
		SelectedContent: &source.ContentExpectation{Size: selected.Size, Checksums: selected.Actual},
	}
	request.Publication = &source.Publication{
		Check: func(ctx context.Context) error {
			if s.InTx(ctx) {
				_, err := check(ctx)
				return err
			}
			_, err := early(ctx)
			return err
		},
		Fence: publication.Fence,
	}
	if selected.Selection.Properties {
		properties := userProperties(selected.Properties)
		request.SelectedProperties, request.SelectedPropertiesStarted = &properties, started
	}
	result, err := s.Read(ctx, request)
	if err != nil {
		return api.PullCached{}, fmt.Errorf("read selected pull bytes: %w", err)
	}
	if result.Stream != nil && result.Stream.Body != nil {
		if err := result.Stream.Body.Close(); err != nil {
			return api.PullCached{}, fmt.Errorf("close selected cache representation: %w", err)
		}
	}
	if result.CacheEntry == nil {
		return api.PullCached{}, fmt.Errorf("pull response did not populate cache: %w: %w", api.ErrPullNotStored, source.ErrUnsupported)
	}
	proof, err := cached(selected, *result.CacheEntry)
	if err != nil {
		return api.PullCached{}, err
	}
	var out api.PullCached
	err = s.Content.Store.Tx(ctx, func(ctx context.Context) error {
		if err := publication.Check(ctx); err != nil {
			return fmt.Errorf("current pull completion: %w", err)
		}
		return s.withCached(ctx, proof, func(ctx context.Context, state selectionState, entry model.RemoteCacheEntry) error {
			if selected.Selection.Properties {
				var err error
				entry, err = s.syncProperties(ctx, state, selected, entry)
				if err != nil {
					return err
				}
			}
			var err error
			out, err = cached(selected, entry)
			if err != nil {
				return err
			}
			if publication.Record != nil {
				if err := publication.Record(ctx, out); err != nil {
					return fmt.Errorf("record selected pull cache: %w", err)
				}
			}
			return publication.Fence(ctx) //nolint:wrapcheck // the worker owns the last local fence
		})
	})
	if err != nil {
		return api.PullCached{}, fmt.Errorf("publish selected pull completion: %w", err)
	}
	return out, nil
}

func (s Source) withCached(ctx context.Context, expected api.PullCached,
	effect func(context.Context, selectionState, model.RemoteCacheEntry) error,
) error {
	state, err := s.selectedState(ctx, expected.Item)
	if err != nil {
		return err
	}
	entries, next, err := s.Content.Store.RemoteCache().Exact(ctx, state.request.Repo.Key, expected.Item.Path, kernel.Page{Limit: 64})
	if err != nil {
		return fmt.Errorf("read selected cache representations: %w", err)
	}
	if next != "" {
		return source.ErrInvalidResult
	}
	for _, entry := range entries {
		proof, err := cached(expected.Item, entry)
		if err != nil || proof.Representation != expected.Representation || proof.Version != expected.Version {
			continue
		}
		token, err := s.Content.Store.RemoteCache().Begin(ctx, entry.Token.Key)
		if err != nil {
			return fmt.Errorf("current selected cache token: %w", err)
		}
		if token.Source != entry.Token.Source || token.Epoch != entry.Token.Epoch || token.Revision != entry.Token.Revision {
			return source.ErrSuperseded
		}
		if err := s.Content.Store.RemoteCache().Check(ctx, token); err != nil {
			return fmt.Errorf("guard selected cache representation: %w", err)
		}
		if token.PolicyRevision != entry.Token.PolicyRevision {
			// Policy-only edits preserve verified bytes and normal cache identity.
			// Commit current CAS authority so the receipt describes stored state.
			entry, err = s.Content.Store.RemoteCache().Put(ctx, token, entry)
			if err != nil {
				return fmt.Errorf("refresh selected cache policy receipt: %w", err)
			}
		}
		return effect(ctx, state, entry)
	}
	return source.ErrSuperseded
}

// WithCurrent refreshes current local authority and exact P9 cache provenance.
// It never asks the peer for bytes or metadata inside the checkpoint transaction.
func (s Source) WithCurrent(ctx context.Context, expected api.PullCached, effect func(context.Context) error) error {
	if s.Content == nil || s.Content.Store == nil || effect == nil || expected.Representation == "" || expected.Version == "" {
		return kernel.ErrInvalid
	}
	err := s.Content.Store.Tx(ctx, func(ctx context.Context) error {
		return s.withCached(ctx, expected, func(ctx context.Context, _ selectionState, _ model.RemoteCacheEntry) error {
			return effect(ctx)
		})
	})
	if err != nil {
		return fmt.Errorf("current selected pull cache: %w", err)
	}
	return nil
}
