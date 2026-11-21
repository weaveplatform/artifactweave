package model_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func pullRun(t *testing.T) model.PullRun {
	t.Helper()
	run, err := model.NewPullRun("logical-pull", model.PullBinding{
		TargetKey: "target", TargetRevision: "current-configuration",
		Source: artifactsapi.PullRepository{Repository: artifactsapi.TransferRepository{
			Key: "remote", Incarnation: "remote-lifetime", Generation: "generation", PackageType: repo.Generic,
		}, Revision: strings.Repeat("a", 64)},
		Credential: kernel.CredentialReference{Name: "original-reader", TokenID: "token-reference"}, Root: "selected", SyncDeletes: true,
	}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func pullIntent(run model.PullRun) model.PullItem {
	return model.PullItem{
		Run: run.Reference, Action: model.PullFetch, Path: "selected/a", Version: 1,
		Phase: model.PullIntent, Updated: run.Started,
		Source: &artifactsapi.PullItem{
			Repository: run.Binding.Source, Selection: run.Binding.Selection(),
			Path: "selected/a", UpstreamPath: "source/a", Size: 4, Actual: kernel.Checksums{SHA256: strings.Repeat("b", 64)}, Version: strings.Repeat("c", 64),
		},
	}
}

func TestUnitPullIntentSuccessNeedsAnExactPersistedOwnerReceipt(t *testing.T) {
	run := pullRun(t)
	intent := pullIntent(run)
	if err := model.ValidatePullItemCreation(run, intent); err != nil {
		t.Fatal(err)
	}
	settled := intent
	settled.Version++
	settled.Phase, settled.Outcome = model.PullItemComplete, model.PullSucceeded
	if err := model.ValidatePullItemAdvance(run, intent, settled); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("selected facts alone became successful cache progress", err)
	}
	settled.Cache = &artifactsapi.PullCached{Item: *intent.Source, Representation: strings.Repeat("d", 64), Version: strings.Repeat("e", 64)}
	if err := model.ValidatePullItemAdvance(run, intent, settled); err != nil {
		t.Fatal("verified owner receipt refused", err)
	}
	settled.Cache.Item.Size++
	if err := model.ValidatePullItemAdvance(run, intent, settled); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("different bytes borrowed another cache receipt", err)
	}
}

func TestUnitPullTraversalAndDeleteEvidenceCannotRebindOrEscapeScope(t *testing.T) {
	run := pullRun(t)
	next := run
	next.Version++
	next.SourceDone, next.Phase = true, model.PullReconcile
	if err := model.ValidatePullAdvance(run, next); err != nil {
		t.Fatal(err)
	}
	changed := next
	changed.Binding.Credential.TokenID = "new-capability"
	if err := model.ValidatePullAdvance(run, changed); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("old run rebound to new credentials", err)
	}
	i := model.PullItem{
		Run: next.Reference, Action: model.PullDelete, Path: "selected/a", Version: 1, Phase: model.PullIntent, Updated: next.Started,
		Target: &artifactsapi.PullCacheCandidate{Repository: next.Binding.Source, Selection: next.Binding.Selection(), Path: "selected/a", Version: strings.Repeat("f", 64)},
	}
	if err := model.ValidatePullItemCreation(next, i); err != nil {
		t.Fatal(err)
	}
	settled := i
	settled.Version++
	settled.Phase, settled.Outcome, settled.Removed = model.PullItemComplete, model.PullSucceeded, []string{"selected/a"}
	if err := model.ValidatePullItemAdvance(next, i, settled); err != nil {
		t.Fatal(err)
	}
	settled.Removed = append(settled.Removed, "outside/sibling")
	if err := model.ValidatePullItemAdvance(next, i, settled); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("deletion evidence escaped its root", err)
	}
	settled = i
	settled.Version++
	settled.Phase, settled.Outcome, settled.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
	if err := model.ValidatePullItemAdvance(next, i, settled); err != nil {
		t.Fatal("sanitized refusal lost recoverable evidence", err)
	}
}
