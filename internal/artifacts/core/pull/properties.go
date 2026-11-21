package pull

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func userProperties(properties map[string][]string) model.Properties {
	out := model.Properties(properties).Clone()
	for key := range out {
		for _, prefix := range []string{"artifactory.", "artifactweave.", "docker.", "oci.", "npm.", "pypi.", "nuget.", "maven."} {
			if strings.HasPrefix(key, prefix) {
				delete(out, key)
				break
			}
		}
	}
	return out
}

func (s Source) syncProperties(ctx context.Context, state selectionState, selected api.PullItem,
	entry model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	incoming := userProperties(selected.Properties)
	if entry.ItemID == 0 {
		if len(incoming) != 0 {
			return model.RemoteCacheEntry{}, fmt.Errorf("raw metadata has no property-bearing item: %w", source.ErrUnsupported)
		}
		return entry, nil
	}
	it, err := s.Content.Store.Items().GetByID(ctx, entry.ItemID)
	if err != nil {
		return model.RemoteCacheEntry{}, fmt.Errorf("selected property item: %w", err)
	}
	if it.Actual.SHA256 != selected.Actual.SHA256 || it.Size != selected.Size {
		return model.RemoteCacheEntry{}, source.ErrSuperseded
	}
	current, err := s.Content.Store.Items().Properties(ctx, it.ID)
	if err != nil {
		return model.RemoteCacheEntry{}, fmt.Errorf("current selected properties: %w", err)
	}
	merged, ownership := item.MergeRemoteProperties(current, entry.Smart, incoming, time.Now().UTC())
	var remove []string
	for key := range current {
		if _, present := merged[key]; !present {
			remove = append(remove, key)
		}
	}
	if err := s.Content.Annotate(ctx, state.request.Repo, it, merged, remove); err != nil {
		return model.RemoteCacheEntry{}, fmt.Errorf("synchronize selected properties: %w", err)
	}
	entry.Smart = ownership
	entry, err = s.Content.Store.RemoteCache().Put(ctx, entry.Token, entry)
	if err != nil {
		return model.RemoteCacheEntry{}, fmt.Errorf("publish selected property ownership: %w", err)
	}
	if err := s.Content.PropagateRemotePropertyState(ctx, entry); err != nil {
		return model.RemoteCacheEntry{}, fmt.Errorf("share selected property ownership: %w", err)
	}
	return entry, nil
}
