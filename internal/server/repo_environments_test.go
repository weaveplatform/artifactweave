package server

import (
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestRepositoryEnvironments checks that every write path refuses an
// environment that does not exist and writes nothing, and that accepted
// names are stored in the environment's own spelling (decision 0013).
func TestRepositoryEnvironments(t *testing.T) {
	ctx := context.Background()
	w := newRepoWorld(t)
	if err := w.db.Projects().CreateEnvironment(ctx, types.Environment{Name: "QA"}); err != nil {
		t.Fatal(err)
	}
	w.create(repo.Config{Key: "kept", Class: repo.Local, Environments: []string{"DEV"}})
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

	refused(
		"create",
		"PUT",
		"/artifactory/api/repositories/fresh",
		`{"rclass":"local","environments":["nope"]}`,
	)
	refused(
		"update",
		"POST",
		"/artifactory/api/repositories/kept",
		`{"environments":["PROD","nope"]}`,
	)
	refused("batch create", "PUT", "/artifactory/api/v2/repositories/batch",
		`[{"key":"b1","rclass":"local"},{"key":"b2","rclass":"local","environments":["nope"]}]`)
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

	// Built-in and custom environments are accepted in any case and stored
	// in their own spelling.
	if rec := w.do("PUT", "/artifactory/api/repositories/fresh", "admin",
		`{"rclass":"local","environments":["dev","qa"]}`); rec.Code != 200 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("POST", "/artifactory/api/repositories/kept", "admin",
		`{"environments":["prod"]}`); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("PUT", "/artifactory/api/v2/repositories/batch", "admin",
		`[{"key":"b1","rclass":"local","environments":["Qa"]}]`); rec.Code != 201 {
		t.Fatalf("batch create %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("POST", "/artifactory/api/v2/repositories/batch", "admin",
		`[{"key":"b1","environments":["DEV","dev"]}]`); rec.Code != 200 {
		t.Fatalf("batch update %d %s", rec.Code, rec.Body)
	}
	for key, want := range map[string]string{"fresh": "DEV,QA", "kept": "PROD", "b1": "DEV"} {
		if got := envs(key); got != want {
			t.Fatalf("%s: environments %s, want %s", key, got, want)
		}
	}
}
