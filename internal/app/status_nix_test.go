package app

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

var nixStatusPart = statusPart{
	handwritten: map[string]bool{
		"GET /artifactory/api/nix/{repoKey}/*":        true,
		"HEAD /artifactory/api/nix/{repoKey}/*":       true,
		"PUT /artifactory/api/nix/{repoKey}/*":        true,
		"DELETE /artifactory/api/nix/{repoKey}/*":     true,
		"POST /artifactory/api/nix/{repoKey}/reindex": true,
	},
	rows: func() []statusCase {
		const base = "/artifactory/api/nix/"
		var rows []statusCase
		for _, tc := range []struct {
			method, path string
			status       int
		}{{"GET", "nix-cache-info", 200}, {"HEAD", "nix-cache-info", 200}, {"PUT", "invalid", 400}, {"DELETE", "nix-cache-info", 404}, {"POST", "reindex", 200}} {
			suffix := "*"
			if tc.method == "POST" {
				suffix = "reindex"
			}
			pattern := tc.method + " " + base + "{repoKey}/" + suffix
			rows = append(
				rows,
				statusCase{pattern, base + "nix-status/" + tc.path, "admin", "", tc.status},
				statusCase{pattern, base + "nix-status/" + tc.path, "user", "", 403},
				statusCase{pattern, base + "nix-status/" + tc.path, "", "", 401},
			)
		}
		return rows
	},
	seed: func(t *testing.T, db *inmem.Store, _ blob.Store) {
		t.Helper()
		if err := db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "nix-status", Class: repo.Local, PackageType: repo.Nix})); err != nil {
			t.Fatal(err)
		}
	},
}

var nixMatrixPart = matrixPart{
	rows: func() []matrixRow {
		var rows []matrixRow
		for _, c := range nixStatusPart.rows() {
			if c.as != "admin" {
				continue
			}
			action := api.ActionRead
			if strings.HasPrefix(c.route, "PUT ") {
				action = api.ActionWrite
			}
			if strings.HasPrefix(c.route, "DELETE ") {
				action = api.ActionDelete
			}
			if strings.HasPrefix(c.route, "POST ") {
				action = api.ActionManage
			}
			rows = append(
				rows,
				row(
					c.route,
					strings.Replace(c.path, "nix-status", "nix-matrix", 1),
					"",
					holds("nix-matrix", "", action),
					c.expected,
				),
			)
		}
		return rows
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		if err := f.db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "nix-matrix", Class: repo.Local, PackageType: repo.Nix})); err != nil {
			t.Fatal(err)
		}
	},
}
