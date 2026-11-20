package storetest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunProjectRepositories proves the projection follows current configurations,
// not future assignment/share records, and preserves keyset/clone boundaries.
func RunProjectRepositories(t *testing.T, store port.RepoStore) {
	t.Helper()
	ctx := t.Context()
	first := *repo.Defaults(&repo.Config{Key: "alpha-dev", Class: repo.Local, ProjectKey: "alpha", Environments: []string{"DEV"}})
	second := *repo.Defaults(&repo.Config{Key: "alpha-prod", Class: repo.Remote, ProjectKey: "alpha", Environments: []string{"PROD"}, URL: "https://private.example", Username: "private-user", Password: "private-password"})
	for _, config := range []repo.Config{second, first, *repo.Defaults(&repo.Config{Key: "beta-dev", Class: repo.Local, ProjectKey: "beta", Environments: []string{"DEV"}}), *repo.Defaults(&repo.Config{Key: "unassigned", Class: repo.Local})} {
		storekit.Must(t, store.Create(ctx, config))
	}
	var actual []api.ProjectRepository
	page := kernel.Page{Limit: 1}
	for {
		entries, next, err := store.ProjectRepositories(ctx, "alpha", page)
		storekit.Must(t, err)
		if len(entries) > 1 {
			t.Fatalf("projection ignored page bound: %d", len(entries))
		}
		actual = append(actual, entries...)
		if next == "" {
			break
		}
		page.After = next
	}
	want := []api.ProjectRepository{
		{Key: first.Key, Class: first.Class, ProjectKey: "alpha", Environments: []string{"DEV"}},
		{Key: second.Key, Class: second.Class, ProjectKey: "alpha", Environments: []string{"PROD"}},
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("project contexts=%+v, want=%+v", actual, want)
	}
	for _, expected := range want {
		one, err := store.ProjectRepository(ctx, expected.Key)
		storekit.Must(t, err)
		if !reflect.DeepEqual(one, expected) {
			t.Fatalf("single repository context=%+v want=%+v", one, expected)
		}
		one.Environments[0] = "caller mutation"
		again, err := store.ProjectRepository(ctx, expected.Key)
		storekit.Must(t, err)
		if !reflect.DeepEqual(again, expected) {
			t.Fatal("single context aliased stored environments")
		}
	}
	unassigned, err := store.ProjectRepository(ctx, "unassigned")
	storekit.Must(t, err)
	if unassigned.Key != "unassigned" || unassigned.ProjectKey != "" || unassigned.Class != repo.Local {
		t.Fatal("unowned repository context was lost")
	}
	_, err = store.ProjectRepository(ctx, "missing")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	actual[0].Environments[0] = "caller mutation"
	fresh, _, err := store.ProjectRepositories(ctx, "alpha", kernel.Page{Limit: 1})
	storekit.Must(t, err)
	if !reflect.DeepEqual(fresh, want[:1]) {
		t.Fatal("caller changed stored environments")
	}
	first.ProjectKey, first.Environments = "beta", []string{"PROD"}
	storekit.Must(t, store.Update(ctx, first))
	current, _, err := store.ProjectRepositories(ctx, "alpha", kernel.Page{})
	storekit.Must(t, err)
	if !reflect.DeepEqual(current, want[1:]) {
		t.Fatalf("ownership move retained old project: %+v", current)
	}
	moved, _, err := store.ProjectRepositories(ctx, "beta", kernel.Page{Limit: 1})
	storekit.Must(t, err)
	if len(moved) != 1 || moved[0].Key != first.Key || moved[0].ProjectKey != "beta" || !reflect.DeepEqual(moved[0].Environments, []string{"PROD"}) {
		t.Fatalf("mixed ownership/environment tuple: %+v", moved)
	}
	one, err := store.ProjectRepository(ctx, first.Key)
	storekit.Must(t, err)
	if !reflect.DeepEqual(one, moved[0]) {
		t.Fatal("single context retained old ownership or environment")
	}
	for _, cursor := range []string{"!invalid", kernel.Cursor("one", "two")} {
		_, _, err := store.ProjectRepositories(ctx, "alpha", kernel.Page{After: cursor})
		if !errors.Is(err, kernel.ErrInvalidCursor) {
			t.Fatalf("invalid cursor=%v", err)
		}
	}
	_, _, err = store.ProjectRepositories(ctx, "", kernel.Page{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	missing, next, err := store.ProjectRepositories(ctx, "missing", kernel.Page{})
	storekit.Must(t, err)
	if len(missing) != 0 || next != "" {
		t.Fatalf("unknown project projection=%+v %q", missing, next)
	}
}
