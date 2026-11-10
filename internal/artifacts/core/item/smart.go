package item

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// MergeRemoteProperties changes only values which still match the previous
// synchronized value. A local edit or deletion permanently takes ownership of
// that key for this source lifetime; upstream disappearance cannot erase it.
func MergeRemoteProperties(
	current model.Properties,
	prior *model.RemoteSmartState,
	incoming model.Properties,
	at time.Time,
) (model.Properties, *model.RemoteSmartState) {
	state := kernel.Clone(prior)
	if state == nil {
		state = &model.RemoteSmartState{}
	}
	if state.LocalProperties == nil {
		state.LocalProperties = make(map[string]bool)
	}
	merged := current.Clone()
	if merged == nil {
		merged = make(model.Properties)
	}
	owned := make(model.Properties)
	for key, old := range state.OwnedProperties {
		value, exists := current[key]
		if !exists || !slices.Equal(value, old) {
			state.LocalProperties[key] = true
			continue
		}
		delete(merged, key)
	}
	for key, values := range incoming {
		if state.LocalProperties[key] {
			continue
		}
		if _, local := merged[key]; local {
			state.LocalProperties[key] = true
			continue
		}
		merged.Set(key, values...)
		owned.Set(key, values...)
	}
	state.OwnedProperties, state.PropertiesAt = owned, at
	return merged, state
}

func (s *Service) propertyRepresentations(
	ctx context.Context,
	token model.RemoteCacheToken,
) ([]model.RemoteCacheEntry, error) {
	entries, next, err := s.Store.RemoteCache().
		Exact(ctx, token.Key.RepoKey, token.Key.Path, kernel.Page{Limit: 64})
	if err != nil {
		return nil, fmt.Errorf("cache property representations: %w", err)
	}
	if next != "" {
		return nil, fmt.Errorf("cache property representation bound: %w", kernel.ErrInvalid)
	}
	return entries, nil
}

// HasRemoteProperties applies legacy initial-only synchronization to the item,
// not each HTTP representation another client may independently request.
func (s *Service) HasRemoteProperties(
	ctx context.Context,
	token model.RemoteCacheToken,
) (bool, error) {
	it, err := s.Store.Items().Get(ctx, token.Key.RepoKey, true, token.Key.Path)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cache property item: %w", err)
	}
	entries, err := s.propertyRepresentations(ctx, token)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Token.Source == token.Source &&
			entry.Token.Key.SettingsRevision == token.Key.SettingsRevision &&
			entry.ItemID == it.ID &&
			entry.Blob.Checksums.SHA256 == it.Actual.SHA256 &&
			entry.Smart != nil &&
			!entry.Smart.PropertiesAt.IsZero() {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) initialRemoteProperties(
	ctx context.Context,
	u CacheUpload,
) (model.Properties, *model.RemoteSmartState, error) {
	current := make(model.Properties)
	it, err := s.Store.Items().Get(ctx, u.Repo.Key, true, u.Token.Key.Path)
	if err == nil {
		current, err = s.Store.Items().Properties(ctx, it.ID)
	}
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		return nil, nil, fmt.Errorf("cache current properties: %w", err)
	}
	entries, err := s.propertyRepresentations(ctx, u.Token)
	if err != nil {
		return nil, nil, err
	}
	prior := &model.RemoteSmartState{}
	var latest *model.RemoteSmartState
	for _, entry := range entries {
		if entry.Token.Source != u.Token.Source ||
			entry.Token.Key.SettingsRevision != u.Token.Key.SettingsRevision ||
			entry.Smart == nil {
			continue
		}
		if entry.Token.Key == u.Token.Key {
			prior = kernel.Clone(entry.Smart)
		}
		if entry.ItemID == it.ID && entry.Blob.Checksums.SHA256 == it.Actual.SHA256 &&
			(latest == nil || entry.Smart.PropertiesAt.After(latest.PropertiesAt)) {
			latest = entry.Smart
		}
	}
	if latest != nil {
		copyRemoteOwnership(prior, latest)
		// Another representation published a newer observation while this
		// request was in flight. Keep its property state and local edits.
		if !u.SmartPropertiesStarted.IsZero() &&
			latest.PropertiesAt.After(u.SmartPropertiesStarted) {
			return current, prior, nil
		}
	}
	merged, state := MergeRemoteProperties(current, prior, *u.SmartProperties, s.now())
	return merged, state, nil
}

// PropagateRemotePropertyState keeps item-level ownership coherent across a
// bounded set of current HTTP representations. It joins the caller's item/cache
// transaction and preserves each representation's separate statistics claims.
func (s *Service) PropagateRemotePropertyState(
	ctx context.Context,
	current model.RemoteCacheEntry,
) error {
	if current.Smart == nil || current.Smart.PropertiesAt.IsZero() {
		return nil
	}
	entries, err := s.propertyRepresentations(ctx, current.Token)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Token.Key == current.Token.Key || entry.Token.Source != current.Token.Source ||
			entry.Token.Key.SettingsRevision != current.Token.Key.SettingsRevision ||
			entry.ItemID != current.ItemID ||
			entry.Blob.Checksums.SHA256 != current.Blob.Checksums.SHA256 {
			continue
		}
		if entry.Smart == nil {
			entry.Smart = &model.RemoteSmartState{}
		}
		copyRemoteOwnership(entry.Smart, current.Smart)
		if _, err = s.Store.RemoteCache().Put(ctx, entry.Token, entry); err != nil {
			return fmt.Errorf("cache shared property ownership: %w", err)
		}
	}
	return nil
}

func copyRemoteOwnership(dst, src *model.RemoteSmartState) {
	dst.OwnedProperties = src.OwnedProperties.Clone()
	dst.LocalProperties = kernel.Clone(src.LocalProperties)
	dst.PropertiesAt = src.PropertiesAt
}
