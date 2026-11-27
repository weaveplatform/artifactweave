package storekit

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// CleanupAdmissionRecord captures membership when bytes are admitted. An item
// replacement or index unlink must not erase an outstanding response's pin.
type CleanupAdmissionRecord struct {
	api.CleanupAdmission
	ItemID         int64
	Versions       []int64
	RepositoryWide bool
}

// CleanupRecords are durable primitives. Preparation holds the repository's
// lifetime lock; release serializes immutable reservation identities without
// requiring a surviving repository. Barriers and admissions have no expiry.
type CleanupRecords interface {
	Closed(context.Context, api.CleanupExclusion) (bool, error)
	Close(context.Context, api.CleanupExclusion) error
	Barrier(context.Context, string) (api.CleanupExclusion, error)
	PutBarrier(context.Context, string, api.CleanupExclusion) error
	RemoveBarrier(context.Context, api.CleanupExclusion) error
	LinkedVersions(context.Context, string, int64) ([]int64, error)
	AddAdmission(context.Context, CleanupAdmissionRecord, time.Time) error
	RemoveAdmission(context.Context, api.CleanupAdmission) error
	Active(context.Context, string) (bool, error)
	ActiveItem(context.Context, string, int64) (bool, error)
}

// CleanupStores supplies the shared metadata transaction and lifetime lock.
type CleanupStores interface {
	tx.Runner
	Repos() port.RepoStore
	RemoteCache() port.RemoteCacheStore
	Items() port.ItemStore
	Packages() port.PackageStore
}

type cleanupExclusions struct {
	store   CleanupStores
	records CleanupRecords
}

// NewCleanupExclusions binds the participant protocol over durable records.
func NewCleanupExclusions(store CleanupStores, records CleanupRecords) api.CleanupExclusions {
	return cleanupExclusions{store, records}
}

func (c cleanupExclusions) Admit(ctx context.Context, key string, itemID int64) (api.CleanupAdmission, error) {
	var admission api.CleanupAdmission
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		source, err := c.store.RemoteCache().Source(ctx, key)
		if err != nil {
			return fmt.Errorf("read hosted admission metadata: %w", err)
		}
		repository, err := c.store.Repos().Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read hosted admission metadata: %w", err)
		}
		if repository.Class != repo.Local && repository.Class != repo.Federated {
			return nil
		}
		barrier, err := c.records.Barrier(ctx, key)
		if err != nil {
			return fmt.Errorf("read hosted admission metadata: %w", err)
		}
		if barrier.ID != "" {
			return api.ErrCleanupExcluded
		}
		it, err := c.store.Items().GetByID(ctx, itemID)
		if err != nil {
			return fmt.Errorf("read hosted admission metadata: %w", err)
		}
		if it.RepoKey != key || it.Cached || it.Folder {
			return kernel.ErrInvalid
		}
		versions, err := c.records.LinkedVersions(ctx, key, itemID)
		if err != nil {
			return fmt.Errorf("read hosted admission metadata: %w", err)
		}
		admission = api.CleanupAdmission{ID: rand.Text(), Repository: key, Incarnation: string(source.Repository)}
		record := CleanupAdmissionRecord{CleanupAdmission: admission, ItemID: itemID, Versions: versions}
		if len(versions) == 0 || len(versions) > api.MaxDownloadVersions {
			record.RepositoryWide = true
			record.Versions = nil
		}
		if err := c.records.AddAdmission(ctx, record, time.Now().UTC()); err != nil {
			return fmt.Errorf("record hosted admission: %w", err)
		}
		return nil
	})
	if err != nil {
		return api.CleanupAdmission{}, fmt.Errorf("admit hosted download: %w", err)
	}
	return admission, nil
}

func (c cleanupExclusions) Complete(ctx context.Context, admission api.CleanupAdmission) error {
	if admission.ID == "" {
		return nil
	}
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		// Completion only removes the exact private token, including its captured
		// lifetime. It cannot authorize deleting another lifetime's admission.
		if err := c.records.RemoveAdmission(ctx, admission); err != nil {
			return fmt.Errorf("complete hosted download: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("complete hosted admission: %w", err)
	}
	return nil
}

func validExclusion(ref api.CleanupExclusion) bool {
	if ref.ID == "" || ref.Owner == "" || ref.Repository == "" || ref.Incarnation == "" || ref.Generation == "" || len(ref.Versions) == 0 ||
		len(ref.Versions) > api.MaxDownloadVersions {
		return false
	}
	seen := make(map[int64]bool, len(ref.Versions))
	for _, version := range ref.Versions {
		if version.ID <= 0 || version.Package == "" || version.Version == "" || seen[version.ID] {
			return false
		}
		seen[version.ID] = true
	}
	return true
}

// SameCleanupExclusion compares complete bindings, independent of version order.
func SameCleanupExclusion(a, b api.CleanupExclusion) bool {
	a.Versions = slices.Clone(a.Versions)
	b.Versions = slices.Clone(b.Versions)
	less := func(a, b api.CleanupVersionIdentity) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	}
	slices.SortFunc(a.Versions, less)
	slices.SortFunc(b.Versions, less)
	return reflect.DeepEqual(a, b)
}

func (c cleanupExclusions) lifetime(ctx context.Context, ref api.CleanupExclusion) error {
	if !validExclusion(ref) {
		return kernel.ErrInvalid
	}
	source, err := c.store.RemoteCache().Source(ctx, ref.Repository)
	if err != nil {
		return fmt.Errorf("validate cleanup lifetime: %w", err)
	}
	if string(source.Repository) != ref.Incarnation || string(source.Generation) != ref.Generation {
		return kernel.ErrConflict
	}
	repository, err := c.store.Repos().Get(ctx, ref.Repository)
	if err != nil {
		return fmt.Errorf("validate cleanup lifetime: %w", err)
	}
	if repository.Class != repo.Federated {
		return kernel.ErrConflict
	}
	return nil
}

func (c cleanupExclusions) Prepare(ctx context.Context, ref api.CleanupExclusion) error {
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		if err := c.lifetime(ctx, ref); err != nil {
			return fmt.Errorf("prepare cleanup lifetime: %w", err)
		}
		closed, err := c.records.Closed(ctx, ref)
		if err != nil {
			return fmt.Errorf("prepare cleanup exclusion: %w", err)
		}
		if closed {
			return kernel.ErrConflict
		}
		held, err := c.records.Barrier(ctx, ref.Repository)
		if err != nil {
			return fmt.Errorf("prepare cleanup exclusion: %w", err)
		}
		if held.ID != "" {
			if SameCleanupExclusion(held, ref) {
				return nil
			}
			return api.ErrCleanupExcluded
		}
		reader, ok := c.store.Packages().(port.PackageDownloadEvidence)
		if !ok {
			return kernel.ErrStore
		}
		requested := make([]api.DownloadVersion, 0, len(ref.Versions))
		for _, version := range ref.Versions {
			requested = append(requested, version.DownloadVersion)
		}
		actual, err := reader.VersionDownloads(ctx, ref.Repository, requested)
		if err != nil {
			return fmt.Errorf("prepare cleanup exclusion: %w", err)
		}
		if len(actual) != len(ref.Versions) {
			return kernel.ErrConflict
		}
		for _, wanted := range ref.Versions {
			found := slices.ContainsFunc(actual, func(got api.VersionDownload) bool {
				return wanted.ID == got.ID && wanted.DownloadVersion == got.DownloadVersion
			})
			if !found {
				return kernel.ErrConflict
			}
		}
		// The same lifetime row serializes both operations: no admission can pass
		// between the active check and publication of the nonexpiring barrier.
		// Native deletion hooks can remove companions outside the selected
		// versions. The gate closes the whole repository, so its drain must
		// include every existing response before ordered tombstones are safe.
		active, err := c.records.Active(ctx, ref.Repository)
		if err != nil {
			return fmt.Errorf("prepare cleanup exclusion: %w", err)
		}
		if active {
			return api.ErrCleanupExcluded
		}
		if err := c.records.PutBarrier(ctx, ref.Repository, ref); err != nil {
			return fmt.Errorf("publish cleanup barrier: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("persist cleanup exclusion: %w", err)
	}
	return nil
}

func (c cleanupExclusions) Guard(ctx context.Context, ref api.CleanupExclusion) error {
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		if err := c.lifetime(ctx, ref); err != nil {
			return fmt.Errorf("guard cleanup lifetime: %w", err)
		}
		held, err := c.records.Barrier(ctx, ref.Repository)
		if err != nil {
			return fmt.Errorf("guard cleanup exclusion: %w", err)
		}
		if !SameCleanupExclusion(held, ref) {
			return api.ErrCleanupExcluded
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("check cleanup exclusion: %w", err)
	}
	return nil
}

func (c cleanupExclusions) Release(ctx context.Context, ref api.CleanupExclusion, authorized func(context.Context) error) error {
	if !validExclusion(ref) {
		return kernel.ErrInvalid
	}
	err := c.store.Tx(ctx, func(ctx context.Context) error {
		// A terminal binding is irreversible. Replaying its acknowledgment cannot
		// depend on today's repository incarnation, config, applied frontier or
		// coordinator availability, and must not clear a replacement reservation.
		closed, err := c.records.Closed(ctx, ref)
		if err != nil {
			return fmt.Errorf("release cleanup exclusion: %w", err)
		}
		if closed {
			return nil
		}
		if authorized == nil {
			return kernel.ErrInvalid
		}
		// Domain guards precede the reservation-row lock. Close then serializes
		// with even a delayed Prepare whose identity row did not exist at lookup.
		if err := authorized(ctx); err != nil {
			return fmt.Errorf("authorize cleanup release: %w", err)
		}
		if err := c.records.Close(ctx, ref); err != nil {
			return fmt.Errorf("release cleanup exclusion: %w", err)
		}
		if err := c.records.RemoveBarrier(ctx, ref); err != nil {
			return fmt.Errorf("remove cleanup barrier: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("persist cleanup release: %w", err)
	}
	return nil
}

// CheckCleanupMutation is called in the actual metadata mutation transaction.
// Repository configuration/lifetime edits never accept a cleanup context permit.
func CheckCleanupMutation(ctx context.Context, store CleanupStores, records CleanupRecords, key string, allowPermit bool) error {
	if allowPermit && artifactevents.IsRemote(ctx) {
		return nil
	}
	// Source obtains the same durable lock used by admission and preparation.
	_, sourceErr := store.RemoteCache().Source(ctx, key)
	if sourceErr != nil {
		if !errors.Is(sourceErr, kernel.ErrNotFound) {
			return fmt.Errorf("guard cleanup mutation: %w", sourceErr)
		}
		// Raw stores historically permit orphan fixture rows. Only an absent
		// repository without a barrier retains that internal contract; a real
		// repository missing its lifetime is still a failure, never a permit.
		_, repositoryErr := store.Repos().Get(ctx, key)
		if !errors.Is(repositoryErr, kernel.ErrNotFound) {
			if repositoryErr != nil {
				return fmt.Errorf("guard cleanup mutation: %w", repositoryErr)
			}
			return fmt.Errorf("guard cleanup mutation: %w", sourceErr)
		}
	}
	held, err := records.Barrier(ctx, key)
	if err != nil {
		return fmt.Errorf("guard cleanup mutation: %w", err)
	}
	if held.ID == "" {
		return nil
	}
	if sourceErr != nil {
		return api.ErrCleanupExcluded
	}
	if allowPermit {
		if proposed, ok := api.CleanupExclusionFromContext(ctx); ok && SameCleanupExclusion(held, proposed) {
			return cleanupExclusions{store, records}.Guard(ctx, proposed)
		}
	}
	return api.ErrCleanupExcluded
}

// CheckCleanupItem protects every actually affected companion, including files
// indexed under another version. Captured IDs survive index unlink/replacement.
func CheckCleanupItem(ctx context.Context, records CleanupRecords, key string, itemID int64) error {
	if artifactevents.IsRemote(ctx) {
		return nil
	}
	if _, ok := api.CleanupExclusionFromContext(ctx); !ok {
		return nil
	}
	active, err := records.ActiveItem(ctx, key, itemID)
	if err != nil {
		return fmt.Errorf("guard cleanup item: %w", err)
	}
	if active {
		return api.ErrCleanupExcluded
	}
	return nil
}
