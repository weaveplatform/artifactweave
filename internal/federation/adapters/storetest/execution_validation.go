package storetest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func executionValidation(t *testing.T, db port.Store) {
	t.Helper()
	t.Run("invalid admission leaves occurrence available", func(t *testing.T) { executionAdmissionValidation(t, db) })
	t.Run("invalid evidence preserves selected proof", func(t *testing.T) { executionEvidenceValidation(t, db) })
	t.Run("invalid private options permit corrected admission", func(t *testing.T) { executionOptionValidation(t, db) })
}

func executionAdmissionValidation(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	retained := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "retained-validation-run", executionBinding("retained-occurrence"), storekit.At(1)))
	for _, tc := range []struct {
		name   string
		change func(*model.ExecutionBinding)
	}{
		{name: "missing source generation", change: func(b *model.ExecutionBinding) { b.Repository.Generation = "" }},
		{name: "unknown direction", change: func(b *model.ExecutionBinding) { b.Direction = "unknown" }},
		{name: "missing credential restoration", change: func(b *model.ExecutionBinding) { b.Credential.TokenID = "" }},
		{name: "escaping scope", change: func(b *model.ExecutionBinding) { b.Root = "../outside" }},
		{name: "one-shot scheduled occurrence", change: func(b *model.ExecutionBinding) { b.OneShot = true }},
		{name: "invalid target lease", change: func(b *model.ExecutionBinding) { b.TargetLeaseKey = "unbound-target" }},
		{name: "negative selection cap", change: func(b *model.ExecutionBinding) { b.SelectionLimit = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			binding := executionBinding("unclaimed-occurrence-" + tc.name)
			invalid := binding
			tc.change(&invalid)
			id := "rejected-admission-" + tc.name
			got, err := progress.Create(ctx, id, invalid, storekit.At(2))
			storekit.WantErr(t, err, kernel.ErrInvalid)
			storekit.Same(t, "invalid admission returns no run", model.ExecutionRun{}, got)
			got, err = progress.ForOccurrence(ctx, binding.Occurrence)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			storekit.Same(t, "invalid admission claims no occurrence", model.ExecutionRun{}, got)
			storekit.Same(t, "invalid admission preserves retained execution", retained,
				storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, retained.Reference)))
			corrected := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, id, binding, storekit.At(3)))
			storekit.Same(t, "corrected admission retains complete provenance", binding, corrected.Binding)
			storekit.Same(t, "corrected admission claims the original occurrence", corrected,
				storekit.Get[model.ExecutionRun](t)(progress.ForOccurrence(ctx, binding.Occurrence)))
		})
	}
}

func executionEvidenceValidation(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	binding := executionBinding("")
	binding.Root = "selected"
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "evidence-validation-run", binding, storekit.At(1)))
	proof := effectFor(run, "selected/file")
	recordEffect(t, db, proof)
	retained := storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference))
	for _, invalidProgress := range []string{"unknown phase", "clock before admission", "unfinished completion"} {
		t.Run(invalidProgress, func(t *testing.T) {
			next := retained
			next.Version++
			switch invalidProgress {
			case "unknown phase":
				next.Phase = "retired"
			case "clock before admission":
				next.Updated = run.Started.Add(-time.Second)
			case "unfinished completion":
				next.Phase, next.Completed = model.ExecutionComplete, next.Updated
			}
			err := db.Tx(t.Context(), func(ctx context.Context) error { return progress.Advance(ctx, retained.Version, next) })
			storekit.WantErr(t, err, kernel.ErrInvalid)
			storekit.Same(t, "invalid dispatch transition preserves pending effect and counters", retained,
				storekit.Get[model.ExecutionRun](t)(progress.Run(t.Context(), run.Reference)))
		})
	}
	other := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "other-evidence-run", binding, storekit.At(1)))
	foreign := run.Reference
	foreign.Incarnation = "another-execution-lifetime"
	for _, tc := range []struct {
		name string
		ref  model.ExecutionReference
	}{
		{name: "foreign lifetime cannot read proof", ref: foreign},
		{name: "another execution cannot read proof", ref: other.Reference},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := progress.Effect(t.Context(), tc.ref, proof.OperationKey)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			storekit.Same(t, "another execution cannot read retained proof", model.ExecutionEffect{}, got)
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*model.ExecutionEffect)
		want   error
	}{
		{name: "another execution lifetime", change: func(e *model.ExecutionEffect) { e.Run = foreign }, want: kernel.ErrNotFound},
		{name: "another transfer identity", change: func(e *model.ExecutionEffect) { e.TransferID = "another-transfer" }, want: kernel.ErrInvalid},
		{name: "invalid intent fingerprint", change: func(e *model.ExecutionEffect) { e.Fingerprint = "unbound-intent" }, want: kernel.ErrInvalid},
		{name: "escaping selection", change: func(e *model.ExecutionEffect) { e.Path = "outside/file" }, want: kernel.ErrInvalid},
		{name: "escaping actual proof", change: func(e *model.ExecutionEffect) { e.Paths = append(e.Paths, "outside/companion") }, want: kernel.ErrInvalid},
		{name: "success without actual proof", change: func(e *model.ExecutionEffect) { e.Paths = nil }, want: kernel.ErrInvalid},
		{name: "stale completion", change: func(e *model.ExecutionEffect) { e.Version = proof.Version }, want: kernel.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			invalid := kernel.Clone(proof)
			invalid.Version++
			invalid.Outcome, invalid.Paths, invalid.Updated = model.EffectSucceeded, []string{proof.Path}, storekit.At(3)
			tc.change(&invalid)
			err := db.Tx(ctx, func(ctx context.Context) error { return progress.RecordEffect(ctx, invalid) })
			storekit.WantErr(t, err, tc.want)
			storekit.Same(t, "refused proof preserves exact execution counters", retained,
				storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
			storekit.Same(t, "refused proof preserves pending driver evidence", proof,
				storekit.Get[model.ExecutionEffect](t)(progress.Effect(ctx, run.Reference, proof.OperationKey)))
			count, present, err := progress.SelectedPaths(ctx, run.Reference, proof.Path)
			storekit.Must(t, err)
			if count != 1 || !present {
				t.Fatal("refused proof changed selected paths", count, present)
			}
		})
	}
	settled := kernel.Clone(proof)
	settled.Version++
	settled.Outcome, settled.Paths, settled.Updated = model.EffectSucceeded, []string{proof.Path, proof.Path + ".sha256"}, storekit.At(3)
	recordEffect(t, db, settled)
	want := retained
	want.Version++
	want.Updated, want.Outcomes = settled.Updated, model.ExecutionOutcomes{Succeeded: 1}
	storekit.Same(t, "valid recovered proof commits after refused evidence", settled,
		storekit.Get[model.ExecutionEffect](t)(progress.Effect(ctx, run.Reference, proof.OperationKey)))
	storekit.Same(t, "valid recovery counts the selected intent once", want,
		storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	storekit.Same(t, "foreign execution retains its own empty counts", other,
		storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, other.Reference)))
}

func executionOptionValidation(t *testing.T, db port.Store) {
	t.Helper()
	ctx, progress := t.Context(), db.Executions()
	binding := executionBinding("")
	binding.OneShot = true
	options := model.ExecutionOptions{ // #nosec G101 -- fixture-only credential
		URL: "https://peer.invalid/artifactory/target", Password: "private-options-validation", SocketTimeout: time.Second,
	}
	for _, tc := range []struct {
		name   string
		change func(*model.ExecutionOptions)
	}{
		{name: "malformed private text", change: func(o *model.ExecutionOptions) { o.Password += "\x00" }},
		{name: "negative timeout", change: func(o *model.ExecutionOptions) { o.SocketTimeout = -time.Second }},
		{name: "incomplete independent target", change: func(o *model.ExecutionOptions) { o.Independent = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "invalid-options-"+tc.name, binding, storekit.At(1)))
			invalid := options
			tc.change(&invalid)
			err := db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, invalid) })
			storekit.WantErr(t, err, kernel.ErrInvalid)
			if strings.Contains(err.Error(), options.Password) {
				t.Fatal("private option validation disclosed the credential")
			}
			got, err := progress.Options(ctx, run.Reference)
			storekit.WantErr(t, err, kernel.ErrNotFound)
			storekit.Same(t, "invalid options leave no private row", model.ExecutionOptions{}, got)
			storekit.Same(t, "invalid options preserve pending admission", run,
				storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
			storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, options) }))
			storekit.Same(t, "corrected private options recover on the same lifetime", options,
				storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
			storekit.Same(t, "corrected options preserve the original admission", run,
				storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
		})
	}
	run := storekit.Get[model.ExecutionRun](t)(progress.Create(ctx, "reference-validation-run", binding, storekit.At(1)))
	storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error { return progress.PutOptions(ctx, run.Reference, options) }))
	for _, tc := range []struct {
		name string
		ref  model.ExecutionReference
	}{
		{name: "missing lifetime", ref: model.ExecutionReference{ID: run.Reference.ID}},
		{name: "malformed identifier", ref: model.ExecutionReference{ID: run.Reference.ID + "\x00", Incarnation: run.Reference.Incarnation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := progress.Run(t.Context(), tc.ref)
			storekit.WantErr(t, err, kernel.ErrInvalid)
			storekit.Same(t, "malformed reference returns no provenance", model.ExecutionRun{}, got)
			private, err := progress.Options(t.Context(), tc.ref)
			storekit.WantErr(t, err, kernel.ErrInvalid)
			storekit.Same(t, "malformed reference returns no private options", model.ExecutionOptions{}, private)
		})
	}
	storekit.Same(t, "malformed references preserve exact run", run,
		storekit.Get[model.ExecutionRun](t)(progress.Run(ctx, run.Reference)))
	storekit.Same(t, "valid reference retains private options after refusal", options,
		storekit.Get[model.ExecutionOptions](t)(progress.Options(ctx, run.Reference)))
}
