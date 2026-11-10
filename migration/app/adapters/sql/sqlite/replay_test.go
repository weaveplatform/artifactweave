package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/sqlite"
	"github.com/weaveplatform/artifactweave/migration/app/storetest"
)

func TestAccReplayProcessCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")
	s, err := sqlite.Open(t.Context(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	storetest.ReplayCrash(t, s, []string{"ARTIFACTWEAVE_REPLAY_CRASH_SQLITE=" + path})
}

func TestAccReplayCrashHelper(t *testing.T) {
	path := os.Getenv("ARTIFACTWEAVE_REPLAY_CRASH_SQLITE")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := sqlite.Open(
		t.Context(),
		path,
		sqlite.Options{Options: sql.Options{SkipMigrate: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	storetest.ReplayCrashChild(t, s)
}
