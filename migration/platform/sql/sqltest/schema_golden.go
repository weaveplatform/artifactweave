package sqltest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// SchemaGolden compares Schema(db, dialect) with the golden file at path and
// reports a mismatch as a line diff; update rewrites the file from db first.
// The golden pins the schema that the module streams together build on a
// fresh database, so a change to any stream shows as a diff of the golden
// that review has to accept.
//
// A planned subtest then proves the comparison can fail: it adds a scratch
// table and requires the description to differ from the golden. The table is
// dropped when the subtest ends, because one PostgreSQL server serves every
// test of the binary.
func SchemaGolden(t *testing.T, db *sql.DB, dialect, path string, update bool) {
	t.Helper()
	got, err := Schema(t.Context(), db, dialect)
	if err != nil {
		t.Fatal(err)
	}
	if update {
		if err := os.WriteFile(filepath.Clean(path), []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(strings.Split(string(want), "\n"), strings.Split(got, "\n")); diff != "" {
		t.Errorf("%s no longer describes the migrated schema; "+
			"rerun with -update only for a deliberate schema change (-want +got):\n%s", path, diff)
	}
	t.Run("an added table changes the description", func(t *testing.T) {
		ctx := t.Context()
		if _, err := db.ExecContext(ctx, "CREATE TABLE golden_probe (id INTEGER)"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// t.Context is cancelled before cleanup runs.
			_, err := db.ExecContext(context.WithoutCancel(ctx), "DROP TABLE golden_probe")
			if err != nil {
				t.Errorf("drop golden_probe: %v", err)
			}
		})
		probed, err := Schema(ctx, db, dialect)
		if err != nil {
			t.Fatal(err)
		}
		if probed == string(want) || !strings.Contains(probed, "golden_probe") {
			t.Fatalf("Schema did not describe the added table golden_probe:\n%s", probed)
		}
	})
}
