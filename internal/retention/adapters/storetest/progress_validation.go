package storetest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

// Malformed recovery state must fail before replacing a previously usable
// checkpoint. Exercise the actual store transaction on every backend.
func testCleanupInvalidProgress(t *testing.T, s port.Store, original model.CleanupProgress) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		change func(*model.CleanupProgress)
	}{
		{"missing run", func(p *model.CleanupProgress) { p.RunID = 0 }},
		{"missing revision", func(p *model.CleanupProgress) { p.Revision = 0 }},
		{"unknown phase", func(p *model.CleanupProgress) { p.Phase = "unknown" }},
		{"missing repository", func(p *model.CleanupProgress) { p.Phase = model.CleanupPackages }},
		{"missing package", func(p *model.CleanupProgress) { p.Phase = model.CleanupVersions }},
		{"missing version", func(p *model.CleanupProgress) { p.Phase = model.CleanupRemoving }},
		{"oversized continuation", func(p *model.CleanupProgress) { p.FileAfter = strings.Repeat("a", 8193) }},
		{"invalid UTF8", func(p *model.CleanupProgress) { p.PackageAfter = string([]byte{255}) }},
		{"embedded null", func(p *model.CleanupProgress) { p.RepositoryAfter = "repo\x00other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := original
			tc.change(&bad)
			err := s.Tx(t.Context(), func(ctx context.Context) error { return s.Cleanup().CreateProgress(ctx, bad) })
			storekit.WantErr(t, err, kernel.ErrInvalid)
			storekit.Same(t, "invalid creation preserves checkpoint", original, storekit.Get[model.CleanupProgress](t)(s.Cleanup().Progress(t.Context(), original.RunID)))
		})
	}
	bad := original
	bad.Revision++
	bad.Phase = model.CleanupCandidates
	err := s.Tx(t.Context(), func(ctx context.Context) error { return s.Cleanup().AdvanceProgress(ctx, original.Revision, bad) })
	storekit.WantErr(t, err, kernel.ErrInvalid)
	storekit.Same(t, "invalid advance preserves checkpoint", original, storekit.Get[model.CleanupProgress](t)(s.Cleanup().Progress(t.Context(), original.RunID)))
}

func testCleanupInvalidEntries(t *testing.T, s port.Store, original model.CleanupEntry) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		change func(*model.CleanupEntry)
	}{
		{"missing run", func(e *model.CleanupEntry) { e.RunID = 0 }},
		{"missing entity", func(e *model.CleanupEntry) { e.EntityID = 0 }},
		{"missing version", func(e *model.CleanupEntry) { e.VersionID = 0 }},
		{"missing revision", func(e *model.CleanupEntry) { e.Revision = 0 }},
		{"unknown kind", func(e *model.CleanupEntry) { e.Kind = "folder" }},
		{"unknown state", func(e *model.CleanupEntry) { e.State = "unknown" }},
		{"oversized reason", func(e *model.CleanupEntry) { e.Reason = strings.Repeat("a", 257) }},
		{"invalid UTF8", func(e *model.CleanupEntry) { e.Reason = string([]byte{255}) }},
		{"embedded null", func(e *model.CleanupEntry) { e.Reason = "selected\x00deleted" }},
		{"effect before acknowledgement", func(e *model.CleanupEntry) { e.Removed = &artifactsapi.RemovedItem{ID: e.EntityID} }},
		{"acknowledgement without effect", func(e *model.CleanupEntry) { e.State = model.CleanupDeleted }},
		{"mismatched effect", func(e *model.CleanupEntry) {
			e.State, e.Removed = model.CleanupDeleted, &artifactsapi.RemovedItem{ID: e.EntityID + 1}
		}},
		{"negative removed bytes", func(e *model.CleanupEntry) {
			e.State, e.Removed = model.CleanupDeleted, &artifactsapi.RemovedItem{ID: e.EntityID, Size: -1}
		}},
		{"snapshot belongs to another item", func(e *model.CleanupEntry) { e.Snapshot.ID++ }},
		{"snapshot belongs to another version", func(e *model.CleanupEntry) { e.Snapshot.Version.ID++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := original
			tc.change(&bad)
			err := s.Tx(t.Context(), func(ctx context.Context) error {
				_, err := s.Cleanup().CreateEntry(ctx, bad)
				if err != nil {
					return fmt.Errorf("reject malformed cleanup intent: %w", err)
				}
				return nil
			})
			storekit.WantErr(t, err, kernel.ErrInvalid)
			storekit.Same(
				t,
				"invalid creation preserves intent",
				original,
				storekit.Get[model.CleanupEntry](t)(s.Cleanup().Entry(t.Context(), original.RunID, original.Kind, original.EntityID)),
			)
		})
	}
	bad := original
	bad.Revision++
	bad.State = model.CleanupDeleted
	err := s.Tx(t.Context(), func(ctx context.Context) error { return s.Cleanup().AdvanceEntry(ctx, original.Revision, bad) })
	storekit.WantErr(t, err, kernel.ErrInvalid)
	storekit.Same(
		t,
		"invalid advance preserves intent",
		original,
		storekit.Get[model.CleanupEntry](t)(s.Cleanup().Entry(t.Context(), original.RunID, original.Kind, original.EntityID)),
	)
}
