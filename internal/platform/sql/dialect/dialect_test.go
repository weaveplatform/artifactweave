package dialect_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// Neither SQLite nor PostgreSQL accepts DO UPDATE SET with nothing to set, so
// an upsert whose columns are all key columns does nothing on a conflict: the
// row it would write is the one already there.
func TestUnitUpsertOfKeyColumnsOnlyDoesNothing(t *testing.T) {
	got := dialect.Upsert("members", []string{"team", "member"}, []string{"team", "member"})
	want := "INSERT INTO members (team, member) VALUES (?, ?) ON CONFLICT (team, member) DO NOTHING"
	if got != want {
		t.Errorf("Upsert = %q\nwant %q", got, want)
	}
}
