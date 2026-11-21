package replication_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitReplicationStatusUsesCurrentLifetimeAndReadAuthority(t *testing.T) {
	for _, key := range []string{"local", "remote"} {
		t.Run(key, func(t *testing.T) {
			w, db, coordinator, ctx := coordinatorFixture(t, key, false)
			incarnation, denied := "repository-lifetime", false
			reader := replication.StatusReader{Store: db, Authorize: func(ctx context.Context, repository, root string) (repo.Class, string, error) {
				if !db.InTx(ctx) || repository != key {
					t.Fatal("status authority escaped its metadata snapshot")
				}
				if denied {
					return "", "", kernel.ErrForbidden
				}
				return w.repos.values[key].Class, incarnation, nil
			}}
			read := func(root string, want model.ReplicationStatus) {
				t.Helper()
				view, err := reader.Read(ctx, key, root)
				must(t, err)
				if len(view.Targets) != 1 || view.Targets[0].Status != want {
					t.Fatal("status disagreed with current scoped evidence", view)
				}
			}
			read("", model.ReplicationNeverRun)
			_, err := coordinator.Execute(ctx, key, "selected")
			must(t, err)
			read("", model.ReplicationIncomplete)
			read("private", model.ReplicationNeverRun)
			// Ordinary configuration edits retain history; repository recreation cannot.
			must(t, w.service.Update(ctx, key, replication.Input{SyncProperties: ptr(true)}))
			read("", model.ReplicationIncomplete)
			incarnation = "recreated-repository"
			read("", model.ReplicationNeverRun)
			incarnation = "repository-lifetime"
			denied = true
			if _, err := reader.Read(ctx, key, ""); !errors.Is(err, kernel.ErrForbidden) {
				t.Fatal("revoked reader retained status authority", err)
			}
			denied = false
			target := stored(t, w, key)
			target.URL = "https://private:status-canary@target.example/repo?token=status-canary"
			must(t, db.Replications().Put(ctx, target))
			_, err = reader.Read(ctx, key, "")
			if !errors.Is(err, kernel.ErrStore) || strings.Contains(err.Error(), "status-canary") {
				t.Fatal("legacy target credentials escaped status", err)
			}
		})
	}
}
