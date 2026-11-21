package sql_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccPushStoredCorruptionCannotBecomeRecoveryEvidence(t *testing.T) {
	for _, corruption := range []string{"binding JSON", "target binding", "run phase", "item JSON", "operation identity", "item phase", "source evidence"} {
		t.Run(corruption, func(t *testing.T) {
			ctx := t.Context()
			s := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
			progress := s.PushProgress()
			run := storekit.Get[model.PushRun](t)(progress.Create(ctx, "run", pushSQLBinding(), time.Unix(1, 0)))
			intent := storekit.Get[model.PushItem](t)(progress.CreateItem(ctx, pushSQLIntent(run)))
			var query string
			var value any
			switch corruption {
			case "binding JSON":
				query, value = "UPDATE replication_push_runs SET binding_json=?", "{"
			case "target binding":
				query, value = "UPDATE replication_push_runs SET target_key=?", "different-target"
			case "run phase":
				query, value = "UPDATE replication_push_runs SET phase=?", "invented"
			case "item JSON":
				query, value = "UPDATE replication_push_items SET document=?", "{"
			case "operation identity":
				query, value = "UPDATE replication_push_items SET version=?", 2
			default:
				if corruption == "item phase" {
					intent.Phase = "invented"
				} else {
					intent.Source.Actual.SHA256 = "missing"
				}
				document, err := json.Marshal(intent)
				storekit.Must(t, err)
				fingerprint := storekit.Get[string](t)(model.PushIntentFingerprint(intent))
				_, err = s.DB().ExecContext(ctx, "UPDATE replication_push_items SET document=?, phase=?, intent_fingerprint=?", string(document), intent.Phase, fingerprint)
				storekit.Must(t, err)
			}
			if query != "" {
				_, err := s.DB().ExecContext(ctx, query, value)
				storekit.Must(t, err)
			}
			if corruption == "binding JSON" || corruption == "target binding" || corruption == "run phase" {
				got, err := progress.Run(ctx, run.Reference)
				if !errors.Is(err, kernel.ErrStore) || got.Reference.ID != "" {
					t.Fatal("corrupt run became usable progress", err, got)
				}
			} else {
				got, err := progress.Item(ctx, run.Reference, intent.Action, intent.Path)
				if !errors.Is(err, kernel.ErrStore) || got.Run.ID != "" {
					t.Fatal("corrupt item became usable proof", err, got)
				}
				rows, next, err := progress.Items(ctx, run.Reference, true, kernel.Page{Limit: 1})
				if !errors.Is(err, kernel.ErrStore) || rows != nil || next != "" {
					t.Fatal("corrupt page became empty recovery success", err, rows, next)
				}
			}
		})
	}
}

func TestAccPushStoreRejectsMalformedRecoveryReferences(t *testing.T) {
	s := fedsql.New(sqltest.OpenSQLite(t, fedsql.Module))
	p, ctx := s.PushProgress(), t.Context()
	run := storekit.Get[model.PushRun](t)(p.Create(ctx, "run", pushSQLBinding(), time.Unix(1, 0)))
	_, err := p.Create(ctx, "", pushSQLBinding(), time.Unix(1, 0))
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = p.Run(ctx, model.PushReference{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = p.Item(ctx, run.Reference, model.PushUpload, "../private")
	storekit.WantErr(t, err, kernel.ErrInvalid)
	_, err = p.CreateItem(ctx, model.PushItem{})
	storekit.WantErr(t, err, kernel.ErrInvalid)
	for _, tc := range []struct {
		ref  model.PushReference
		page kernel.Page
		want error
	}{
		{model.PushReference{}, kernel.Page{Limit: 1}, kernel.ErrInvalid},
		{run.Reference, kernel.Page{Limit: 1, After: kernel.Cursor("a", "b")}, kernel.ErrInvalidCursor},
	} {
		rows, next, err := p.Items(ctx, tc.ref, true, tc.page)
		if !errors.Is(err, tc.want) || rows != nil || next != "" {
			t.Fatal("invalid recovery cursor became exhausted page", err, rows, next)
		}
	}
}
