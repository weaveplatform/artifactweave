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
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type pushConnectionsFunc func(context.Context, model.Replication) (port.PushConnection, error)

func (f pushConnectionsFunc) Connection(ctx context.Context, target model.Replication) (port.PushConnection, error) {
	return f(ctx, target)
}

type pushPageSource struct {
	artifactsapi.TransferSource
	page func() ([]artifactsapi.TransferItem, string, error)
}

func (s pushPageSource) Items(context.Context, artifactsapi.TransferRepository, artifactsapi.TransferSelection, kernel.Page) ([]artifactsapi.TransferItem, string, error) {
	return s.page()
}

func TestUnitPushAdmissionFailureNeverPublishesLogicalRun(t *testing.T) {
	for _, failure := range []string{"unavailable contracts", "anonymous caller", "invalid scope", "source read", "connection read", "missing connection", "missing policy identity", "missing logical identity"} {
		t.Run(failure, func(t *testing.T) {
			w := newPushWorld(t)
			target := stored(t, w.world, "local")
			request := replication.PushRequest{ID: "logical-run", TargetKey: target.Key, TargetRevision: target.Revision, Root: "selected"}
			ctx, want := pushContext(t.Context()), kernel.ErrStore
			switch failure {
			case "unavailable contracts":
				w.push.Source = nil
			case "anonymous caller":
				ctx, want = t.Context(), kernel.ErrUnauthenticated
			case "invalid scope":
				request.Root, want = "../private", kernel.ErrInvalid
			case "source read":
				w.sourceError, want = errPushLostAck, errPushLostAck
			case "connection read":
				w.push.Connections = pushConnectionsFunc(func(context.Context, model.Replication) (port.PushConnection, error) { return nil, errPushLostAck })
				want = errPushLostAck
			case "missing connection":
				w.push.Connections = pushConnectionsFunc(func(context.Context, model.Replication) (port.PushConnection, error) { return nil, nil })
			case "missing policy identity":
				w.transportRevision = ""
			case "missing logical identity":
				request.ID, want = "", kernel.ErrInvalid
			}
			got, err := w.push.Start(ctx, request, nil)
			if !errors.Is(err, want) || got.Reference.ID != "" || w.puts+w.patches+w.deletes != 0 {
				t.Fatal("failed admission published work", err, got, w.puts, w.patches, w.deletes)
			}
			// A subsequent valid admission must be fresh, including the logical
			// identity that a failed callback/store operation tried to reserve.
			if request.ID != "" {
				w.push.Source, w.push.Connections = pushSource{w}, pushConnections{w}
				w.sourceError, w.transportRevision = nil, "fresh-policy"
				fresh := w.start(t, "selected")
				if fresh.Version != 1 || fresh.Binding.TransportRevision != "fresh-policy" {
					t.Fatal("failed admission reserved the logical identity", fresh)
				}
			}
		})
	}
}

func TestUnitPushStepRefusesInvalidOwnershipAndRestorationContexts(t *testing.T) {
	for _, failure := range []string{"unavailable contracts", "missing fence", "cancelled", "unknown run", "missing restored context", "transactional restored context", "transactional step"} {
		t.Run(failure, func(t *testing.T) {
			w := newPushWorld(t)
			run := w.start(t, "selected")
			ctx, fence, want := t.Context(), replication.PushFence(func(context.Context) error { return nil }), kernel.ErrInvalid
			switch failure {
			case "unavailable contracts":
				w.push.Source, want = nil, kernel.ErrStore
			case "missing fence":
				fence = nil
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "unknown run":
				run.Reference.ID, want = "missing", kernel.ErrNotFound
			case "missing restored context":
				w.push.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) { return nil, nil }
				want = kernel.ErrStore
			case "transactional restored context":
				w.push.Restore = func(ctx context.Context, _ kernel.CredentialReference) (context.Context, error) {
					var tx context.Context
					must(t, w.db.Tx(ctx, func(current context.Context) error { tx = current; return nil }))
					return tx, nil
				}
				want = kernel.ErrStore
			}
			invoke := func(ctx context.Context) error { _, err := w.push.Step(ctx, run.Reference, fence); return err }
			var err error
			if failure == "transactional step" {
				err = w.db.Tx(ctx, invoke)
			} else {
				err = invoke(ctx)
			}
			if !errors.Is(err, want) || w.puts+w.patches+w.deletes != 0 {
				t.Fatal("invalid execution context reached a mutation", err, w.puts, w.patches, w.deletes)
			}
		})
	}
}

func TestUnitPushInvalidSourcePagesLeaveTraversalAndTargetUntouched(t *testing.T) {
	for _, failure := range []string{"read fault", "unbounded page", "stalled cursor", "escaped selection"} {
		t.Run(failure, func(t *testing.T) {
			w := newPushWorld(t)
			w.addSource("selected/item.pkg", "original")
			run := w.start(t, "selected")
			if failure == "stalled cursor" {
				run.Version++
				run.SourceCursor = kernel.Cursor("selected/before.pkg")
				must(t, w.db.Tx(t.Context(), func(ctx context.Context) error { return w.db.PushProgress().Advance(ctx, run.Version-1, run) }))
			}
			want := kernel.ErrStore
			w.push.Source = pushPageSource{TransferSource: w.push.Source, page: func() ([]artifactsapi.TransferItem, string, error) {
				row := pushView(w.source["selected/item.pkg"], run.Binding.SyncProperties)
				switch failure {
				case "read fault":
					return nil, "", errPushLostAck
				case "unbounded page":
					return []artifactsapi.TransferItem{row, row}, "", nil
				case "stalled cursor":
					return nil, run.SourceCursor, nil
				default:
					row.Path = "private/leak.pkg"
					return []artifactsapi.TransferItem{row}, "", nil
				}
			}}
			if failure == "read fault" {
				want = errPushLostAck
			}
			_, err := w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil })
			storekit.WantErr(t, err, want)
			current := storekit.Get[model.PushRun](t)(w.db.PushProgress().Run(t.Context(), run.Reference))
			rows, _, err := w.db.PushProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 1})
			must(t, err)
			if current != run || len(rows) != 0 || w.puts+w.deletes+w.patches != 0 {
				t.Fatal("invalid source page advanced or mutated", current, len(rows))
			}
		})
	}
}

type pushPageTarget struct {
	port.PushConnection
	page func() ([]port.PushTargetItem, string, error)
}

func (c pushPageTarget) Files(context.Context, string, kernel.Page) ([]port.PushTargetItem, string, error) {
	return c.page()
}

func TestUnitPushInvalidTargetPagesNeverAuthorizeDeletionOrAdvance(t *testing.T) {
	for _, failure := range []string{"read fault", "unbounded page", "stalled cursor", "folder in file page"} {
		t.Run(failure, func(t *testing.T) {
			w := newPushWorld(t)
			must(t, w.service.Update(t.Context(), "local", replication.Input{SyncDeletes: ptr(true)}))
			w.addTarget("selected/stale.pkg", "must survive invalid enumeration")
			run := w.start(t, "selected")
			for range 3 {
				if run.Phase == model.PushReconcile {
					break
				}
				run = storekit.Get[model.PushRun](t)(w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil }))
			}
			if run.Phase != model.PushReconcile {
				t.Fatal("fixture did not reach reconciliation")
			}
			if failure == "stalled cursor" {
				run.Version++
				run.TargetCursor = kernel.Cursor("selected/before.pkg")
				must(t, w.db.Tx(t.Context(), func(ctx context.Context) error { return w.db.PushProgress().Advance(ctx, run.Version-1, run) }))
			}
			w.push.Connections = pushConnectionsFunc(func(ctx context.Context, _ model.Replication) (port.PushConnection, error) {
				if !w.db.InTx(ctx) {
					t.Fatal("connection read escaped transaction")
				}
				return pushPageTarget{PushConnection: pushConnection{w, w.transportRevision}, page: func() ([]port.PushTargetItem, string, error) {
					row := w.target["selected/stale.pkg"]
					switch failure {
					case "read fault":
						return nil, "", errPushLostAck
					case "unbounded page":
						return []port.PushTargetItem{row, row}, "", nil
					case "stalled cursor":
						return nil, run.TargetCursor, nil
					default:
						row.Snapshot.Folder, row.Snapshot.Size = true, 0
						return []port.PushTargetItem{row}, "", nil
					}
				}}, nil
			})
			_, err := w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil })
			want := kernel.ErrStore
			if failure == "read fault" {
				want = errPushLostAck
			}
			storekit.WantErr(t, err, want)
			current := storekit.Get[model.PushRun](t)(w.db.PushProgress().Run(t.Context(), run.Reference))
			rows, _, err := w.db.PushProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 1})
			must(t, err)
			if current != run || len(rows) != 0 || w.deletes != 0 || w.targetBodies["selected/stale.pkg"] != "must survive invalid enumeration" {
				t.Fatal("invalid destination enumeration authorized progress or deletion", current, len(rows), w.deletes)
			}
		})
	}
}

func TestUnitPushCompletedTraversalDoesNotReenterAuthorityOrEffects(t *testing.T) {
	w := newPushWorld(t)
	run := w.finish(t, w.start(t, "selected"))
	w.push.Restore = func(context.Context, kernel.CredentialReference) (context.Context, error) {
		t.Fatal("completed traversal restored execution authority")
		return nil, kernel.ErrStore
	}
	got, err := w.push.Step(t.Context(), run.Reference, func(context.Context) error {
		t.Fatal("completed traversal checked execution ownership")
		return kernel.ErrStore
	})
	must(t, err)
	if got != run || w.puts+w.patches+w.deletes != 0 {
		t.Fatal("completed traversal changed retained evidence", got)
	}
}
