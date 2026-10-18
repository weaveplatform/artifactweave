package item

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// worker is a format that runs derived work, recording each run.
type worker struct {
	bare
	kinds []string
	owed  []format.WorkRef
	fail  *error
	ran   *[]string
}

func (f worker) WorkKinds() []string { return f.kinds }

func (f worker) Work(_ context.Context, env format.Env, it types.WorkItem) error {
	if env.Store == nil || env.Derive == nil {
		return errors.New("work ran without its environment")
	}
	*f.ran = append(*f.ran, it.Kind+"/"+it.Key)
	if f.fail != nil {
		return *f.fail
	}
	return nil
}

func (f worker) Owed(c repo.Config, cached bool, p string) []format.WorkRef {
	if cached || c.Key == "" || p == "" {
		return nil
	}
	return f.owed
}

// otherWorker is a second format type, which may not claim worker's kinds.
type otherWorker struct{ worker }

func newQueue(t *testing.T, st store.Store, o work.Options) *work.Queue {
	t.Helper()
	q, err := work.New(st, o)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// Every kind a format runs gets one handler, run through the service's own
// environment, however many package types share the format.
func TestRegisterWork(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var ran []string
	f := worker{kinds: []string{"test.a", "test.b"}, ran: &ran}
	w.svc.Formats = func(t repo.PackageType) format.Handler {
		if t == repo.Maven || t == repo.Gradle {
			return f
		}
		return bare{}
	}
	q := newQueue(t, w.db, work.Options{})
	if err := w.svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	if err := w.db.WorkQueue().Enqueue(ctx, "test.b", "k", nil, at); err != nil {
		t.Fatal(err)
	}
	if err := q.EnsureFresh(
		ctx,
		"test.b",
		"k",
	); err != nil ||
		!slices.Equal(ran, []string{"test.b/k"}) {
		t.Fatalf("ran %v: %v", ran, err)
	}

	// Two formats claiming one kind, and a kind the queue already handles,
	// are refused.
	w.svc.Formats = func(t repo.PackageType) format.Handler {
		switch t {
		case repo.Maven:
			return f
		case repo.NPM:
			return otherWorker{f}
		}
		return bare{}
	}
	if err := w.svc.RegisterWork(
		newQueue(t, w.db, work.Options{}),
	); !errors.Is(
		err,
		ErrDuplicateKind,
	) {
		t.Errorf("two owners: %v", err)
	}
	if err := w.svc.RegisterWork(q); !errors.Is(err, work.ErrDuplicateHandler) {
		t.Errorf("registered twice: %v", err)
	}
}

// A download first runs the work its format says the path is owed: none
// when nothing is owed, nothing runs it here, or no handler is registered,
// and the run's failure when it fails.
func TestResolveRunsOwedWork(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var (
		ran  []string
		fail error
	)
	f := worker{kinds: []string{"test.a"}, ran: &ran, fail: &fail, owed: []format.WorkRef{
		{Kind: "test.a", Key: "k"}, {Kind: "test.unregistered", Key: "k"},
	}}
	w.svc.Formats = func(repo.PackageType) format.Handler { return f }
	q := newQueue(t, w.db, work.Options{MaxAttempts: 1})
	if err := w.svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	w.svc.Work = q
	enqueue := func() {
		t.Helper()
		if err := w.db.WorkQueue().Enqueue(ctx, "test.a", "k", nil, at); err != nil {
			t.Fatal(err)
		}
	}

	enqueue()
	if got, err := w.svc.Resolve(
		ctx,
		w.loc,
		format.Download{Path: "p"},
	); err != nil ||
		got != "p" ||
		!slices.Equal(ran, []string{"test.a/k"}) {
		t.Fatalf("resolve %q, ran %v: %v", got, ran, err)
	}
	// Done work is not run again.
	if _, err := w.svc.Resolve(
		ctx,
		w.loc,
		format.Download{Path: "p"},
	); err != nil ||
		len(ran) != 1 {
		t.Fatalf("ran %v: %v", ran, err)
	}

	fail = errBoom
	enqueue()
	if _, err := w.svc.Resolve(ctx, w.loc, format.Download{Path: "p"}); !errors.Is(err, errBoom) {
		t.Fatalf("failing work: %v", err)
	}
	// It is parked now, after its one attempt.
	if _, err := w.svc.Resolve(
		ctx,
		w.loc,
		format.Download{Path: "p"},
	); !errors.Is(
		err,
		work.ErrParked,
	) {
		t.Fatalf("parked work: %v", err)
	}

	// A queue that only takes work runs none.
	w.svc.Work = &queued{}
	enqueue()
	if _, err := w.svc.Resolve(
		ctx,
		w.loc,
		format.Download{Path: "p"},
	); err != nil ||
		len(ran) != 2 {
		t.Fatalf("ran %v: %v", ran, err)
	}
}

// Underive removes a derived file and releases its content; nothing at the
// path is no error, and a folder is refused.
func TestUnderive(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	env := w.svc.env()
	if _, err := env.Derive(ctx, format.Derived{
		Repo: w.loc, Path: "a/meta.xml", Body: strings.NewReader("meta"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.Underive(ctx, w.loc, false, "/a/meta.xml"); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.get(w.loc, "a/meta.xml"); ok {
		t.Error("the derived file stayed")
	}
	if b, err := w.db.Blobs().Find(ctx, sums("meta")); err != nil || b.Refs != 0 {
		t.Errorf("blob %+v: %v", b, err)
	}
	if err := env.Underive(ctx, w.loc, false, "a/meta.xml"); err != nil {
		t.Errorf("nothing there: %v", err)
	}
	if err := env.Underive(ctx, w.loc, false, "a"); !errors.Is(err, ErrConflict) {
		t.Errorf("a folder: %v", err)
	}
	if err := env.Underive(ctx, w.loc, true, "x"); err != nil {
		t.Errorf("an empty cache: %v", err)
	}
}
