package sql

import (
	"context"
	"crypto/rand"
)

// itemVersionsChanged runs before deletion can cascade the links away. where
// is an adapter-owned items predicate, never caller text. One indexed update
// fences every version sharing the item, rather than only VersionOf's first.
func (s *Store) itemVersionsChanged(ctx context.Context, where string, args ...any) error {
	_, err := s.Exec(ctx, "invalidate linked version snapshots",
		"UPDATE package_versions SET mutation_revision = ? WHERE id IN "+
			"(SELECT version_id FROM package_version_files WHERE item_id IN (SELECT id FROM items WHERE "+where+"))",
		append([]any{rand.Text()}, args...)...)
	return err
}

func (p packages) versionChanged(ctx context.Context, id int64) error {
	return p.s.ExecOne(ctx, "invalidate package version snapshot",
		"UPDATE package_versions SET mutation_revision = ? WHERE id = ?", rand.Text(), id)
}
