package model

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func pushFixture(t *testing.T) PushRun {
	t.Helper()
	r, err := NewPushRun("logical-run", PushBinding{
		TargetKey: "target", TargetRevision: "target-revision", TransportRevision: "transport-revision",
		Source:     artifactsapi.TransferRepository{Key: "source", Incarnation: "source-lifetime", Generation: "source-revision", PackageType: repo.Generic},
		Credential: kernel.CredentialReference{Name: "reader", TokenID: "source-token-id"},
		Root:       "selected", Include: "**", SyncDeletes: true, SyncProperties: true,
	}, time.Date(2026, 11, 21, 1, 0, 0, 123456789, time.FixedZone("fixture", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnitPushRunKeepsIndependentDurableTraversal(t *testing.T) {
	r := pushFixture(t)
	if r.Version != 1 || r.Reference.Incarnation == "" || r.Started.Nanosecond() != 123456000 || r.Started.Location() != time.UTC {
		t.Fatal("initial identity or stored clock is not canonical", r)
	}
	advance := func(edit func(*PushRun)) {
		t.Helper()
		next := kernel.Clone(r)
		next.Version++
		next.Updated = next.Updated.Add(time.Second)
		edit(&next)
		if err := ValidatePushAdvance(r, next); err != nil {
			t.Fatal(err)
		}
		r = next
	}
	advance(func(r *PushRun) { r.SourceCursor = "source-page" })
	advance(func(r *PushRun) { r.SourceCursor, r.SourceDone = "", true })
	// SourceDone while still copying allows pending item intents to settle
	// before reconciliation begins. Empty cursor alone cannot express this.
	if r.Phase != PushCopy || !r.SourceDone || r.TargetDone {
		t.Fatal("exhausted source confused with completed target", r)
	}
	advance(func(r *PushRun) { r.Phase = PushReconcile })
	advance(func(r *PushRun) { r.TargetCursor = "target-page" })
	advance(func(r *PushRun) { r.TargetCursor, r.TargetDone = "", true })
	advance(func(r *PushRun) { r.Phase, r.Completed = PushComplete, r.Updated })
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored PushRun
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.Validate(); err != nil || restored.Reference != r.Reference || !restored.SourceDone || !restored.TargetDone {
		t.Fatal("persisted run lost its identity or terminal traversal", restored, err)
	}
	next := restored
	next.Version++
	next.Updated = next.Updated.Add(time.Second)
	if err := ValidatePushAdvance(restored, next); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("terminal run could be advanced again", err)
	}
}

func TestUnitPushRunRejectsRebindingAndPhaseReopening(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PushRun)
	}{
		{"stale version", func(r *PushRun) { r.Version-- }},
		{"different logical run", func(r *PushRun) { r.Reference.ID = "another-run" }},
		{"recreated lifetime", func(r *PushRun) { r.Reference.Incarnation = "another-lifetime" }},
		{"rotated target", func(r *PushRun) { r.Binding.TargetRevision = "rotated" }},
		{"rotated transport", func(r *PushRun) { r.Binding.TransportRevision = "rotated" }},
		{"rotated source", func(r *PushRun) { r.Binding.Source.Generation = "rotated" }},
		{"different caller", func(r *PushRun) { r.Binding.Credential.TokenID = "another-token" }},
		{"widened scope", func(r *PushRun) { r.Binding.Root = "" }},
		{"different options", func(r *PushRun) { r.Binding.SyncStatistics = true }},
		{"reopened traversal", func(r *PushRun) { r.SourceDone = false }},
		{"skipped reconciliation", func(r *PushRun) { r.Phase, r.TargetDone, r.Completed = PushComplete, true, r.Updated }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := pushFixture(t)
			current.SourceDone = true
			next := current
			next.Version++
			next.Updated = next.Updated.Add(time.Second)
			tc.edit(&next)
			if err := ValidatePushAdvance(current, next); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("changed execution binding or stale progress accepted", err)
			}
		})
	}
	current := pushFixture(t)
	current.Version = math.MaxInt64
	if err := ValidatePushAdvance(current, current); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("push version exhausted without refusing progress", err)
	}
}

func TestUnitPushRunRejectsMalformedBindingsAndProgress(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PushRun)
	}{
		{"empty id", func(r *PushRun) { r.Reference.ID = "" }},
		{"oversized id", func(r *PushRun) { r.Reference.ID = strings.Repeat("x", 257) }},
		{"binary id", func(r *PushRun) { r.Reference.ID = "run\x00id" }},
		{"empty target revision", func(r *PushRun) { r.Binding.TargetRevision = "" }},
		{"empty transport revision", func(r *PushRun) { r.Binding.TransportRevision = "" }},
		{"unbound source", func(r *PushRun) { r.Binding.Source.Incarnation = "" }},
		{"anonymous caller", func(r *PushRun) { r.Binding.Credential.Anonymous = true }},
		{"unverified caller", func(r *PushRun) { r.Binding.Credential.TokenID = "" }},
		{"registry capability", func(r *PushRun) { r.Binding.Credential.Registry = &kernel.RegistryReference{} }},
		{"absolute root", func(r *PushRun) { r.Binding.Root = "/selected" }},
		{"parent traversal", func(r *PushRun) { r.Binding.Root = "selected/../private" }},
		{"binary pattern", func(r *PushRun) { r.Binding.Include = string([]byte{0xff}) }},
		{"invalid version", func(r *PushRun) { r.Version = 0 }},
		{"missing start", func(r *PushRun) { r.Started = time.Time{} }},
		{"done cursor still open", func(r *PushRun) { r.SourceDone, r.SourceCursor = true, "more" }},
		{"target ahead of copy", func(r *PushRun) { r.TargetCursor = "more" }},
		{"source incomplete", func(r *PushRun) { r.Phase = PushReconcile }},
		{"incomplete terminal time", func(r *PushRun) { r.Phase = PushFailed }},
		{"unknown phase", func(r *PushRun) { r.Phase = "invented" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pushFixture(t)
			tc.edit(&r)
			if err := r.Validate(); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("malformed execution state accepted", err)
			}
		})
	}
}

func pushIntent(t *testing.T, run PushRun) PushItem {
	t.Helper()
	return PushItem{
		Run: run.Reference, Action: PushUpload, Path: "selected/item.pkg", Version: 1, Phase: PushIntent, Updated: run.Started,
		Source: &artifactsapi.TransferItem{
			Repository: run.Binding.Source, ID: 7, Path: "selected/item.pkg", Size: 6,
			Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Version: strings.Repeat("b", 64),
			PropertiesIncluded: true, Properties: map[string][]string{"quality": {"verified"}},
		},
	}
}

func TestUnitPushItemPreservesIntentAndRequiresVerification(t *testing.T) {
	run := pushFixture(t)
	current := pushIntent(t, run)
	if err := current.Validate(run); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := PushIntentFingerprint(current)
	if err != nil {
		t.Fatal(err)
	}
	advance := func(phase PushItemPhase, outcome PushOutcome) {
		t.Helper()
		next := kernel.Clone(current)
		next.Version++
		next.Phase, next.Outcome, next.Updated = phase, outcome, next.Updated.Add(time.Second)
		next.VerifiedTarget = &PushTargetSnapshot{Actual: current.Source.Actual, Size: current.Source.Size}
		if err := ValidatePushItemAdvance(run, current, next); err != nil {
			t.Fatal(err)
		}
		current = next
	}
	advance(PushContentVerified, "")
	advance(PushMetadataVerified, "")
	advance(PushItemComplete, PushSucceeded)
	if got, err := PushIntentFingerprint(current); err != nil || got != fingerprint {
		t.Fatal("verification changed immutable intent identity", got, fingerprint, err)
	}
	next := current
	next.Version++
	if err := ValidatePushItemAdvance(run, current, next); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("completed item acknowledged twice", err)
	}
	uploadKey, err := PushItemKey(PushUpload, current.Path)
	if err != nil {
		t.Fatal(err)
	}
	deleteKey, err := PushItemKey(PushDelete, current.Path)
	if err != nil || uploadKey == deleteKey || len(uploadKey) != 64 {
		t.Fatal("operation identity collided or was not bounded", uploadKey, deleteKey, err)
	}
}

func TestUnitPushItemRejectsFalseSuccessAndChangedIntent(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PushItem)
		want error
	}{
		{"missing checksum proof", func(i *PushItem) { i.VerifiedTarget = nil }, kernel.ErrInvalid},
		{"wrong destination checksum", func(i *PushItem) { i.VerifiedTarget.Actual.SHA256 = strings.Repeat("c", 64) }, kernel.ErrInvalid},
		{"wrong destination size", func(i *PushItem) { i.VerifiedTarget.Size++ }, kernel.ErrInvalid},
		{"different source", func(i *PushItem) { i.Source.ID++ }, kernel.ErrConflict},
		{"different properties", func(i *PushItem) { i.Source.Properties["quality"][0] = "changed" }, kernel.ErrConflict},
		{"different run", func(i *PushItem) { i.Run.Incarnation = "other" }, kernel.ErrInvalid},
		{"scope sibling", func(i *PushItem) { i.Path, i.Source.Path = "selected2/item.pkg", "selected2/item.pkg" }, kernel.ErrInvalid},
		{"terminal outcome before verification", func(i *PushItem) { i.Outcome = PushSucceeded }, kernel.ErrInvalid},
		{"skipped metadata proof", func(i *PushItem) { i.Phase, i.Outcome = PushItemComplete, PushSucceeded }, kernel.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := pushFixture(t)
			current := pushIntent(t, run)
			if tc.name == "skipped metadata proof" {
				current.Phase = PushContentVerified
				current.VerifiedTarget = &PushTargetSnapshot{Actual: current.Source.Actual, Size: current.Source.Size}
			}
			next := kernel.Clone(current)
			next.Version++
			next.Phase = PushContentVerified
			next.VerifiedTarget = &PushTargetSnapshot{Actual: next.Source.Actual, Size: next.Source.Size}
			tc.edit(&next)
			if err := ValidatePushItemAdvance(run, current, next); !errors.Is(err, tc.want) {
				t.Fatal("unsafe item transition accepted", err, "want", tc.want)
			}
		})
	}
}

func TestUnitPushDeleteNeedsAbsenceAndFailuresKeepSafeReasons(t *testing.T) {
	run := pushFixture(t)
	run.Phase, run.SourceDone = PushReconcile, true
	intent := PushItem{
		Run: run.Reference, Action: PushDelete, Path: "selected/stale.pkg", Version: 1, Phase: PushIntent, Updated: run.Started,
		Target: &PushTargetSnapshot{Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 6},
	}
	next := intent
	next.Version++
	next.Phase, next.Outcome = PushItemComplete, PushSucceeded
	if err := ValidatePushItemAdvance(run, intent, next); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("delete reported success without absence evidence", err)
	}
	next.TargetAbsent = true
	if err := ValidatePushItemAdvance(run, intent, next); err != nil {
		t.Fatal(err)
	}
	next.TargetAbsent, next.Outcome, next.Reason = false, PushItemFailed, "target_refused"
	if err := ValidatePushItemAdvance(run, intent, next); err != nil {
		t.Fatal("honest refusal cannot be retained", err)
	}
	next.Reason = "https://user:password@target.invalid/private"
	if err := ValidatePushItemAdvance(run, intent, next); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unsafe remote error admitted to checkpoint reason", err)
	}
}
