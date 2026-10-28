package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const cranArchiveRoute = "GET /artifactory/api/cran/{repoKey}/move-archives"

var cranArchiveMatrixPart = matrixPart{
	rows: func() []matrixRow {
		return []matrixRow{row(cranArchiveRoute, cranMoveURL, "", administrators, 200)}
	},
	seed: func(t *testing.T, f *matrixFixture) {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran,
		})); err != nil {
			t.Fatal(err)
		}
	},
	tune: func(c *Config) { c.CranArchiveMoverEnabled = true },
}

var cranArchiveStatusPart = statusPart{
	generated: map[string]bool{cranArchiveRoute: true},
	tune:      func(c *Config) { c.CranArchiveMoverEnabled = true },
	rows: func() []statusCase {
		const base = "/artifactory/api/cran/"
		const suffix = "/move-archives"
		return []statusCase{
			{cranArchiveRoute, base + "cran-migrate" + suffix, "admin", "", 200},
			{cranArchiveRoute, base + "cran-migrate" + suffix, "", "", 401},
			{cranArchiveRoute, base + "cran-migrate" + suffix, "anonymous", "", 401},
			{cranArchiveRoute, base + "cran-migrate" + suffix, "user", "", 403},
			{cranArchiveRoute, base + "missing" + suffix, "admin", "", 404},
			{cranArchiveRoute, base + "libs-local" + suffix, "admin", "", 400},
			{cranArchiveRoute, base + "cran-collision" + suffix, "admin", "", 409},
			{cranArchiveRoute, base + "cran-lost-blob" + suffix, "admin", "", 500},
		}
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		for _, key := range []string{"cran-migrate", "cran-collision", "cran-lost-blob"} {
			if err := db.Repos().
				Create(t.Context(), *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Cran})); err != nil {
				t.Fatal(err)
			}
			if key == "cran-migrate" {
				continue
			}
			if _, err := db.Items().
				Put(t.Context(), types.Item{RepoKey: key, Name: cranLegacyPath, Actual: types.Checksums{SHA256: sha256Hex("missing blob")}}); err != nil {
				t.Fatal(err)
			}
			if key == "cran-collision" {
				if _, err := db.Items().
					Put(t.Context(), types.Item{RepoKey: key, Name: cranArchivePath, Folder: true}); err != nil {
					t.Fatal(err)
				}
			}
		}
	},
}
