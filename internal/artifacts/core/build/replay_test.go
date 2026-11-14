package build

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

func buildReplayRequest() idempotency.ReplayRequest {
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	now := time.Now()
	return idempotency.ReplayRequest{
		Scope:       digest("scope"),
		Key:         digest("key"),
		Fingerprint: digest("body"),
		Now:         now,
		Expires:     now.Add(time.Hour),
	}
}

func currentBuildAccess(context.Context) (Access, error) { return everyone, nil }

// appendOnce runs a keyed append as the router does: AppendKeyed as the
// effect and ConfirmAppend as the reload, under the shared replay over the
// service's store. replays is the store whose receipts it keeps, which a
// faulty store wraps without changing.
func appendOnce(
	ctx context.Context,
	s *Service,
	replays idempotency.ReplayStore,
	ap Append,
	r idempotency.ReplayRequest,
	current func(context.Context) (Access, error),
) (idempotency.ReplayReceipt, error) {
	return idempotency.Replay{Store: replays, Tx: s.Store}.Do(ctx, r, idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) { return s.AppendKeyed(ctx, ap, current) },
		Reload: func(ctx context.Context, receipt idempotency.ReplayReceipt) error {
			return s.ConfirmAppend(ctx, ap, receipt.ResourceID, current)
		},
	})
}

func TestUnitAppendOnce(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	first := w.publish(doc("app", "1", started0))
	ap := Append{Name: "app", Number: "1", Modules: []byte(`[{"id":"added"}]`), By: "admin"}
	r := buildReplayRequest()
	for i := range 3 {
		out, err := appendOnce(ctx, w.svc, w.db.Replays(), ap, r, currentBuildAccess)
		if err != nil || out.ResourceID != first.Run.ID || out.Replayed != (i > 0) {
			t.Fatalf("attempt %d: %+v, %v", i, out, err)
		}
	}
	later := w.publish(doc("app", "1", at(1)))
	out, err := appendOnce(ctx, w.svc, w.db.Replays(), ap, r, currentBuildAccess)
	if err != nil || out.ResourceID != first.Run.ID {
		t.Fatalf("replay changed its target: %+v, %v", out, err)
	}
	for id, wantModules := range map[int64]int{first.Run.ID: 1, later.Run.ID: 0} {
		got, err := w.db.Builds().ByID(ctx, id)
		if err != nil || len(got.Modules) != wantModules {
			t.Fatalf("build %d: %+v, %v", id, got.Modules, err)
		}
	}
	_, err = appendOnce(
		ctx,
		w.svc,
		w.db.Replays(),
		ap,
		r,
		func(context.Context) (Access, error) { return only(api.ActionRead), nil },
	)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("replay after permission revocation: %v", err)
	}
	if err := w.svc.Rename(ctx, "", "app", "moved", "admin", everyone); err != nil {
		t.Fatal(err)
	}
	_, err = appendOnce(ctx, w.svc, w.db.Replays(), ap, r, currentBuildAccess)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("replay after rename: %v", err)
	}
}

func TestUnitAppendOnceRevalidationRollsBack(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	p := w.publish(doc("app", "1", started0))
	ap := Append{Name: "app", Number: "1", Modules: []byte(`[{"id":"added"}]`)}
	r := buildReplayRequest()
	calls := 0
	_, err := appendOnce(ctx, w.svc, w.db.Replays(), ap, r, func(context.Context) (Access, error) {
		calls++
		if calls == 2 {
			return nil, ErrForbidden
		}
		return everyone, nil
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("revalidation error: %v", err)
	}
	b, err := w.db.Builds().ByID(ctx, p.Run.ID)
	if err != nil || len(b.Modules) != 0 {
		t.Fatalf("rolled-back append: %v, %v", b.Modules, err)
	}
	out, err := appendOnce(ctx, w.svc, w.db.Replays(), ap, r, currentBuildAccess)
	if err != nil || out.Replayed {
		t.Fatalf("receipt survived rollback: %+v, %v", out, err)
	}
}

func TestUnitAppendOnceFailures(t *testing.T) {
	for _, fail := range []string{"get", "replace", "byid", "repo:" + api.BuildInfoRepository, "item"} {
		t.Run(fail, func(t *testing.T) {
			w := newWorld(t)
			w.publish(doc("app", "1", started0))
			s := &Service{Store: faulty{w.db, fail}, Blobs: w.blobs, Formats: ecosystem.For}
			_, err := appendOnce(
				t.Context(),
				s,
				w.db.Replays(),
				Append{Name: "app", Number: "1", Modules: []byte(`[]`)},
				buildReplayRequest(),
				currentBuildAccess,
			)
			if !errors.Is(err, errBoom) {
				t.Fatalf("%s: %v", fail, err)
			}
		})
	}
	w := newWorld(t)
	ap := Append{Name: "app", Number: "1", Modules: []byte(`[]`)}
	_, err := appendOnce(t.Context(), w.svc, w.db.Replays(), ap, buildReplayRequest(), nil)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing revalidation: %v", err)
	}
	if err := w.svc.ConfirmAppend(t.Context(), ap, 1, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing reload revalidation: %v", err)
	}
	w.publish(doc("app", "1", started0))
	_, err = appendOnce(
		t.Context(),
		w.svc,
		w.db.Replays(),
		ap,
		buildReplayRequest(),
		func(context.Context) (Access, error) { return nil, ErrForbidden },
	)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("initial revalidation: %v", err)
	}
}
