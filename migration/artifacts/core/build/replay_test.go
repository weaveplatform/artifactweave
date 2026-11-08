package build

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func buildReplayRequest() store.ReplayRequest {
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	now := time.Now()
	return store.ReplayRequest{
		Scope:       digest("scope"),
		Key:         digest("key"),
		Fingerprint: digest("body"),
		Now:         now,
		Expires:     now.Add(time.Hour),
	}
}

func currentBuildAccess(context.Context) (Access, error) { return everyone, nil }

func TestUnitAppendOnce(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	first := w.publish(doc("app", "1", started0))
	ap := Append{Name: "app", Number: "1", Modules: []byte(`[{"id":"added"}]`), By: "admin"}
	r := buildReplayRequest()
	for i := range 3 {
		out, err := w.svc.AppendOnce(ctx, ap, r, currentBuildAccess)
		if err != nil || out.ResourceID != first.Run.ID || out.Replayed != (i > 0) {
			t.Fatalf("attempt %d: %+v, %v", i, out, err)
		}
	}
	later := w.publish(doc("app", "1", at(1)))
	out, err := w.svc.AppendOnce(ctx, ap, r, currentBuildAccess)
	if err != nil || out.ResourceID != first.Run.ID {
		t.Fatalf("replay changed its target: %+v, %v", out, err)
	}
	for id, wantModules := range map[int64]int{first.Run.ID: 1, later.Run.ID: 0} {
		got, err := w.db.Builds().ByID(ctx, id)
		if err != nil || len(got.Modules) != wantModules {
			t.Fatalf("build %d: %+v, %v", id, got.Modules, err)
		}
	}
	_, err = w.svc.AppendOnce(
		ctx,
		ap,
		r,
		func(context.Context) (Access, error) { return only(types.ActionRead), nil },
	)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("replay after permission revocation: %v", err)
	}
	if err := w.svc.Rename(ctx, "", "app", "moved", "admin", everyone); err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.AppendOnce(ctx, ap, r, currentBuildAccess)
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
	_, err := w.svc.AppendOnce(ctx, ap, r, func(context.Context) (Access, error) {
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
	out, err := w.svc.AppendOnce(ctx, ap, r, currentBuildAccess)
	if err != nil || out.Replayed {
		t.Fatalf("receipt survived rollback: %+v, %v", out, err)
	}
}

func TestUnitAppendOnceFailures(t *testing.T) {
	for _, fail := range []string{"get", "replace", "byid", "repo:" + types.BuildInfoRepository, "item"} {
		t.Run(fail, func(t *testing.T) {
			w := newWorld(t)
			w.publish(doc("app", "1", started0))
			s := &Service{Store: faulty{w.db, fail}, Blobs: w.blobs, Formats: ecosystem.For}
			_, err := s.AppendOnce(
				t.Context(),
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
	_, err := w.svc.AppendOnce(t.Context(), ap, buildReplayRequest(), nil)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing revalidation: %v", err)
	}
	w.publish(doc("app", "1", started0))
	_, err = w.svc.AppendOnce(
		t.Context(),
		ap,
		buildReplayRequest(),
		func(context.Context) (Access, error) { return nil, ErrForbidden },
	)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("initial revalidation: %v", err)
	}
}
