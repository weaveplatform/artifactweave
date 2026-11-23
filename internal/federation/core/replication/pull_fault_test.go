package replication_test

import (
	"context"
	"errors"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type pullFaultStore struct {
	port.PullStore
	progress port.PullProgressStore
}

func (s pullFaultStore) PullProgress() port.PullProgressStore { return s.progress }

type pullFaultProgress struct {
	port.PullProgressStore
	mode string
}

func (p pullFaultProgress) Items(ctx context.Context, ref model.PullReference, pending bool, page kernel.Page) ([]model.PullItem, string, error) {
	if p.mode == "pending" {
		return nil, "", kernel.ErrStore
	}
	return p.PullProgressStore.Items(ctx, ref, pending, page)
}

func (p pullFaultProgress) NextPending(ctx context.Context, ref model.PullReference) (model.PullItem, bool, error) {
	if p.mode == "pending" {
		return model.PullItem{}, false, kernel.ErrStore
	}
	return p.PullProgressStore.NextPending(ctx, ref)
}

func (p pullFaultProgress) CreateItem(ctx context.Context, i model.PullItem) (model.PullItem, error) {
	if p.mode == "intent" {
		return model.PullItem{}, kernel.ErrStore
	}
	return p.PullProgressStore.CreateItem(ctx, i)
}

func (p pullFaultProgress) Advance(ctx context.Context, expected int64, run model.PullRun) error {
	if p.mode == "cursor" {
		return kernel.ErrStore
	}
	return p.PullProgressStore.Advance(ctx, expected, run)
}

func (p pullFaultProgress) AdvanceItem(ctx context.Context, expected int64, i model.PullItem) error {
	if p.mode == "receipt" {
		return kernel.ErrStore
	}
	return p.PullProgressStore.AdvanceItem(ctx, expected, i)
}

func TestUnitPullProgressFailuresCannotCommitEnumerationOrCacheEffects(t *testing.T) {
	for _, mode := range []string{"pending", "intent", "cursor", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			w, _, run := newPullWorld(t, false)
			w.add(run, "selected/a")
			if mode == "receipt" {
				var err error
				run, err = w.driver.Step(t.Context(), run.Reference, pullFence)
				must(t, err)
			}
			w.driver.Store = pullFaultStore{PullStore: w.db, progress: pullFaultProgress{PullProgressStore: w.db.PullProgress(), mode: mode}}
			if _, err := w.driver.Step(t.Context(), run.Reference, pullFence); !errors.Is(err, kernel.ErrStore) {
				t.Fatal("progress fault became success or a safe source refusal", err)
			}
			current, err := w.db.PullProgress().Run(t.Context(), run.Reference)
			must(t, err)
			if current != run {
				t.Fatal("failed progress write advanced the durable cursor")
			}
			rows, _, err := w.db.PullProgress().Items(t.Context(), run.Reference, true, kernel.Page{})
			must(t, err)
			if mode == "receipt" {
				if len(rows) != 1 || rows[0].Cache != nil || rows[0].Phase != model.PullIntent {
					t.Fatal("failed receipt became completion")
				}
			} else if len(rows) != 0 {
				t.Fatal("failed enumeration left an intent without its cursor")
			}
			var cached bool
			if err := w.platform.Settings().Get(t.Context(), "cache-effect:selected/a", &cached); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("cache effect survived its rejected receipt", err)
			}
		})
	}
}

func TestUnitPullAdmissionAndRestorationFailuresCannotReachPeerWork(t *testing.T) {
	if _, err := (replication.Pull{}).Start(t.Context(), replication.PullRequest{}, nil); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing direction contracts accepted", err)
	}
	for _, mode := range []string{"anonymous admission", "bad root", "rejected admission", "missing lifetime", "restore denied", "nil restoration"} {
		t.Run(mode, func(t *testing.T) {
			w, ctx, run := newPullWorld(t, false)
			request := replication.PullRequest{ID: "new-run", TargetKey: run.Binding.TargetKey, TargetRevision: run.Binding.TargetRevision, Root: "selected"}
			var err error
			switch mode {
			case "anonymous admission":
				_, err = w.driver.Start(t.Context(), request, nil)
			case "bad root":
				request.Root = "../outside"
				_, err = w.driver.Start(ctx, request, nil)
			case "rejected admission":
				var rejected model.PullReference
				_, err = w.driver.Start(ctx, request, func(_ context.Context, r model.PullRun) error { rejected = r.Reference; return kernel.ErrStore })
				if _, readErr := w.db.PullProgress().Run(ctx, rejected); !errors.Is(readErr, kernel.ErrNotFound) {
					t.Fatal("failed admission retained a logical lifetime", readErr)
				}
			case "missing lifetime":
				_, err = w.driver.Step(ctx, model.PullReference{ID: "missing", Incarnation: "missing"}, pullFence)
			case "restore denied":
				w.driver.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) {
					return nil, kernel.ErrBadCredentials
				}
				_, err = w.driver.Step(ctx, run.Reference, pullFence)
			case "nil restoration":
				w.driver.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) { return nil, nil }
				_, err = w.driver.Step(ctx, run.Reference, pullFence)
			}
			if err == nil || w.pages != 0 || w.fetches != 0 || w.deletes != 0 {
				t.Fatal("failed admission/restoration reached peer work", err)
			}
		})
	}
}

var _ artifactsapi.PullSource = pullSource{}
