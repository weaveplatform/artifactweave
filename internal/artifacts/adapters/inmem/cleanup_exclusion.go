package inmem

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

type (
	cleanupRecords  struct{ s *Store }
	cleanupActivity struct {
		At       time.Time
		Revision string
	}
)

// CleanupExclusions binds durable hosted admissions and cleanup reservations.
func (s *Store) CleanupExclusions() api.CleanupExclusions {
	return storekit.NewCleanupExclusions(s, cleanupRecords{s})
}

func (r cleanupRecords) barriers() *memtx.Table[string, api.CleanupExclusion] {
	return memtx.TableOf[string, api.CleanupExclusion](r.s.Store, "artifact_cleanup_barriers")
}

func (r cleanupRecords) admissions() *memtx.Table[string, storekit.CleanupAdmissionRecord] {
	return memtx.TableOf[string, storekit.CleanupAdmissionRecord](r.s.Store, "artifact_cleanup_admissions")
}

func (r cleanupRecords) activity() *memtx.Table[int64, cleanupActivity] {
	return memtx.TableOf[int64, cleanupActivity](r.s.Store, "package_version_admissions")
}

func (r cleanupRecords) repositoryActivity() *memtx.Table[string, cleanupActivity] {
	return memtx.TableOf[string, cleanupActivity](r.s.Store, "artifact_cleanup_repository_activity")
}

func (r cleanupRecords) Barrier(ctx context.Context, key string) (api.CleanupExclusion, error) {
	var out api.CleanupExclusion
	err := r.s.Do(ctx, func() error { out, _ = r.barriers().Get(key); out.Versions = slices.Clone(out.Versions); return nil })
	return out, err
}

func (r cleanupRecords) PutBarrier(ctx context.Context, key string, ref api.CleanupExclusion) error {
	return r.s.Do(ctx, func() error {
		identity := ref.Owner + "\x00" + ref.ID
		if previous, exists := r.bindings().Get(identity); exists && !storekit.SameCleanupExclusion(previous, ref) {
			return kernel.ErrConflict
		}
		if r.closed().Has(identity) {
			return kernel.ErrConflict
		}
		ref.Versions = slices.Clone(ref.Versions)
		r.bindings().Put(identity, ref)
		r.barriers().Put(key, ref)
		return nil
	})
}

func (r cleanupRecords) RemoveBarrier(ctx context.Context, ref api.CleanupExclusion) error {
	return r.s.Do(ctx, func() error {
		held, exists := r.barriers().Get(ref.Repository)
		if exists && storekit.SameCleanupExclusion(held, ref) {
			r.barriers().Del(ref.Repository)
		}
		return nil
	})
}

func (r cleanupRecords) LinkedVersions(ctx context.Context, key string, itemID int64) ([]int64, error) {
	var out []int64
	err := r.s.Do(ctx, func() error {
		p := packages(r)
		for link := range p.files().Rows {
			if link.ItemID != itemID {
				continue
			}
			version, ok := p.versions().Get(link.VersionID)
			if !ok {
				return kernel.ErrStore
			}
			pkg, ok := p.t().Get(version.PackageID)
			if !ok || pkg.RepoKey != key {
				return kernel.ErrStore
			}
			out = append(out, version.ID)
			if len(out) > api.MaxDownloadVersions {
				break
			}
		}
		return nil
	})
	return out, err
}

func (r cleanupRecords) AddAdmission(ctx context.Context, record storekit.CleanupAdmissionRecord, at time.Time) error {
	return r.s.Do(ctx, func() error {
		record.Versions = slices.Clone(record.Versions)
		r.admissions().Put(record.ID, record)
		activity := cleanupActivity{At: at, Revision: rand.Text()}
		if record.RepositoryWide {
			previous, _ := r.repositoryActivity().Get(record.Repository)
			if previous.At.After(activity.At) {
				activity.At = previous.At
			}
			r.repositoryActivity().Put(record.Repository, activity)
		}
		for _, id := range record.Versions {
			previous, _ := r.activity().Get(id)
			if previous.At.After(activity.At) {
				activity.At = previous.At
			}
			r.activity().Put(id, activity)
		}
		return nil
	})
}

func (r cleanupRecords) RemoveAdmission(ctx context.Context, token api.CleanupAdmission) error {
	return r.s.Do(ctx, func() error {
		record, ok := r.admissions().Get(token.ID)
		if !ok {
			return nil
		}
		if record.CleanupAdmission != token {
			return kernel.ErrConflict
		}
		r.admissions().Del(token.ID)
		return nil
	})
}

func (r cleanupRecords) Active(ctx context.Context, key string) (bool, error) {
	var active bool
	err := r.s.Do(ctx, func() error {
		for _, record := range r.admissions().Rows {
			if record.Repository == key {
				active = true
				break
			}
		}
		return nil
	})
	return active, err
}

func (r cleanupRecords) ActiveItem(ctx context.Context, key string, itemID int64) (bool, error) {
	var active bool
	err := r.s.Do(ctx, func() error {
		for _, record := range r.admissions().Rows {
			if record.Repository == key && (record.RepositoryWide || record.ItemID == itemID) {
				active = true
				break
			}
		}
		return nil
	})
	return active, err
}

func (s *Store) checkCleanupMutation(ctx context.Context, key string, permit bool) error {
	if err := storekit.CheckCleanupMutation(ctx, s, cleanupRecords{s}, key, permit); err != nil {
		return fmt.Errorf("guard cleanup mutation: %w", err)
	}
	return nil
}

func (s *Store) checkCleanupItem(ctx context.Context, it model.Item) error {
	if err := storekit.CheckCleanupItem(ctx, cleanupRecords{s}, it.RepoKey, it.ID); err != nil {
		return fmt.Errorf("guard cleanup item: %w", err)
	}
	return nil
}

func (r cleanupRecords) closed() *memtx.Table[string, api.CleanupExclusion] {
	return memtx.TableOf[string, api.CleanupExclusion](r.s.Store, "artifact_cleanup_closed")
}

func (r cleanupRecords) Closed(ctx context.Context, ref api.CleanupExclusion) (bool, error) {
	var closed bool
	err := r.s.Do(ctx, func() error {
		previous, ok := r.bindings().Get(ref.Owner + "\x00" + ref.ID)
		_, closed = r.closed().Get(ref.Owner + "\x00" + ref.ID)
		if ok && !storekit.SameCleanupExclusion(previous, ref) {
			return kernel.ErrConflict
		}
		return nil
	})
	return closed, err
}

func (r cleanupRecords) Close(ctx context.Context, ref api.CleanupExclusion) error {
	return r.s.Do(ctx, func() error {
		identity := ref.Owner + "\x00" + ref.ID
		if previous, exists := r.bindings().Get(identity); exists && !storekit.SameCleanupExclusion(previous, ref) {
			return kernel.ErrConflict
		}
		ref.Versions = slices.Clone(ref.Versions)
		r.closed().Put(ref.Owner+"\x00"+ref.ID, ref)
		r.bindings().Put(ref.Owner+"\x00"+ref.ID, ref)
		return nil
	})
}

func (s *Store) checkCleanupPackage(ctx context.Context, id int64) error {
	pkg, err := s.Packages().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("read cleanup package: %w", err)
	}
	return s.checkCleanupMutation(ctx, pkg.RepoKey, true)
}

func (s *Store) checkCleanupVersion(ctx context.Context, id int64) error {
	version, ok := (packages{s}).versions().Get(id)
	if !ok {
		return fmt.Errorf("read cleanup version: %w", memtx.NotFound("package version", id))
	}
	return s.checkCleanupPackage(ctx, version.PackageID)
}

func (r cleanupRecords) bindings() *memtx.Table[string, api.CleanupExclusion] {
	return memtx.TableOf[string, api.CleanupExclusion](r.s.Store, "artifact_cleanup_reservations")
}
