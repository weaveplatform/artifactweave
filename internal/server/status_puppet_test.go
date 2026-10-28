package server

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var puppetStatusPart = statusPart{
	generated: map[string]bool{
		"GET /artifactory/api/puppet/{repoKey}/v3/modules":                      true,
		"GET /artifactory/api/puppet/{repoKey}/v3/modules/{userModule}":         true,
		"GET /artifactory/api/puppet/{repoKey}/v3/releases":                     true,
		"GET /artifactory/api/puppet/{repoKey}/v3/releases/{userModuleVersion}": true,
	},
	handwritten: map[string]bool{
		"HEAD /artifactory/api/puppet/{repoKey}/v3/modules":         true,
		"HEAD /artifactory/api/puppet/{repoKey}/v3/modules/{name}":  true,
		"HEAD /artifactory/api/puppet/{repoKey}/v3/releases":        true,
		"HEAD /artifactory/api/puppet/{repoKey}/v3/releases/{slug}": true,
		"GET /artifactory/api/puppet/{repoKey}/v3/files/{file}":     true,
		"HEAD /artifactory/api/puppet/{repoKey}/v3/files/{file}":    true,
		"POST /artifactory/api/puppet/{repoKey}/reindex":            true,
	},
	rows: func() []statusCase {
		const prefix = "/artifactory/api/puppet/"
		var rows []statusCase
		for _, route := range []struct {
			pattern, path string
			status        int
		}{
			{"GET " + prefix + "{repoKey}/v3/modules", "v3/modules?query=acme-widget", 200},
			{"GET " + prefix + "{repoKey}/v3/modules/{userModule}", "v3/modules/acme-widget", 404},
			{"GET " + prefix + "{repoKey}/v3/releases", "v3/releases?module=acme-widget", 200},
			{"GET " + prefix + "{repoKey}/v3/releases/{userModuleVersion}", "v3/releases/acme-widget-1.0.0", 404},
			{"HEAD " + prefix + "{repoKey}/v3/modules", "v3/modules?query=acme-widget", 200},
			{"HEAD " + prefix + "{repoKey}/v3/modules/{name}", "v3/modules/acme-widget", 404},
			{"HEAD " + prefix + "{repoKey}/v3/releases", "v3/releases?module=acme-widget", 200},
			{"HEAD " + prefix + "{repoKey}/v3/releases/{slug}", "v3/releases/acme-widget-1.0.0", 404},
			{"GET " + prefix + "{repoKey}/v3/files/{file}", "v3/files/acme-widget-1.0.0.tar.gz", 404},
			{"HEAD " + prefix + "{repoKey}/v3/files/{file}", "v3/files/acme-widget-1.0.0.tar.gz", 404},
		} {
			path := prefix + "puppet-status/" + route.path
			rows = append(
				rows,
				statusCase{route.pattern, path, "admin", "", route.status},
				statusCase{route.pattern, path, "user", "", 403},
				statusCase{route.pattern, path, "", "", 401},
			)
		}
		reindex := "POST " + prefix + "{repoKey}/reindex"
		return append(rows,
			statusCase{reindex, prefix + "puppet-status/reindex?invalid=1", "admin", "", 400},
			statusCase{reindex, prefix + "puppet-status/reindex", "user", "", 403},
			statusCase{reindex, prefix + "missing/reindex", "admin", "", 404},
		)
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		t.Helper()
		if err := db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "puppet-status", Class: repo.Local, PackageType: repo.Puppet})); err != nil {
			t.Fatal(err)
		}
	},
}

var puppetMatrixPart = matrixPart{
	rows: func() []matrixRow {
		var rows []matrixRow
		for _, c := range puppetStatusPart.rows() {
			if c.as != "admin" || strings.HasPrefix(c.route, "POST ") {
				continue
			}
			rows = append(
				rows,
				row(
					c.route,
					strings.Replace(c.path, "puppet-status", "puppet-matrix", 1),
					"",
					holds("puppet-matrix", "acme-widget", types.ActionRead),
					c.expected,
				),
			)
		}
		return append(
			rows,
			row(
				"POST /artifactory/api/puppet/{repoKey}/reindex",
				"/artifactory/api/puppet/puppet-matrix/reindex",
				"",
				holds("puppet-matrix", "", types.ActionManage),
				200,
			),
		)
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		if err := f.db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "puppet-matrix", Class: repo.Local, PackageType: repo.Puppet})); err != nil {
			t.Fatal(err)
		}
	},
}
