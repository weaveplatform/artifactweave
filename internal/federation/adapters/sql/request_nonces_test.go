package sql_test

import (
	"path/filepath"
	"testing"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqlite"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// TestAccRequestNoncesSurviveRestart remembers a signed peer request's
// nonce, closes the SQLite database and opens it again, as a restart does:
// the replay is still refused, because the replay cache is a table rather
// than process memory (decision 0021, Transport).
func TestAccRequestNoncesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation.db")
	open := func() (*fedsql.Store, func()) {
		t.Helper()
		db, err := sqlite.OpenDB(path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dialect.Migrate(t.Context(), db, sqlite.Dialect, []dialect.Module{fedsql.Module}, ""); err != nil {
			t.Fatal(err)
		}
		return fedsql.New(dialect.New(db, sqlite.Dialect, nil)), func() { _ = db.Close() }
	}
	hash := []byte{0x7e, 0x57}
	s, closeDB := open()
	storekit.Must(t, s.RequestNonces().Remember(t.Context(), hash, storekit.At(5), storekit.At(0)))
	closeDB()

	s, closeDB = open()
	defer closeDB()
	storekit.WantErr(t, s.RequestNonces().Remember(t.Context(), hash, storekit.At(6), storekit.At(1)), kernel.ErrConflict)
}
