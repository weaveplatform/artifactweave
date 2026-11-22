package sql_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func pullSQLBinding() model.PullBinding {
	return model.PullBinding{
		TargetKey:      "target",
		TargetRevision: "configuration",
		Root:           "selected",
		Credential:     kernel.CredentialReference{Name: "author", TokenID: "original-token"},
		Source: artifactsapi.PullRepository{
			Repository: artifactsapi.TransferRepository{Key: "remote", Incarnation: "remote-lifetime", Generation: "generation", PackageType: repo.Generic},
			Revision:   strings.Repeat("a", 64),
		},
	}
}

func pullSQLIntent(run model.PullRun) model.PullItem {
	return model.PullItem{
		Run: run.Reference, Action: model.PullFetch, Path: "selected/a", Version: 1, Phase: model.PullIntent, Updated: run.Started,
		Source: &artifactsapi.PullItem{
			Repository: run.Binding.Source, Selection: run.Binding.Selection(), Path: "selected/a", UpstreamPath: "source/a", Size: 4,
			Actual: kernel.Checksums{SHA256: strings.Repeat("b", 64)}, Version: strings.Repeat("c", 64),
		},
	}
}

func TestAccPullStoredCorruptionCannotBecomeRecoveryEvidence(t *testing.T) {
	for _, corruption := range []string{"binding JSON", "target binding", "run phase", "item JSON", "scalar identity", "fingerprint", "invalid operation", "source evidence", "pending receipt", "completed receipt"} {
		t.Run(corruption, func(t *testing.T) {
			ctx := t.Context()
			s := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			p := s.PullProgress()
			run := storekit.Get[model.PullRun](t)(p.Create(ctx, "run", pullSQLBinding(), time.Unix(1, 0)))
			intent := storekit.Get[model.PullItem](t)(p.CreateItem(ctx, pullSQLIntent(run)))
			var query string
			var value any
			switch corruption {
			case "binding JSON":
				query, value = "UPDATE replication_pull_runs SET binding_json=?", "{"
			case "target binding":
				query, value = "UPDATE replication_pull_runs SET target_key=?", "different-target"
			case "run phase":
				query, value = "UPDATE replication_pull_runs SET phase=?", "invented"
			case "item JSON":
				query, value = "UPDATE replication_pull_items SET document=?", "{"
			case "scalar identity":
				query, value = "UPDATE replication_pull_items SET version=?", 2
			case "fingerprint":
				query, value = "UPDATE replication_pull_items SET intent_fingerprint=?", strings.Repeat("f", 64)
			default:
				bad := kernel.Clone(intent)
				switch corruption {
				case "invalid operation":
					bad.Action = "invented"
				case "source evidence":
					bad.Source.Actual.SHA256 = "invalid"
				case "pending receipt":
					bad.Cache = &artifactsapi.PullCached{}
				case "completed receipt":
					bad.Phase, bad.Outcome, bad.Cache = model.PullItemComplete, model.PullSucceeded, &artifactsapi.PullCached{}
				}
				document, err := json.Marshal(bad)
				storekit.Must(t, err)
				fingerprint := storekit.Get[string](t)(model.PullIntentFingerprint(bad))
				_, err = s.DB().ExecContext(ctx, "UPDATE replication_pull_items SET document=?, phase=?, intent_fingerprint=?", string(document), bad.Phase, fingerprint)
				storekit.Must(t, err)
			}
			if query != "" {
				_, err := s.DB().ExecContext(ctx, query, value)
				storekit.Must(t, err)
			}
			if corruption == "binding JSON" || corruption == "target binding" || corruption == "run phase" {
				got, err := p.Run(ctx, run.Reference)
				if !errors.Is(err, kernel.ErrStore) || got.Reference.ID != "" {
					t.Fatal("corrupt run became usable progress", err)
				}
				_, err = p.Create(ctx, "run", pullSQLBinding(), time.Unix(2, 0))
				storekit.WantErr(t, err, kernel.ErrStore)
			} else {
				got, err := p.Item(ctx, run.Reference, intent.Action, intent.Path)
				if !errors.Is(err, kernel.ErrStore) || got.Run.ID != "" {
					t.Fatal("corrupt intent became usable evidence", err)
				}
				rows, next, err := p.Items(ctx, run.Reference, false, kernel.Page{Limit: 1})
				if !errors.Is(err, kernel.ErrStore) || rows != nil || next != "" {
					t.Fatal("corruption became an exhausted recovery page", err)
				}
				_, err = p.CreateItem(ctx, intent)
				storekit.WantErr(t, err, kernel.ErrStore)
				settled := kernel.Clone(intent)
				settled.Version++
				settled.Phase, settled.Outcome, settled.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
				storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, intent.Version, settled) }), kernel.ErrStore)
			}
		})
	}
}

func TestAccPullProgressWriteFaultsCannotCommitCursorsOrEvidence(t *testing.T) {
	for _, tc := range []struct{ event, table string }{
		{"INSERT", "replication_pull_runs"},
		{"INSERT", "replication_pull_items"},
		{"UPDATE", "replication_pull_runs"},
		{"UPDATE", "replication_pull_items"},
	} {
		t.Run(tc.event+" "+tc.table, func(t *testing.T) {
			ctx := t.Context()
			s := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			p := s.PullProgress()
			run := storekit.Get[model.PullRun](t)(p.Create(ctx, "run", pullSQLBinding(), time.Unix(1, 0)))
			i := storekit.Get[model.PullItem](t)(p.CreateItem(ctx, pullSQLIntent(run)))
			sqltest.Exec(t, s.DB(), sqltest.FailTrigger(tc.event, tc.table))
			next := run
			next.Version++
			next.SourceCursor = "must-not-commit"
			settled := kernel.Clone(i)
			settled.Version++
			settled.Phase, settled.Outcome, settled.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
			err := s.Tx(ctx, func(ctx context.Context) error {
				switch {
				case tc.table == "replication_pull_runs" && tc.event == "INSERT":
					_, err := p.Create(ctx, "new-run", pullSQLBinding(), time.Unix(2, 0))
					return err
				case tc.event == "INSERT":
					fresh := kernel.Clone(i)
					fresh.Path, fresh.Source.Path = "selected/another", "selected/another"
					_, err := p.CreateItem(ctx, fresh)
					return err
				default:
					if err := p.Advance(ctx, run.Version, next); err != nil {
						return err
					}
					return p.AdvanceItem(ctx, i.Version, settled)
				}
			})
			if err == nil || errors.Is(err, kernel.ErrNotFound) || errors.Is(err, kernel.ErrConflict) {
				t.Fatal("storage failure was hidden as success or ordinary contention", err)
			}
			storekit.Same(t, "failed write retained cursor", run, storekit.Get[model.PullRun](t)(p.Run(ctx, run.Reference)))
			storekit.Same(t, "failed write retained intent", i, storekit.Get[model.PullItem](t)(p.Item(ctx, i.Run, i.Action, i.Path)))
			_, err = p.Item(ctx, run.Reference, model.PullFetch, "selected/another")
			storekit.WantErr(t, err, kernel.ErrNotFound)
		})
	}
}
