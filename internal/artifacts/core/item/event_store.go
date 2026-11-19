package item

import (
	"context"
	"fmt"
	"time"

	artapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// A format can update another artifact's properties (for example, npm moves a
// dist-tag). Keep those mutations at this service boundary too. The primary
// item's complete before/after snapshot already includes its format hooks.
type eventFormatStore struct {
	format.Store
	service *Service
}

func (s eventFormatStore) Items() port.ItemStore {
	return eventItems{ItemStore: s.Store.Items(), service: s.service}
}

type eventItems struct {
	port.ItemStore
	service *Service
}

type (
	propertyOwnerKey struct{}
	propertyOwner    struct {
		id       int64
		by       string
		previous *propertyOwner
	}
)

func ownPropertyEvents(ctx context.Context, id int64, by string) context.Context {
	previous, _ := ctx.Value(propertyOwnerKey{}).(*propertyOwner)
	if by == "" && previous != nil {
		by = previous.by
	}
	return context.WithValue(ctx, propertyOwnerKey{}, &propertyOwner{id: id, by: by, previous: previous})
}

func ownsPropertyEvents(ctx context.Context, id int64) bool {
	owner, _ := ctx.Value(propertyOwnerKey{}).(*propertyOwner)
	for ; owner != nil; owner = owner.previous {
		if owner.id == id {
			return true
		}
	}
	return false
}

func (i eventItems) changeProperties(ctx context.Context, id int64, at time.Time, change func(context.Context) error) error {
	if ownsPropertyEvents(ctx, id) {
		return change(ctx)
	}
	err := i.service.Store.Tx(ctx, func(ctx context.Context) error {
		before, err := i.Properties(ctx, id)
		if err != nil {
			return fmt.Errorf("item: format property snapshot: %w", err)
		}
		it, err := i.GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("item: format property identity: %w", err)
		}
		if err := change(ctx); err != nil {
			return err
		}
		return i.service.emitPropertyChanges(ctx, it, before, propertyActor(ctx), at)
	})
	if err != nil {
		return fmt.Errorf("item: format property transaction: %w", err)
	}
	return nil
}

func (i eventItems) PatchProperties(ctx context.Context, id int64, set model.Properties, remove []string, at time.Time) error {
	return i.changeProperties(ctx, id, at, func(ctx context.Context) error {
		if err := i.ItemStore.PatchProperties(ctx, id, set, remove, at); err != nil {
			return fmt.Errorf("item: format property patch: %w", err)
		}
		return nil
	})
}

func (i eventItems) SetProperties(ctx context.Context, id int64, set model.Properties, at time.Time) error {
	return i.changeProperties(ctx, id, at, func(ctx context.Context) error {
		if err := i.ItemStore.SetProperties(ctx, id, set, at); err != nil {
			return fmt.Errorf("item: format property replacement: %w", err)
		}
		return nil
	})
}

func (i eventItems) Delete(ctx context.Context, repoKey string, cached bool, full string) (port.DeleteResult, error) {
	var result port.DeleteResult
	err := i.service.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		result, err = i.ItemStore.Delete(ctx, repoKey, cached, full)
		if err != nil {
			return fmt.Errorf("item: format deletion: %w", err)
		}
		for _, it := range result.Removed {
			if !it.Folder {
				if err := i.service.emitArtifact(ctx, artapi.ArtifactDeleted, it, nil, propertyActor(ctx), i.service.now()); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return port.DeleteResult{}, fmt.Errorf("item: format deletion transaction: %w", err)
	}
	return result, nil
}

func propertyActor(ctx context.Context) string {
	owner, _ := ctx.Value(propertyOwnerKey{}).(*propertyOwner)
	if owner != nil {
		return owner.by
	}
	return ""
}
