package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var modelRoutes = []struct {
	method, pattern, path string
	status                int
}{
	{"POST", "model/{name}/start-transaction", "model/controlled/start-transaction", 200},
	{
		"POST",
		"model/{name}/start-transaction/{version}",
		"model/controlled/start-transaction/1.0.0",
		200,
	},
	{
		"POST",
		"model/{namespace}/{name}/start-transaction",
		"model/team/controlled/start-transaction",
		200,
	},
	{
		"POST",
		"model/{namespace}/{name}/start-transaction/{version}",
		"model/team/controlled/start-transaction/1.0.0",
		200,
	},
	{
		"PUT",
		"model/entity-manifest/{name}/{version}/{transaction}",
		"model/entity-manifest/controlled/1.0.0/1793188800000",
		415,
	},
	{
		"PUT",
		"model/entity-manifest/{namespace}/{name}/{version}/{transaction}",
		"model/entity-manifest/team/controlled/1.0.0/1793188800000",
		415,
	},
	{
		"GET",
		"model/entity-manifest/{name}/{version}",
		"model/entity-manifest/controlled/1.0.0",
		404,
	},
	{
		"GET",
		"model/entity-manifest/{namespace}/{name}/{version}",
		"model/entity-manifest/team/controlled/1.0.0",
		404,
	},
}

var machineLearningStatusPart = statusPart{
	handwritten: func() map[string]bool {
		routes := map[string]bool{}
		for _, r := range modelRoutes {
			routes[r.method+" /artifactory/api/machinelearning/{repoKey}/"+r.pattern] = true
		}
		return routes
	}(),
	rows: func() []statusCase {
		var rows []statusCase
		for _, r := range modelRoutes {
			route := r.method + " /artifactory/api/machinelearning/{repoKey}/" + r.pattern
			path := "/artifactory/api/machinelearning/ml-status/" + r.path
			rows = append(
				rows,
				statusCase{route, path, "admin", "", r.status},
				statusCase{route, path, "user", "", 403},
				statusCase{route, path, "", "", 401},
			)
		}
		return rows
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		t.Helper()
		if err := db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "ml-status", Class: repo.Local, PackageType: repo.MachineLearning})); err != nil {
			t.Fatal(err)
		}
	},
}

var machineLearningMatrixPart = matrixPart{
	rows: func() []matrixRow {
		var rows []matrixRow
		for _, r := range modelRoutes {
			action := types.ActionWrite
			if r.method == "GET" {
				action = types.ActionRead
			}
			rows = append(
				rows,
				row(
					r.method+" /artifactory/api/machinelearning/{repoKey}/"+r.pattern,
					"/artifactory/api/machinelearning/ml-matrix/"+r.path,
					"",
					holds("ml-matrix", "models", action),
					r.status,
				),
			)
		}
		return rows
	},
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		if err := f.db.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "ml-matrix", Class: repo.Local, PackageType: repo.MachineLearning})); err != nil {
			t.Fatal(err)
		}
	},
}
