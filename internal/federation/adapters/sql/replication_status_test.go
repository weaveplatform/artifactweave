package sql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccReplicationStatusDoesNotOpenSealedCredentials(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{fedsql.Module}, fedsql.New, func(t *testing.T, newStore func(*testing.T) *fedsql.Store) {
		sealed := newStore(t)
		if err := sealed.Replications().Put(t.Context(), model.Replication{Key: "k1", RepoKey: "repo-a", Password: "lab-only-password"}); err != nil {
			t.Fatal(err)
		}
		if err := sealed.Replications().PutRun(t.Context(), model.ReplicationRun{ReplicationKey: "k1", Started: time.Unix(1, 0), Status: model.ReplicationIncomplete}); err != nil {
			t.Fatal(err)
		}
		reader := fedsql.New(dialect.New(sealed.DB(), sealed.Dialect(), nil))
		if _, err := reader.Replications().Get(t.Context(), "k1"); !errors.Is(err, dialect.ErrNoKeyring) {
			t.Fatalf("sealed credential control: %v", err)
		}
		states, next, err := reader.Replications().StatusTargets(t.Context(), "repo-a", kernel.Page{})
		if err != nil || len(states) != 1 || states[0].Key != "k1" || !states[0].Latest.Started.Equal(time.Unix(1, 0)) || next != "" {
			t.Fatalf("credential-free status projection %+v %q %v", states, next, err)
		}
		if !states[0].LastCompleted.IsZero() {
			t.Fatal("invented a completion from an unfinished run")
		}
	}, sqltest.WithPostgres())
}

func TestAccReplicationStatusReportsDatabaseAndCancellationFailures(t *testing.T) {
	s := replicationStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.Replications().StatusTargets(ctx, "repo-a", kernel.Page{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled status read: %v", err)
	}
	sqltest.Exec(t, s.DB(), sqltest.DropTable("replication_runs"))
	if _, _, err := s.Replications().StatusTargets(t.Context(), "repo-a", kernel.Page{}); err == nil || errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing history table was presented as never-run: %v", err)
	}
}
