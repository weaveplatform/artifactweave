package npm_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// run queues kind's work on the world's repository and runs it now, through
// the work queue the item service registers the npm format's kinds with.
func (w *world) run(kind, payload string) error {
	w.t.Helper()
	q, err := work.New(w.st, work.Options{})
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.items.RegisterWork(q); err != nil {
		w.t.Fatal(err)
	}
	var body []byte
	if payload != "" {
		body = []byte(payload)
	}
	if err := q.Enqueue(w.ctx, kind, w.c.Key, body); err != nil {
		w.t.Fatal(err)
	}
	return q.EnsureFresh(w.ctx, kind, w.c.Key) //nolint:wrapcheck // test helper
}

// The queued reindex rebuilds the index as a reindex run at once does.
func TestUnitWorkReindex(t *testing.T) {
	w := newWorld(t)
	w.deploy(canonical("1.0.0"), pkg("left-pad", "1.0.0", ""), nil)
	want := w.index()
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.run(format.KindReindex, ""); err != nil {
		t.Fatal(err)
	}
	if got := w.index(); len(got["left-pad"]) != len(want["left-pad"]) {
		t.Fatalf("reindexed %v", got)
	}
}

// The strict-layout conversion moves each tarball the index lacks to the
// canonical path its package.json gives, where it is indexed, and leaves
// what it cannot move where it is, failing with each so the queue retries
// it; run again, it changes nothing more, and once the path is free it
// finishes.
func TestUnitConvert(t *testing.T) {
	w := newWorld(t)
	w.deploy("left-pad-1.0.0.tgz", pkg("left-pad", "1.0.0", ""), nil)
	w.deploy("deep/stray.tgz", pkg("left-pad", "2.0.0", ""), nil)
	w.deploy("scoped.tgz", pkg("@s/p", "1.0.0", ""), types.Properties{npm.PropDistTag: {"beta"}})
	// The same content as a version already at its canonical path needs
	// nothing; content that is no tarball stays.
	w.deploy(canonical("3.0.0"), pkg("left-pad", "3.0.0", ""), nil)
	w.deploy("copy-of-3.tgz", pkg("left-pad", "3.0.0", ""), nil)
	w.deploy("junk.tgz", []byte("not a tarball"), nil)
	// Its canonical path holds other content.
	w.deploy("blocked.tgz", pkg("blocked", "1.0.0", ""), nil)
	w.deploy(npm.CanonicalPath("blocked", "1.0.0"), []byte("garbage"), nil)

	err := w.run(npm.KindConvert, `{"by":"converter"}`)
	if !errors.Is(err, npm.ErrUnconverted) || !strings.Contains(err.Error(), "blocked.tgz") ||
		strings.Contains(err.Error(), "junk.tgz") {
		t.Fatalf("convert: %v", err)
	}
	idx := w.index()
	if len(idx["left-pad"]) != 3 || len(idx["@s/p"]) != 1 || idx["blocked"] != nil {
		t.Fatalf("index %v", idx)
	}
	if got := w.latestOf("left-pad"); got != "3.0.0" {
		t.Fatalf("latest %s", got)
	}
	// The scoped tarball kept its tag, and as its package's first version
	// took latest too.
	if got := w.index()["@s/p"]["1.0.0"].Tags; len(got) != 2 {
		t.Fatalf("@s/p's tags %v", got)
	}
	for _, p := range []string{"left-pad-1.0.0.tgz", "deep/stray.tgz", "scoped.tgz"} {
		if _, err := w.st.Items().
			Get(w.ctx, w.c.Key, false, p); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			t.Errorf("%s stayed: %v", p, err)
		}
	}
	for _, p := range []string{"copy-of-3.tgz", "junk.tgz", "blocked.tgz"} {
		if _, err := w.st.Items().Get(w.ctx, w.c.Key, false, p); err != nil {
			t.Errorf("%s moved: %v", p, err)
		}
	}
	before := w.index()
	if err := w.run(npm.KindConvert, ""); !errors.Is(err, npm.ErrUnconverted) {
		t.Fatalf("again: %v", err)
	}
	if got := w.index(); len(got) != len(before) {
		t.Fatalf("a second run changed the index: %v", got)
	}
	w.remove(npm.CanonicalPath("blocked", "1.0.0"))
	if err := w.run(npm.KindConvert, ""); err != nil {
		t.Fatalf("once free: %v", err)
	}
	if w.index()["blocked"] == nil {
		t.Fatal("blocked not converted")
	}
}

// A move the repository refuses is reported, and a failed conversion names
// at most ten tarballs.
func TestUnitConvertReports(t *testing.T) {
	w := newWorld(t)
	off := false
	w.c.HandleSnapshots = &off
	if err := w.st.Repos().Update(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	w.deploy("pre.tgz", pkg("left-pad", "2.0.0-rc.1", ""), nil)
	err := w.run(npm.KindConvert, "")
	if !errors.Is(err, npm.ErrUnconverted) ||
		!strings.Contains(err.Error(), "snapshot release handling") {
		t.Fatalf("refused: %v", err)
	}
	w = newWorld(t)
	for i := range 12 {
		name := fmt.Sprintf("p%d", i)
		w.deploy(name+".tgz", pkg(name, "1.0.0", ""), nil)
		w.deploy(npm.CanonicalPath(name, "1.0.0"), []byte("garbage"), nil)
	}
	if err := w.run(npm.KindConvert, ""); err == nil ||
		!strings.HasSuffix(err.Error(), "; and 2 more") ||
		strings.Count(err.Error(), "is taken") != 10 {
		t.Fatalf("report: %v", err)
	}
}

// Work for a repository that is gone or no npm one does nothing, and a
// failure on the way fails the run.
func TestUnitWorkFailures(t *testing.T) {
	w := newWorld(t)
	h := npm.Handler{}
	generic := repo.Config{Key: "generic-local", Class: repo.Local, PackageType: repo.Generic}
	repo.Defaults(&generic)
	if err := w.st.Repos().Create(w.ctx, generic); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: w.st, Blobs: w.items.Blobs}
	for _, key := range []string{"missing", "generic-local"} {
		for _, kind := range h.WorkKinds() {
			if err := h.Work(w.ctx, env, types.WorkItem{Kind: kind, Key: key}); err != nil {
				t.Errorf("%s on %s: %v", kind, key, err)
			}
		}
	}
	if h.Owed(w.c, false, canonical("1.0.0")) != nil {
		t.Error("a read owes work")
	}
	// An item of a kind the format no longer runs, left by an earlier
	// build, is finished without reading anything.
	if err := h.Work(w.ctx, format.Env{}, types.WorkItem{
		Kind: "npm.snapshots", Key: w.c.Key + "/left-pad",
	}); err != nil {
		t.Errorf("a stale kind: %v", err)
	}
	w.deploy("stray.tgz", pkg("left-pad", "1.0.0", ""), nil)
	moveFails := env
	moveFails.Move = func(context.Context, repo.Config, string, string, string) (string, error) {
		return "", errFault
	}
	if err := h.Work(
		w.ctx,
		moveFails,
		types.WorkItem{Kind: npm.KindConvert, Key: w.c.Key},
	); !errors.Is(
		err,
		errFault,
	) {
		t.Errorf("move: %v", err)
	}
	for _, c := range []struct {
		fail string
		pass int
		kind string
	}{
		{"Get", 0, npm.KindConvert},
		{"Search", 0, npm.KindConvert},
		{"VersionOf", 0, npm.KindConvert},
		{"ItemGet", 0, npm.KindConvert},
	} {
		f := &faults{pass: map[string]int{c.fail: c.pass}}
		st := &faulty{Store: w.st, f: f}
		env := format.Env{Store: st, Blobs: w.items.Blobs, Move: moveFails.Move}
		if err := h.Work(
			w.ctx,
			env,
			types.WorkItem{Kind: c.kind, Key: w.c.Key},
		); !errors.Is(
			err,
			errFault,
		) {
			t.Errorf("%s: %v", c.fail, err)
		}
	}
	// Content the tree names but the blob store lost fails the run.
	it, err := w.st.Items().Get(w.ctx, w.c.Key, false, "stray.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.items.Blobs.Delete(w.ctx, it.Actual.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := h.Work(w.ctx, env, types.WorkItem{Kind: npm.KindConvert, Key: w.c.Key}); err == nil {
		t.Error("converted lost content")
	}
}
