package sql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storekit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

type cleanupRecords struct{ s *Store }

// CleanupExclusions binds durable hosted admissions and cleanup reservations.
func (s *Store) CleanupExclusions() api.CleanupExclusions {
	return storekit.NewCleanupExclusions(s, cleanupRecords{s})
}

func (r cleanupRecords) Barrier(ctx context.Context, key string) (api.CleanupExclusion, error) {
	var raw string
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT barrier FROM artifact_cleanup_state WHERE repo_key = ?"), key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return api.CleanupExclusion{}, nil
	}
	if err != nil {
		return api.CleanupExclusion{}, r.s.Wrap("read cleanup barrier", err)
	}
	var ref api.CleanupExclusion
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		return ref, fmt.Errorf("decode cleanup barrier: %w", kernel.ErrStore)
	}
	return ref, nil
}

func (r cleanupRecords) PutBarrier(ctx context.Context, key string, ref api.CleanupExclusion) error {
	if err := r.bind(ctx, ref, false); err != nil {
		return err
	}
	_, err := r.s.Exec(ctx, "persist cleanup barrier",
		"INSERT INTO artifact_cleanup_state (repo_key, barrier) VALUES (?, ?) ON CONFLICT (repo_key) DO UPDATE SET barrier = excluded.barrier", key, dialect.MustJSON(ref))
	return err
}

func (r cleanupRecords) RemoveBarrier(ctx context.Context, ref api.CleanupExclusion) error {
	var raw string
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT barrier FROM artifact_cleanup_state WHERE repo_key = ?"), ref.Repository).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return r.s.Wrap("read released cleanup barrier", err)
	}
	var held api.CleanupExclusion
	if err := json.Unmarshal([]byte(raw), &held); err != nil {
		return fmt.Errorf("decode released cleanup barrier: %w", kernel.ErrStore)
	}
	if !storekit.SameCleanupExclusion(held, ref) {
		return nil
	}
	// Updating a non-key field takes no repository FK/lifetime lock. The raw
	// compare-and-swap protects a replacement gate without requiring identical
	// JSON ordering between a release request and the persisted version set.
	_, err = r.s.Exec(ctx, "remove exact cleanup barrier", "UPDATE artifact_cleanup_state SET barrier = '{}' WHERE repo_key = ? AND barrier = ?", ref.Repository, raw)
	return err
}

func (r cleanupRecords) LinkedVersions(ctx context.Context, key string, itemID int64) ([]int64, error) {
	var ids []int64
	err := r.s.Rows(
		ctx,
		"capture admission version membership",
		"SELECT v.id FROM package_versions v JOIN packages p ON p.id = v.package_id JOIN package_version_files f ON f.version_id = v.id WHERE f.item_id = ? AND p.repo_key = ? ORDER BY v.id LIMIT ?",
		[]any{itemID, key, api.MaxDownloadVersions + 1},
		func(row *sql.Rows) error {
			var id int64
			if err := row.Scan(&id); err != nil {
				return fmt.Errorf("scan cleanup version: %w", err)
			}
			ids = append(ids, id)
			return nil
		},
	)
	return ids, err
}

func (r cleanupRecords) AddAdmission(ctx context.Context, record storekit.CleanupAdmissionRecord, at time.Time) error {
	_, err := r.s.Exec(
		ctx,
		"persist active hosted admission",
		"INSERT INTO artifact_cleanup_admissions (id, repo_key, incarnation, item_id, repository_wide) VALUES (?, ?, ?, ?, ?)",
		record.ID,
		record.Repository,
		record.Incarnation,
		record.ItemID,
		record.RepositoryWide,
	)
	if err != nil {
		return err
	}
	revision := rand.Text()
	if record.RepositoryWide {
		_, err = r.s.Exec(
			ctx,
			"record unknown membership activity",
			"INSERT INTO artifact_cleanup_state (repo_key, barrier, admitted_at, admission_revision) VALUES (?, '{}', ?, ?) ON CONFLICT (repo_key) DO UPDATE SET admitted_at = CASE WHEN artifact_cleanup_state.admitted_at > excluded.admitted_at THEN artifact_cleanup_state.admitted_at ELSE excluded.admitted_at END, admission_revision = excluded.admission_revision",
			record.Repository,
			dialect.Micros(at),
			revision,
		)
		return err
	}
	for _, id := range record.Versions {
		if _, err := r.s.Exec(
			ctx,
			"capture active admission version",
			"INSERT INTO artifact_cleanup_admission_versions (admission_id, version_id) VALUES (?, ?)",
			record.ID,
			id,
		); err != nil {
			return err
		}
		if _, err := r.s.Exec(
			ctx,
			"record private admitted download",
			"UPDATE package_versions SET admitted_at = CASE WHEN admitted_at > ? THEN admitted_at ELSE ? END, admission_revision = ? WHERE id = ?",
			dialect.Micros(at),
			dialect.Micros(at),
			revision,
			id,
		); err != nil {
			return err
		}
	}
	return nil
}

func (r cleanupRecords) RemoveAdmission(ctx context.Context, token api.CleanupAdmission) error {
	var key, incarnation string
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT repo_key, incarnation FROM artifact_cleanup_admissions WHERE id = ?"), token.ID).Scan(&key, &incarnation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return r.s.Wrap("read active admission", err)
	}
	if key != token.Repository || incarnation != token.Incarnation {
		return kernel.ErrConflict
	}
	_, err = r.s.Exec(ctx, "complete hosted admission", "DELETE FROM artifact_cleanup_admissions WHERE id = ?", token.ID)
	return err
}

func (r cleanupRecords) Active(ctx context.Context, key string) (bool, error) {
	var active bool
	err := r.s.Q(ctx).QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT EXISTS (SELECT 1 FROM artifact_cleanup_admissions WHERE repo_key = ?)"), key).Scan(&active)
	return active, r.s.Wrap("drain repository cleanup admissions", err)
}

func (r cleanupRecords) ActiveItem(ctx context.Context, key string, itemID int64) (bool, error) {
	var active bool
	err := r.s.Q(ctx).
		QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT EXISTS (SELECT 1 FROM artifact_cleanup_admissions WHERE repo_key = ? AND (repository_wide = TRUE OR item_id = ?))"), key, itemID).
		Scan(&active)
	return active, r.s.Wrap("check active file admissions", err)
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

func (r cleanupRecords) Closed(ctx context.Context, ref api.CleanupExclusion) (bool, error) {
	var raw string
	var closed bool
	err := r.s.Q(ctx).
		QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT binding,closed FROM artifact_cleanup_reservations WHERE owner = ? AND id = ?"), ref.Owner, ref.ID).
		Scan(&raw, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, r.s.Wrap("read terminal cleanup reservation", err)
	}
	var previous api.CleanupExclusion
	if err := json.Unmarshal([]byte(raw), &previous); err != nil {
		return false, kernel.ErrStore
	}
	if !storekit.SameCleanupExclusion(previous, ref) {
		return false, kernel.ErrConflict
	}
	return closed, nil
}

func (r cleanupRecords) Close(ctx context.Context, ref api.CleanupExclusion) error {
	return r.bind(ctx, ref, true)
}

// Both publishers lock the same immutable identity, including when no row
// existed at their initial lookup. A closed identity can never reopen, and
// ON CONFLICT must never relabel another binding as terminal.
func (r cleanupRecords) bind(ctx context.Context, ref api.CleanupExclusion, closeReservation bool) error {
	if !r.s.InTx(ctx) {
		return kernel.ErrInvalid
	}
	_, err := r.s.Exec(ctx, "bind cleanup reservation identity",
		"INSERT INTO artifact_cleanup_reservations (owner,id,binding,closed) VALUES (?,?,?,?) ON CONFLICT (owner,id) DO NOTHING",
		ref.Owner, ref.ID, dialect.MustJSON(ref), closeReservation)
	if err != nil {
		return err
	}
	var raw string
	var closed bool
	err = r.s.Q(ctx).
		QueryRowContext(ctx, r.s.Dialect().Rebind("SELECT binding,closed FROM artifact_cleanup_reservations WHERE owner = ? AND id = ?"+r.s.LockClause(ctx)), ref.Owner, ref.ID).
		Scan(&raw, &closed)
	if err != nil {
		return r.s.Wrap("lock cleanup reservation identity", err)
	}
	var previous api.CleanupExclusion
	if err := json.Unmarshal([]byte(raw), &previous); err != nil {
		return kernel.ErrStore
	}
	if !storekit.SameCleanupExclusion(previous, ref) {
		return kernel.ErrConflict
	}
	if !closeReservation {
		if closed {
			return kernel.ErrConflict
		}
		return nil
	}
	_, err = r.s.Exec(ctx, "retain terminal cleanup reservation", "UPDATE artifact_cleanup_reservations SET closed = TRUE WHERE owner = ? AND id = ?", ref.Owner, ref.ID)
	return err
}

func (s *Store) checkCleanupDeletedItems(ctx context.Context, key, where string, args []any) error {
	if artifactevents.IsRemote(ctx) {
		return nil
	}
	if _, ok := api.CleanupExclusionFromContext(ctx); !ok {
		return nil
	}
	// This existence check uses precisely the actual delete predicate. It stays
	// bounded even for a folder and does not materialize all removed item IDs.
	var active bool
	query := "SELECT EXISTS (SELECT 1 FROM artifact_cleanup_admissions a WHERE a.repo_key = ? AND (a.repository_wide = TRUE OR a.item_id IN (SELECT id FROM items WHERE " + where + ")))"
	bound := append([]any{key}, args...)
	if err := s.Q(ctx).QueryRowContext(ctx, s.Dialect().Rebind(query), bound...).Scan(&active); err != nil {
		return s.Wrap("guard actually removed files", err)
	}
	if active {
		return api.ErrCleanupExcluded
	}
	return nil
}

func (s *Store) checkCleanupPackage(ctx context.Context, id int64) error {
	pkg, err := s.Packages().Get(ctx, id)
	if err != nil {
		return fmt.Errorf("read cleanup package: %w", err)
	}
	return s.checkCleanupMutation(ctx, pkg.RepoKey, true)
}

func (s *Store) checkCleanupVersion(ctx context.Context, id int64) error {
	var packageID int64
	err := s.Q(ctx).QueryRowContext(ctx, s.Dialect().Rebind("SELECT package_id FROM package_versions WHERE id = ?"), id).Scan(&packageID)
	if err != nil {
		return s.Wrap("find cleanup version parent", err)
	}
	return s.checkCleanupPackage(ctx, packageID)
}

// Read identity without an item lock; the repository lifetime must be acquired
// before the normal item/property guard to keep the shared lock order.
func (s *Store) cleanupItemRepository(ctx context.Context, id int64) (string, error) {
	var key string
	err := s.Q(ctx).QueryRowContext(ctx, s.Dialect().Rebind("SELECT repo_key FROM items WHERE id = ?"), id).Scan(&key)
	return key, s.Wrap("read cleanup item repository", err)
}
