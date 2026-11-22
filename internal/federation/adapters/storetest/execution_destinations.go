package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func executionDestinations(t *testing.T, db port.Store) {
	t.Helper()
	ctx := t.Context()
	for index, row := range []struct{ key, url, incarnation string }{
		{"explicit:a", "https://peer.invalid/artifactory/a", "repository-lifetime"},
		{"explicit:a", "https://peer.invalid/artifactory/a", "repository-lifetime"},
		{"explicit:b", "https://peer.invalid/artifactory/b", "repository-lifetime"},
		{"explicit:c", "https://peer.invalid/artifactory/c", "old-lifetime"},
	} {
		binding := executionBinding("")
		binding.OneShot, binding.TargetKey, binding.Repository.Incarnation = true, row.key, row.incarnation
		run := storekit.Get[model.ExecutionRun](t)(db.Executions().Create(ctx, fmt.Sprintf("explicit-run-%d", index), binding, storekit.At(index+1)))
		options := model.ExecutionOptions{
			Independent: true, RepositoryKey: binding.Repository.Key, TargetKey: binding.TargetKey, TargetRevision: binding.TargetRevision,
			URL: row.url, SocketTimeout: time.Second,
		}
		storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return db.Executions().PutOptions(ctx, run.Reference, options) }))
		state := storekit.Get[model.ExecutionPathState](t)(db.Executions().PathStatus(ctx, row.key, row.incarnation, ""))
		storekit.Same(t, "explicit pending history needs no configuration", model.ReplicationIncomplete, state.Latest.Status)
	}
	first, next, err := db.Executions().ExplicitTargets(ctx, "source", "repository-lifetime", kernel.Page{Limit: 1})
	storekit.Must(t, err)
	if len(first) != 1 || first[0].Key != "explicit:a" || next == "" {
		t.Fatal("explicit status lost deduplication or bounded continuation")
	}
	second, last, err := db.Executions().ExplicitTargets(ctx, "source", "repository-lifetime", kernel.Page{Limit: 1, After: next})
	storekit.Must(t, err)
	if len(second) != 1 || second[0].Key != "explicit:b" || last != "" {
		t.Fatal("explicit status lost current lifetime/continuation fencing")
	}
	other, _, err := db.Executions().ExplicitTargets(ctx, "other-repository", "repository-lifetime", kernel.Page{})
	storekit.Must(t, err)
	if len(other) != 0 {
		t.Fatal("explicit status borrowed another repository's history")
	}
}
