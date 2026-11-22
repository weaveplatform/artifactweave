package model_test

import (
	"errors"
	"math"
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

func TestUnitPullProvenanceRejectsIncompleteOrUnusableBindings(t *testing.T) {
	for _, ref := range []model.PullReference{{}, {ID: "a"}, {ID: strings.Repeat("a", 257), Incarnation: "b"}} {
		if err := ref.Validate(); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid logical lifetime accepted", err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*model.PullBinding)
	}{
		{"missing source", func(b *model.PullBinding) { b.Source.Repository.Incarnation = "" }},
		{"anonymous", func(b *model.PullBinding) { b.Credential.Anonymous = true }},
		{"registry credential", func(b *model.PullBinding) { b.Credential.Registry = &kernel.RegistryReference{} }},
		{"missing credential", func(b *model.PullBinding) { b.Credential.TokenID = "" }},
		{"source fingerprint", func(b *model.PullBinding) { b.Source.Revision = "unhashed-source" }},
		{"scope traversal", func(b *model.PullBinding) { b.Root = "selected/../other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := pullRun(t).Binding
			tc.edit(&binding)
			if err := binding.Validate(); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("unusable durable pull provenance accepted", err)
			}
		})
	}
}

func TestUnitPullTraversalValidationRejectsFalseCompletionAndRewind(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*model.PullRun)
	}{
		{"invalid lifetime", func(r *model.PullRun) { r.Reference.ID = "" }},
		{"invalid binding", func(r *model.PullRun) { r.Binding.TargetKey = "" }},
		{"clock before admission", func(r *model.PullRun) { r.Updated = r.Started.Add(-time.Second) }},
		{"target before source", func(r *model.PullRun) { r.TargetDone = true }},
		{"reconcile incomplete source", func(r *model.PullRun) { r.Phase = model.PullReconcile }},
		{"complete incomplete traversal", func(r *model.PullRun) { r.Phase, r.Completed = model.PullComplete, r.Updated }},
		{"unknown phase", func(r *model.PullRun) { r.Phase = "future" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := pullRun(t)
			tc.edit(&run)
			if err := run.Validate(); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("invalid durable traversal accepted", err)
			}
			if err := model.ValidatePullAdvance(pullRun(t), run); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("invalid traversal admitted by advance", err)
			}
		})
	}
	run := pullRun(t)
	failed := run
	failed.Version++
	failed.Phase, failed.Completed = model.PullFailed, failed.Updated
	if err := model.ValidatePullAdvance(run, failed); err != nil {
		t.Fatal("failed work must retain a terminal record", err)
	}
	for _, current := range []model.PullRun{failed, {}} {
		if err := model.ValidatePullAdvance(current, failed); !errors.Is(err, kernel.ErrConflict) {
			t.Fatal("invalid or settled lifetime advanced", err)
		}
	}
	run.Version = math.MaxInt64
	if err := model.ValidatePullAdvance(run, failed); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("version overflow advanced", err)
	}
}

func TestUnitPullEvidenceRejectsUnsafeResultsAndReboundIntents(t *testing.T) {
	run := pullRun(t)
	for _, tc := range []struct {
		name string
		edit func(*model.PullItem)
	}{
		{"invalid reference", func(i *model.PullItem) { i.Run.ID = "" }},
		{"foreign lifetime", func(i *model.PullItem) { i.Run.Incarnation = "new-lifetime" }},
		{"source absent", func(i *model.PullItem) { i.Source = nil }},
		{"delete lacks target", func(i *model.PullItem) { i.Action, i.Source = model.PullDelete, nil }},
		{"unknown action", func(i *model.PullItem) { i.Action = "future" }},
		{"unknown phase", func(i *model.PullItem) { i.Phase = "future" }},
		{"pending terminal evidence", func(i *model.PullItem) { i.Reason = "source_changed" }},
		{"unsafe failure reason", func(i *model.PullItem) {
			i.Phase, i.Outcome, i.Reason = model.PullItemComplete, model.PullItemFailed, "https://peer.invalid/secret"
		}},
		{"unknown outcome", func(i *model.PullItem) { i.Phase, i.Outcome = model.PullItemComplete, "future" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := pullIntent(run)
			tc.edit(&intent)
			if err := intent.Validate(run); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("unsafe or unbound evidence accepted", err)
			}
			if err := model.ValidatePullItemCreation(run, intent); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("invalid intent persisted", err)
			}
			if err := model.ValidatePullItemAdvance(run, intent, intent); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("invalid current evidence advanced", err)
			}
		})
	}
	intent := pullIntent(run)
	settled := kernel.Clone(intent)
	settled.Version++
	settled.Phase, settled.Outcome, settled.Reason = model.PullItemComplete, model.PullSkipped, "source_changed"
	if err := model.ValidatePullItemAdvance(run, intent, model.PullItem{}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("empty next evidence accepted", err)
	}
	changed := kernel.Clone(settled)
	changed.Source.Size++
	if err := model.ValidatePullItemAdvance(run, intent, changed); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("changed immutable source accepted", err)
	}
	run.SourceDone = true
	if err := model.ValidatePullItemCreation(run, intent); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("new fetch admitted after source traversal ended", err)
	}
	for _, invalid := range []struct {
		action model.PullAction
		path   string
	}{{"future", "selected/a"}, {model.PullFetch, "../outside"}} {
		if _, err := model.PullItemKey(invalid.action, invalid.path); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid recovery key accepted", err)
		}
	}
	bad := kernel.Clone(intent)
	bad.Source.Modified = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := model.PullIntentFingerprint(bad); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unencodable upstream facts received a fingerprint", err)
	}
	if err := model.ValidatePullItemAdvance(run, bad, settled); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unencodable current facts advanced", err)
	}
	badNext := kernel.Clone(settled)
	badNext.Source.Modified = bad.Source.Modified
	if err := model.ValidatePullItemAdvance(run, intent, badNext); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unencodable next facts advanced", err)
	}
}

func TestUnitPullDeletionEvidenceRequiresExactBoundedRemovalProof(t *testing.T) {
	run := pullRun(t)
	run.SourceDone, run.Phase = true, model.PullReconcile
	intent := model.PullItem{
		Run: run.Reference, Action: model.PullDelete, Path: "selected/a", Version: 1, Phase: model.PullIntent, Updated: run.Started,
		Target: &artifactsapi.PullCacheCandidate{Repository: run.Binding.Source, Selection: run.Binding.Selection(), Path: "selected/a", Version: strings.Repeat("f", 64)},
	}
	for _, paths := range [][]string{nil, {"selected/other"}, {"selected/a", "selected/a"}, {"selected/a", "../escape"}} {
		settled := kernel.Clone(intent)
		settled.Version++
		settled.Phase, settled.Outcome, settled.Removed = model.PullItemComplete, model.PullSucceeded, paths
		if err := model.ValidatePullItemAdvance(run, intent, settled); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("unproved/duplicate removal acknowledged", paths, err)
		}
	}
}

func TestUnitPullRetainedEvidenceIsBenignOnlyForUnchangedDeletionIntents(t *testing.T) {
	run := pullRun(t)
	run.SourceDone, run.Phase = true, model.PullReconcile
	intent := model.PullItem{
		Run: run.Reference, Action: model.PullDelete, Path: "selected/a", Version: 1, Phase: model.PullIntent, Updated: run.Started,
		Target: &artifactsapi.PullCacheCandidate{Repository: run.Binding.Source, Selection: run.Binding.Selection(), Path: "selected/a", Version: strings.Repeat("f", 64)},
	}
	retained := kernel.Clone(intent)
	retained.Version++
	retained.Phase, retained.Outcome = model.PullItemComplete, model.PullRetained
	if err := model.ValidatePullItemAdvance(run, intent, retained); err != nil {
		t.Fatal("fenced retention was refused", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*model.PullItem)
	}{
		{"fabricated deletion", func(i *model.PullItem) { i.Removed = []string{i.Path} }},
		{"refusal reason", func(i *model.PullItem) { i.Reason = "source_changed" }},
		{"fabricated cache receipt", func(i *model.PullItem) { i.Cache = &artifactsapi.PullCached{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := kernel.Clone(retained)
			tc.edit(&bad)
			if err := model.ValidatePullItemAdvance(run, intent, bad); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("retention carried effect or refusal evidence", err)
			}
		})
	}
	fetch := pullIntent(run)
	fetch.Version++
	fetch.Phase, fetch.Outcome = model.PullItemComplete, model.PullRetained
	if err := fetch.Validate(run); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("fetch used retention instead of a byte receipt", err)
	}
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
