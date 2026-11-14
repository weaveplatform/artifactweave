package build

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
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

// deleteOnce runs a keyed delete as the router does: DeleteKeyed as the
// effect and ConfirmDelete as the reload, under the shared replay over the
// service's store, and returns the runs the answer lists.
func deleteOnce(
	ctx context.Context,
	s *Service,
	replays idempotency.ReplayStore,
	d Delete,
	r idempotency.ReplayRequest,
	current func(context.Context) (Access, error),
) ([]model.BuildRun, idempotency.ReplayReceipt, error) {
	var gone []model.BuildRun
	out, err := idempotency.Replay{Store: replays, Tx: s.Store}.Do(ctx, r, idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			_, id, err := s.DeleteKeyed(ctx, d, current, time.Hour)
			return id, err
		},
		Reload: func(ctx context.Context, receipt idempotency.ReplayReceipt) error {
			var err error
			gone, err = s.ConfirmDelete(ctx, d, receipt.ResourceID, current)
			return err
		},
	})
	return gone, out, err
}

func numbers(runs []model.BuildRun) string {
	var out []string
	for _, r := range runs {
		out = append(out, r.Name+"#"+r.Number+"@"+FormatStarted(r.Started))
	}
	return strings.Join(out, ",")
}

// A keyed delete deletes once: its replays answer with the runs it
// deleted, though the same request would now delete the number's next run,
// and only while the credential may still delete them.
func TestUnitDeleteOnce(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	for i, number := range []string{"1", "1", "2"} {
		w.publish(doc("app", number, at(i)))
	}
	d := Delete{Name: "app", Numbers: []string{"1"}, By: "admin"}
	r := buildReplayRequest()
	want := "app#1@" + at(0)
	for i := range 3 {
		gone, out, err := deleteOnce(ctx, w.svc, w.db.Replays(), d, r, currentBuildAccess)
		if err != nil || numbers(gone) != want || out.Replayed != (i > 0) {
			t.Fatalf("attempt %d: %s, %+v, %v", i, numbers(gone), out, err)
		}
	}
	runs, _, err := w.db.Builds().Runs(ctx, "", "app", kernel.Page{Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs left: %v, %v", runs, err)
	}
	_, _, err = deleteOnce(ctx, w.svc, w.db.Replays(), d, r,
		func(context.Context) (Access, error) { return only(api.ActionRead), nil })
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("replay after permission revocation: %v", err)
	}
	if err := w.db.Tx(ctx, func(ctx context.Context) error {
		_, err := w.svc.ConfirmDelete(ctx, Delete{Name: "other"}, 1, currentBuildAccess)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("report of another build: %v", err)
	}
}

// promoteOnce runs a keyed promotion as the router does, and returns the
// messages the answer lists.
func promoteOnce(
	ctx context.Context,
	s *Service,
	replays idempotency.ReplayStore,
	p Promotion,
	r idempotency.ReplayRequest,
	current func(context.Context) (Access, error),
) ([]item.Message, idempotency.ReplayReceipt, error) {
	var msgs []item.Message
	out, err := idempotency.Replay{Store: replays, Tx: s.Store}.Do(ctx, r, idempotency.Effect{
		Run: func(ctx context.Context) (int64, error) {
			res, id, err := s.PromoteKeyed(ctx, p, current, time.Hour)
			msgs = res.Messages
			return id, err
		},
		Reload: func(ctx context.Context, receipt idempotency.ReplayReceipt) error {
			var err error
			msgs, err = s.ConfirmPromote(ctx, p, receipt.ResourceID, current)
			return err
		},
	})
	return msgs, out, err
}

// A keyed promotion promotes once: its replays answer with the original
// report, though its files have moved and the same request would now report
// nothing to move, and only while the run stays where the request names it
// and the credential may still promote it.
func TestUnitPromoteOnce(t *testing.T) {
	w := promotionWorld(t)
	ctx := t.Context()
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, nil)))
	p := Promotion{
		Name: "app", Number: "1", Status: "released", SourceRepo: "libs-local",
		TargetRepo: "release", Artifacts: true, FailFast: true, By: "admin",
	}
	r := buildReplayRequest()
	var first string
	for i := range 3 {
		msgs, out, err := promoteOnce(ctx, w.svc, w.db.Replays(), p, r, currentBuildAccess)
		if err != nil || out.Replayed != (i > 0) {
			t.Fatalf("attempt %d: %+v, %v", i, out, err)
		}
		if i == 0 {
			first = texts(msgs)
		}
		if texts(msgs) != first || !strings.Contains(first, "moving libs-local:app/a.bin") {
			t.Fatalf("attempt %d report:\n%s", i, texts(msgs))
		}
	}
	if got := w.promotions("app", "1"); len(got) != 1 {
		t.Fatalf("promotions %+v", got)
	}
	_, _, err := promoteOnce(ctx, w.svc, w.db.Replays(), p, r,
		func(context.Context) (Access, error) { return only(api.ActionRead), nil })
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("replay after permission revocation: %v", err)
	}
	if err := w.svc.Rename(ctx, "", "app", "moved", "admin", everyone); err != nil {
		t.Fatal(err)
	}
	if _, _, err := promoteOnce(ctx, w.svc, w.db.Replays(), p, r, currentBuildAccess); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("replay after rename: %v", err)
	}
	if _, err := w.svc.Delete(ctx, Delete{Name: "moved", All: true}, everyone); err != nil {
		t.Fatal(err)
	}
	if _, _, err := promoteOnce(ctx, w.svc, w.db.Replays(), p, r, currentBuildAccess); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("replay after delete: %v", err)
	}
	if err := w.db.Tx(ctx, func(ctx context.Context) error {
		_, err := w.svc.ConfirmPromote(ctx, Promotion{Name: "other"}, 1, currentBuildAccess)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("report of another build: %v", err)
	}
}

// A dry run and an aborted promotion change nothing, so they report no
// receipt and nothing is kept for them.
func TestUnitPromoteKeyedWithoutEffect(t *testing.T) {
	w := promotionWorld(t)
	ctx := t.Context()
	w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
	// b.bin's content is nowhere with the run's properties, which aborts
	// a fail-fast promotion.
	w.put("libs-local", false, "app/b.bin", "b.bin", nil)
	w.publish(doc("app", "1", started0, fileModule([]string{"a.bin", "b.bin"}, nil)))
	for name, p := range map[string]Promotion{
		"dry": {
			Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true,
			DryRun: true,
		},
		"aborted": {
			Name: "app", Number: "1", TargetRepo: "release-local", Artifacts: true,
			FailFast: true,
		},
	} {
		res, id, err := w.svc.PromoteKeyed(ctx, p, currentBuildAccess, time.Hour)
		if err != nil || id != 0 || len(res.Messages) == 0 {
			t.Fatalf("%s: %d, %+v, %v", name, id, res, err)
		}
	}
	if !w.has("libs-local", "app/a.bin") {
		t.Fatal("a promotion without effect moved the file")
	}
}

// A keyed action removes expired reports before it stores its own.
func TestUnitKeyedActionPrunesReports(t *testing.T) {
	w := newWorld(t)
	ctx := t.Context()
	w.publish(doc("app", "1", started0))
	past := time.Now().Add(-2 * time.Hour)
	old, err := w.db.Builds().PutReport(ctx, model.BuildReport{
		Action: actionDelete, Name: "app", Report: []byte(`{}`),
		Created: past, Expires: past.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.DeleteKeyed(
		ctx,
		Delete{Name: "app", All: true},
		currentBuildAccess,
		time.Hour,
	); err != nil {
		t.Fatal(err)
	}
	err = w.db.Tx(ctx, func(ctx context.Context) error {
		_, err := w.db.Builds().ReportForUpdate(ctx, old)
		return err
	})
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("expired report: %v", err)
	}
}

func TestUnitKeyedActionFailures(t *testing.T) {
	deny := func(context.Context) (Access, error) { return nil, ErrForbidden }
	del := Delete{Name: "app", All: true}
	pr := Promotion{Name: "app", Number: "1"}
	for _, fail := range []string{"putreport", "prunereports"} {
		w := newWorld(t)
		w.publish(doc("app", "1", started0))
		s := &Service{Store: faulty{w.db, fail}, Blobs: w.blobs, Formats: ecosystem.For}
		if _, _, err := s.DeleteKeyed(t.Context(), del, currentBuildAccess, time.Hour); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("delete, %s: %v", fail, err)
		}
		if _, _, err := s.PromoteKeyed(t.Context(), pr, currentBuildAccess, time.Hour); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("promote, %s: %v", fail, err)
		}
	}
	w := newWorld(t)
	ctx := t.Context()
	w.publish(doc("app", "1", started0))
	for name, err := range map[string]error{
		"delete without recheck": second(w.svc.DeleteKeyed(ctx, del, nil, time.Hour)),
		"delete denied":          second(w.svc.DeleteKeyed(ctx, del, deny, time.Hour)),
		"promote without recheck": second(
			w.svc.PromoteKeyed(ctx, pr, nil, time.Hour),
		),
		"promote denied": second(w.svc.PromoteKeyed(ctx, pr, deny, time.Hour)),
		"promote absent": second(
			w.svc.PromoteKeyed(ctx, Promotion{Name: "absent", Number: "1"}, currentBuildAccess, time.Hour),
		),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	bad, err := w.db.Builds().PutReport(ctx, model.BuildReport{
		Action: actionDelete, Name: "app", Report: []byte(`not json`),
		Created: time.Now(), Expires: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	badPromotion, err := w.db.Builds().PutReport(ctx, model.BuildReport{
		Action: actionPromote, Name: "app", BuildID: 1, Report: []byte(`not json`),
		Created: time.Now(), Expires: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	inTx := func(fn func(ctx context.Context) error) error {
		return w.db.Tx(ctx, fn)
	}
	for name, err := range map[string]error{
		"confirm delete without recheck": inTx(func(ctx context.Context) error {
			_, err := w.svc.ConfirmDelete(ctx, del, bad, nil)
			return err
		}),
		"confirm promote without recheck": inTx(func(ctx context.Context) error {
			_, err := w.svc.ConfirmPromote(ctx, pr, badPromotion, nil)
			return err
		}),
		"confirm delete of a missing report": inTx(func(ctx context.Context) error {
			_, err := w.svc.ConfirmDelete(ctx, del, 999, currentBuildAccess)
			return err
		}),
		"undecodable delete report": inTx(func(ctx context.Context) error {
			_, err := w.svc.ConfirmDelete(ctx, del, bad, currentBuildAccess)
			return err
		}),
		"undecodable promotion report": inTx(func(ctx context.Context) error {
			_, err := w.svc.ConfirmPromote(ctx, pr, badPromotion, currentBuildAccess)
			return err
		}),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	good, err := w.db.Builds().PutReport(ctx, model.BuildReport{
		Action: actionPromote, Name: "app", BuildID: 1, Report: []byte(`{"messages":[]}`),
		Created: time.Now(), Expires: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inTx(func(ctx context.Context) error {
		_, err := w.svc.ConfirmPromote(ctx, pr, good, deny)
		return err
	}); !errors.Is(err, ErrForbidden) {
		t.Errorf("confirm promote denied: %v", err)
	}
	goodDelete, err := w.db.Builds().PutReport(ctx, model.BuildReport{
		Action: actionDelete, Name: "app", Report: []byte(`{"runs":[]}`),
		Created: time.Now(), Expires: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := inTx(func(ctx context.Context) error {
		_, err := w.svc.ConfirmDelete(ctx, del, goodDelete, deny)
		return err
	}); !errors.Is(err, ErrForbidden) {
		t.Errorf("confirm delete denied: %v", err)
	}
	if err := inTx(func(ctx context.Context) error {
		_, err := w.svc.ConfirmDelete(ctx, Delete{ProjectKey: "p1", Name: "app"}, goodDelete,
			currentBuildAccess)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Errorf("confirm delete of another project's report: %v", err)
	}
}

// second is the error of a call that returns two values before it.
func second[A, B any](_ A, _ B, err error) error { return err }
