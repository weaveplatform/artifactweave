package server

import (
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestAccRepositoryEnvironments checks Artifactory's rule for a repository's
// environments on every write path (decision 0013): ignored outside a
// project, DEV by default in one, and an unknown name refused with nothing
// written.
func TestAccRepositoryEnvironments(t *testing.T) {
	ctx := context.Background()
	w := newRepoWorld(t)
	if err := w.db.Projects().Create(ctx, types.Project{Key: "proj"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []types.Environment{{Name: "QA"}, {ProjectKey: "proj", Name: "stage"}} {
		if err := w.db.Projects().CreateEnvironment(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	w.create(repo.Config{
		Key: "kept", Class: repo.Local, ProjectKey: "proj", Environments: []string{"DEV"},
	})
	envs := func(key string) string {
		t.Helper()
		c, err := w.db.Repos().Get(ctx, key)
		if err != nil {
			return "missing"
		}
		return strings.Join(c.Environments, ",")
	}
	refused := func(name, method, path, body string) {
		t.Helper()
		rec := w.do(method, path, "admin", body)
		if rec.Code != 400 ||
			!strings.Contains(rec.Body.String(), "Environment 'nope' does not exist") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	refused("create", "PUT", "/artifactory/api/repositories/fresh",
		`{"rclass":"local","projectKey":"proj","environments":["nope"]}`)
	refused("update", "POST", "/artifactory/api/repositories/kept",
		`{"environments":["PROD","nope"]}`)
	refused("batch create", "PUT", "/artifactory/api/v2/repositories/batch",
		`[{"key":"b1","rclass":"local"},`+
			`{"key":"b2","rclass":"local","projectKey":"proj","environments":["nope"]}]`)
	refused("batch update", "POST", "/artifactory/api/v2/repositories/batch",
		`[{"key":"kept","description":"x"},{"key":"kept","environments":["nope"]}]`)
	for key, want := range map[string]string{"fresh": "missing", "b1": "missing", "kept": "DEV"} {
		if got := envs(key); got != want {
			t.Fatalf("%s: environments %s after refused writes, want %s", key, got, want)
		}
	}
	if c, _ := w.db.Repos().Get(ctx, "kept"); c.Description != "" {
		t.Fatalf("a refused batch updated a repository: %+v", c)
	}

	for _, step := range []struct{ method, path, body string }{
		// Outside a project the list is ignored, even an unknown name.
		{
			"PUT", "/artifactory/api/repositories/loose",
			`{"rclass":"local","environments":["nope"]}`,
		},
		// In a project an empty list becomes DEV.
		{"PUT", "/artifactory/api/repositories/fresh", `{"rclass":"local","projectKey":"proj"}`},
		// Built-in, global and the project's own, in any case.
		{
			"POST", "/artifactory/api/repositories/kept",
			`{"environments":["prod","qa","PROJ-STAGE"]}`,
		},
		{
			"PUT", "/artifactory/api/v2/repositories/batch",
			`[{"key":"b1","rclass":"local","projectKey":"proj","environments":["Qa"]}]`,
		},
		{
			"POST", "/artifactory/api/v2/repositories/batch",
			`[{"key":"b1","environments":["DEV","dev"]}]`,
		},
	} {
		if rec := w.do(step.method, step.path, "admin", step.body); rec.Code/100 != 2 {
			t.Fatalf("%s %s: %d %s", step.method, step.path, rec.Code, rec.Body)
		}
	}
	for key, want := range map[string]string{
		"loose": "", "fresh": "DEV", "kept": "PROD,QA,proj-stage", "b1": "DEV",
	} {
		if got := envs(key); got != want {
			t.Fatalf("%s: environments %q, want %q", key, got, want)
		}
	}
	// Leaving the project drops the environments.
	if rec := w.do("POST", "/artifactory/api/repositories/kept", "admin",
		`{"projectKey":""}`); rec.Code != 200 || envs("kept") != "" {
		t.Fatalf("leaving the project: %d %s, environments %q", rec.Code, rec.Body, envs("kept"))
	}
}
