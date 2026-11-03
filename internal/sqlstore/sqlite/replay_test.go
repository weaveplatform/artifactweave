package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
)

func TestAccReplayProcessCrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")
	s, err := sqlite.Open(t.Context(), path, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sqltest.ReplayCrash(t, s, []string{"ARTIFACTWEAVE_REPLAY_CRASH_SQLITE=" + path})
}

func TestAccReplayCrashHelper(t *testing.T) {
	path := os.Getenv("ARTIFACTWEAVE_REPLAY_CRASH_SQLITE")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := sqlite.Open(
		t.Context(),
		path,
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	sqltest.ReplayCrashChild(t, s)
}
