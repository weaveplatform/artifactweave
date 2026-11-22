package inmem

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func TestUnitPullStoredCorruptionCannotAuthorizeRecoveryOrRetry(t *testing.T) {
	for _, mode := range []string{"run", "intent"} {
		t.Run(mode, func(t *testing.T) {
			s := New(memtx.New())
			ctx, p := t.Context(), s.PullProgress()
			binding := model.PullBinding{
				TargetKey:      "target",
				TargetRevision: "target-revision",
				Root:           "selected",
				Credential:     kernel.CredentialReference{Name: "author", TokenID: "verified"},
				Source: artifactsapi.PullRepository{
					Repository: artifactsapi.TransferRepository{Key: "remote", Incarnation: "lifetime", Generation: "generation", PackageType: repo.Generic},
					Revision:   strings.Repeat("a", 64),
				},
			}
			run, err := p.Create(ctx, "run", binding, time.Unix(1, 0))
			if err != nil {
				t.Fatal(err)
			}
			i := model.PullItem{
				Run:     run.Reference,
				Action:  model.PullFetch,
				Path:    "selected/a",
				Phase:   model.PullIntent,
				Version: 1,
				Updated: run.Started,
				Source: &artifactsapi.PullItem{
					Repository:   binding.Source,
					Selection:    binding.Selection(),
					Path:         "selected/a",
					UpstreamPath: "source/a",
					Size:         4,
					Actual:       kernel.Checksums{SHA256: strings.Repeat("b", 64)},
					Version:      strings.Repeat("c", 64),
				},
			}
			if _, err := p.CreateItem(ctx, i); err != nil {
				t.Fatal(err)
			}
			if err := s.Do(ctx, func() error {
				if mode == "run" {
					bad := run
					bad.Phase = "invented"
					memtx.TableOf[string, model.PullRun](s.Store, "replication_pull_runs").Put(run.Reference.ID, bad)
				} else {
					bad := kernel.Clone(i)
					bad.Source.Actual.SHA256 = "invalid"
					key, err := model.PullItemKey(i.Action, i.Path)
					if err != nil {
						return err
					}
					memtx.TableOf[pullItemKey, model.PullItem](s.Store, "replication_pull_items").Put(pullItemKey{run.Reference.ID, key}, bad)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if mode == "run" {
				got, err := p.Run(ctx, run.Reference)
				if !errors.Is(err, kernel.ErrStore) || got.Reference.ID != "" {
					t.Fatal("corrupt lifetime became usable", err)
				}
				if _, err := p.Create(ctx, "run", binding, time.Unix(2, 0)); !errors.Is(err, kernel.ErrStore) {
					t.Fatal("retry repaired corrupt lifetime silently", err)
				}
				next := run
				next.Version++
				if err := s.Tx(ctx, func(ctx context.Context) error { return p.Advance(ctx, run.Version, next) }); !errors.Is(err, kernel.ErrStore) {
					t.Fatal("corrupt run advanced", err)
				}
			}
			if _, err := p.CreateItem(ctx, i); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("retry borrowed corrupt evidence", err)
			}
			if got, err := p.Item(ctx, i.Run, i.Action, i.Path); !errors.Is(err, kernel.ErrStore) || got.Run.ID != "" {
				t.Fatal("corrupt evidence became a receipt", err)
			}
			if rows, next, err := p.Items(ctx, i.Run, true, kernel.Page{Limit: 1}); !errors.Is(err, kernel.ErrStore) || rows != nil || next != "" {
				t.Fatal("corruption became exhausted recovery", err)
			}
			settled := i
			settled.Version++
			settled.Phase, settled.Outcome, settled.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
			if err := s.Tx(ctx, func(ctx context.Context) error { return p.AdvanceItem(ctx, i.Version, settled) }); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("corrupt evidence was acknowledged", err)
			}
		})
	}
}
