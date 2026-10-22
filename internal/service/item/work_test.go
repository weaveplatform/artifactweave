package item

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
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

// A kind the service names as periodic is registered on a timer: its item
// waits for RunDue, which runs it through the service's environment, and
// WaitIdle does not wait for it.
func TestRegisterWorkPeriodic(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var ran []string
	f := worker{kinds: []string{"test.a", "test.timed"}, ran: &ran}
	w.svc.Formats = func(repo.PackageType) format.Handler { return f }
	w.svc.Periodic = map[string]time.Duration{"test.timed": time.Hour}
	q := newQueue(t, w.db, work.Options{})
	if err := w.svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	if err := w.db.WorkQueue().Enqueue(ctx, "test.timed", "k", nil, at); err != nil {
		t.Fatal(err)
	}
	if err := q.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := q.RunDue(ctx, "test.timed"); err != nil || n != 1 ||
		!slices.Equal(ran, []string{"test.timed/k"}) {
		t.Fatalf("RunDue: %d %v, ran %v", n, err, ran)
	}
	if _, err := q.RunDue(ctx, "test.a"); err != nil {
		t.Fatalf("an ordinary kind has a handler too: %v", err)
	}
}

// Remove deletes files as a client does, into the trash can, and prunes
// the folders they leave empty, up to the root; a folder that keeps a file
// stays, and a path with nothing at it is skipped.
func TestRemove(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	for _, p := range []string{"g/a/1/x.jar", "g/a/1/y.jar", "g/b/1/z.jar", "g/keep.txt"} {
		w.must(p, p)
	}
	env := w.svc.env()
	if err := env.Remove(
		ctx, w.loc, []string{"g/a/1/x.jar", "/g/a/1/y.jar", "g/b/1/z.jar", "g/none.jar"}, "sweeper",
	); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"g/a/1/x.jar", "g/a/1", "g/a", "g/b/1", "g/b"} {
		if _, ok := w.get(w.loc, p); ok {
			t.Errorf("%s stayed", p)
		}
	}
	for _, p := range []string{"g/keep.txt", "g"} {
		if _, ok := w.get(w.loc, p); !ok {
			t.Errorf("%s was removed", p)
		}
	}
	trashed, err := w.db.Trash().Find(ctx, w.loc.Key, "g/a/1/x.jar")
	if err != nil || len(trashed) != 1 || trashed[0].DeletedBy != "sweeper" {
		t.Fatalf("trash %+v: %v", trashed, err)
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

// reindexer is a format whose index a reindex rebuilds, recording each
// rebuild.
type reindexer struct {
	bare
	ran  *[]string
	fail error
}

func (f reindexer) Reindex(_ context.Context, env format.Env, c repo.Config) error {
	if env.Store == nil {
		return errors.New("reindex ran without its environment")
	}
	*f.ran = append(*f.ran, "all "+c.Key)
	return f.fail
}

// partial is a reindexer that also rebuilds the index of a path alone.
type partial struct{ reindexer }

func (f partial) ReindexPath(_ context.Context, _ format.Env, c repo.Config, p string) error {
	*f.ran = append(*f.ran, "path "+c.Key+"/"+p)
	return f.fail
}

// reposDown fails every repository lookup.
type reposDown struct{ store.RepoStore }

func (reposDown) Get(context.Context, string) (repo.Config, error) { return repo.Config{}, errBoom }

type lookupFails struct{ *inmem.Store }

func (lookupFails) Repos() store.RepoStore { return reposDown{} }

// The reindex kinds run through the format of the repository's package
// type, whichever format it is: a whole repository, or a path through a
// format that rebuilds one, and the whole repository through any other.
// A repository that is gone, or whose format keeps no index, owes nothing,
// and a failure fails the run.
func TestReindexWork(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var ran []string
	formats := map[repo.PackageType]format.Handler{
		repo.Generic: partial{reindexer{ran: &ran}},
		repo.PyPI:    reindexer{ran: &ran},
	}
	w.svc.Formats = func(t repo.PackageType) format.Handler {
		if h, ok := formats[t]; ok {
			return h
		}
		return bare{}
	}
	for _, c := range []repo.Config{
		{Key: "py", Class: repo.Local, PackageType: repo.PyPI},
		{Key: "plain", Class: repo.Local, PackageType: repo.Debian},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	q := newQueue(t, w.db, work.Options{})
	if err := w.svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	run := func(kind, key string) error {
		t.Helper()
		if err := w.db.WorkQueue().Enqueue(ctx, kind, key, nil, at); err != nil {
			t.Fatal(err)
		}
		return q.EnsureFresh(ctx, kind, key)
	}
	for _, c := range []struct{ kind, key string }{
		{format.KindReindex, "libs"},
		{format.KindReindexPath, "libs/a/b"},
		{format.KindReindexPath, "py/a"},
		{format.KindReindex, "plain"},
		{format.KindReindexPath, "plain/a"},
		{format.KindReindex, "gone"},
		{format.KindReindexPath, "gone/a"},
	} {
		if err := run(c.kind, c.key); err != nil {
			t.Errorf("%s %s: %v", c.kind, c.key, err)
		}
	}
	if want := []string{"all libs", "path libs/a/b", "all py"}; !slices.Equal(ran, want) {
		t.Fatalf("ran %v, want %v", ran, want)
	}

	formats[repo.PyPI] = reindexer{ran: &ran, fail: errBoom}
	if err := run(format.KindReindex, "py"); !errors.Is(err, errBoom) {
		t.Errorf("a failing rebuild: %v", err)
	}
	w.svc.Store = lookupFails{w.db}
	if err := run(format.KindReindexPath, "libs/a"); !errors.Is(err, errBoom) {
		t.Errorf("a failing lookup: %v", err)
	}

	// The service owns the reindex kinds: a format may not claim one.
	w.svc.Store = w.db
	w.svc.Formats = func(repo.PackageType) format.Handler {
		return worker{kinds: []string{format.KindReindex}, ran: &ran}
	}
	if err := w.svc.RegisterWork(newQueue(t, w.db, work.Options{})); !errors.Is(
		err,
		ErrDuplicateKind,
	) {
		t.Errorf("a format claiming a reindex kind: %v", err)
	}
}

func TestSourceBoundReindexRejectsPathWork(t *testing.T) {
	w := newWorld(t)
	c := repo.Config{
		Key:         "remote-nuget",
		Class:       repo.Remote,
		PackageType: repo.NuGet,
		URL:         "https://upstream.example",
	}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	var ran []string
	w.svc.Formats = func(repo.PackageType) format.Handler { return partial{reindexer{ran: &ran}} }
	err := w.svc.reindexWork(t.Context(), types.WorkItem{
		Kind: format.KindReindexPath, Key: c.Key + "/package",
		Payload: []byte(`{"source":{}}`),
	})
	if !errors.Is(err, work.ErrPermanent) || len(ran) != 0 {
		t.Fatalf("source-bound path work bypassed authorization: %v, ran %v", err, ran)
	}
}
