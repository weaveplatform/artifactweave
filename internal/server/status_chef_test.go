package server

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var chefRouteCases = []struct {
	method, pattern, path string
	status                int
}{
	{"GET", "/api/v1/cookbooks", "/api/v1/cookbooks", 200},
	{"HEAD", "/api/v1/cookbooks", "/api/v1/cookbooks", 200},
	{"GET", "/api/v1/search", "/api/v1/search?q=widget", 200},
	{"HEAD", "/api/v1/search", "/api/v1/search?q=widget", 200},
	{"POST", "/api/v1/cookbooks", "/api/v1/cookbooks", 415},
	{"GET", "/api/v1/cookbooks/{name}", "/api/v1/cookbooks/widget", 404},
	{"HEAD", "/api/v1/cookbooks/{name}", "/api/v1/cookbooks/widget", 404},
	{"POST", "/api/v1/cookbooks/{name}", "/api/v1/cookbooks/widget", 400},
	{"DELETE", "/api/v1/cookbooks/{name}", "/api/v1/cookbooks/widget", 404},
	{
		"GET",
		"/api/v1/cookbooks/{name}/versions/{version}",
		"/api/v1/cookbooks/widget/versions/1_0_0",
		404,
	},
	{
		"HEAD",
		"/api/v1/cookbooks/{name}/versions/{version}",
		"/api/v1/cookbooks/widget/versions/1_0_0",
		404,
	},
	{
		"DELETE",
		"/api/v1/cookbooks/{name}/versions/{version}",
		"/api/v1/cookbooks/widget/versions/1_0_0",
		404,
	},
	{"DELETE", "/api/v1/cookbooks/{name}/{version}", "/api/v1/cookbooks/widget/1.0.0", 404},
	{
		"GET",
		"/api/v1/cookbooks/{name}/versions/{version}/download",
		"/api/v1/cookbooks/widget/versions/1_0_0/download",
		404,
	},
	{
		"HEAD",
		"/api/v1/cookbooks/{name}/versions/{version}/download",
		"/api/v1/cookbooks/widget/versions/1_0_0/download",
		404,
	},
	{"POST", "/reindex", "/reindex", 200},
}

var chefStatusPart = statusPart{
	handwritten: func() map[string]bool {
		out := map[string]bool{}
		for _, r := range chefRouteCases {
			out[r.method+" /artifactory/api/chef/{repoKey}"+r.pattern] = true
		}
		return out
	}(),
	rows: func() []statusCase {
		var out []statusCase
		for _, r := range chefRouteCases {
			pattern := r.method + " /artifactory/api/chef/{repoKey}" + r.pattern
			path := "/artifactory/api/chef/chef-status" + r.path
			out = append(
				out,
				statusCase{pattern, path, "admin", "", r.status},
				statusCase{pattern, path, "user", "", 403},
				statusCase{pattern, path, "", "", 401},
			)
		}
		return out
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		t.Helper()
		if err := db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "chef-status", Class: repo.Local, PackageType: repo.Chef})); err != nil {
			t.Fatal(err)
		}
	},
}

var chefMatrixPart = matrixPart{
	rows: func() []matrixRow {
		var out []matrixRow
		for _, c := range chefRouteCases {
			action := types.ActionRead
			switch c.method {
			case "POST":
				action = types.ActionWrite
			case "DELETE":
				action = types.ActionDelete
			}
			permission := ""
			if strings.Contains(c.path, "widget") {
				permission = "widget"
			}
			if c.pattern == "/reindex" {
				action = types.ActionManage
			}
			out = append(
				out,
				row(
					c.method+" /artifactory/api/chef/{repoKey}"+c.pattern,
					"/artifactory/api/chef/chef-matrix"+c.path,
					"",
					holds("chef-matrix", permission, action),
					c.status,
				),
			)
		}
		return out
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		if err := f.db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "chef-matrix", Class: repo.Local, PackageType: repo.Chef})); err != nil {
			t.Fatal(err)
		}
	},
}
