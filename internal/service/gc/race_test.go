package gc_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The race this file proves closed (#64): garbage collection removes a blob
// in three steps (G1 mark deleting, G2 delete the content, G3 tombstone)
// while a deploy of the same bytes takes two (D1 commit the content, which a
// content-addressed store skips when it is already there, and D2 reference
// it). Every interleaving of the two must leave either a stored item whose
// content exists, or a refused deploy that stored nothing and succeeds when
// repeated.

// sequence runs the hooked steps one at a time in a given order. A role's
// steps that never happen (a collector that finds the blob referenced does
// not delete it) are dropped when the role ends.
type sequence struct {
	mu    sync.Mutex
	cond  *sync.Cond
	order []string
}

func newSequence(order []string) *sequence {
	s := &sequence{order: append([]string(nil), order...)}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *sequence) wait(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.order) > 0 && s.order[0] != step {
		s.cond.Wait()
	}
}

func (s *sequence) done(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.order) > 0 && s.order[0] == step {
		s.order = s.order[1:]
	}
	s.cond.Broadcast()
}

// end drops the steps left for a role whose work is over.
func (s *sequence) end(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.order[:0]
	for _, step := range s.order {
		if !strings.HasPrefix(step, prefix) {
			kept = append(kept, step)
		}
	}
	s.order = kept
	s.cond.Broadcast()
}

type roleKey struct{}

func as(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}

func roleOf(ctx context.Context) string { r, _ := ctx.Value(roleKey{}).(string); return r }

// step runs fn as a sequenced step when ctx carries role.
func (s *sequence) step(ctx context.Context, role, name string, fn func()) {
	if roleOf(ctx) != role {
		fn()
		return
	}
	s.wait(name)
	defer s.done(name)
	fn()
}

// seqBlobs sequences the collector's content delete (G2) and the deploy's
// commit (D1); the deploy then waits for its turn at D2 before it goes on to
// reference the content, so its whole transaction is the D2 step.
type seqBlobs struct {
	blob.Store
	seq *sequence
}

func (b seqBlobs) Delete(ctx context.Context, sha string) error {
	var err error
	b.seq.step(ctx, "gc", "G2", func() { err = b.Store.Delete(ctx, sha) })
	return err //nolint:wrapcheck // test double
}

func (b seqBlobs) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	u, err := b.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // test double
	}
	return seqUpload{u, b.seq}, nil
}

type seqUpload struct {
	blob.Upload
	seq *sequence
}

func (u seqUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	var (
		info blob.Info
		err  error
	)
	u.seq.step(ctx, "deploy", "D1", func() { info, err = u.Upload.Commit(ctx, want) })
	if roleOf(ctx) == "deploy" {
		u.seq.wait("D2")
	}
	return info, err //nolint:wrapcheck // test double
}

// seqStore sequences the collector's mark (G1) and tombstone (G3).
type seqStore struct {
	store.Store
	seq *sequence
}

func (s seqStore) Blobs() store.BlobIndex { return seqIndex{s.Store.Blobs(), s.seq} }

type seqIndex struct {
	store.BlobIndex
	seq *sequence
}

func (x seqIndex) MarkDeleting(
	ctx context.Context,
	b types.Blob,
	at time.Time,
) (types.Blob, error) {
	var (
		out types.Blob
		err error
	)
	x.seq.step(ctx, "gc", "G1", func() { out, err = x.BlobIndex.MarkDeleting(ctx, b, at) })
	return out, err //nolint:wrapcheck // test double
}

func (x seqIndex) FinishDelete(ctx context.Context, sha string, at time.Time) error {
	var err error
	x.seq.step(ctx, "gc", "G3", func() { err = x.BlobIndex.FinishDelete(ctx, sha, at) })
	return err //nolint:wrapcheck // test double
}

// interleavings returns every order of a's steps merged with b's, each list
// keeping its own order.
func interleavings(a, b []string) [][]string {
	if len(a) == 0 {
		return [][]string{append([]string(nil), b...)}
	}
	if len(b) == 0 {
		return [][]string{append([]string(nil), a...)}
	}
	var out [][]string
	for _, rest := range interleavings(a[1:], b) {
		out = append(out, append([]string{a[0]}, rest...))
	}
	for _, rest := range interleavings(a, b[1:]) {
		out = append(out, append([]string{b[0]}, rest...))
	}
	return out
}

const raced = "the raced content"

// deployWins lists the orders in which the racing deploy stores its item: it
// writes the content after the collector deleted it (and finds it there when
// it revives the tombstone), or it references the blob before the collector
// marks it, which keeps it. In every other order the content the deploy
// relied on is, or may be, gone, and the deploy is refused.
var deployWins = map[string]bool{
	"G1-G2-G3-D1-D2": true,
	"G1-G2-D1-G3-D2": true,
	"D1-D2-G1-G2-G3": true,
}

// raceStores are the metadata stores the race runs on.
var raceStores = map[string]func(t *testing.T) store.Store{
	"inmem": func(*testing.T) store.Store { return inmem.New() },
	"sqlite": func(t *testing.T) store.Store {
		t.Helper()
		st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
			sqlite.Options{Options: sqlcommon.Options{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	},
}

func TestNoContentLostToARacingDeploy(t *testing.T) {
	gcSteps, deploySteps := []string{"G1", "G2", "G3"}, []string{"D1", "D2"}
	for storeName, open := range raceStores {
		for _, orphan := range []bool{false, true} {
			for _, order := range interleavings(gcSteps, deploySteps) {
				name := storeName + "/indexed/" + strings.Join(order, "-")
				if orphan {
					name = storeName + "/orphan/" + strings.Join(order, "-")
				}
				t.Run(name, func(t *testing.T) { raceOnce(t, open(t), orphan, order) })
			}
		}
	}
}

// raceOnce collects the raced content while a deploy of the same bytes
// overwrites another file, in order, then checks nothing was lost.
func raceOnce(t *testing.T, base store.Store, orphan bool, order []string) {
	ctx := context.Background()
	disk, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	plain := &item.Service{Store: base, Blobs: disk, Now: now}
	if err := base.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	libs, _ := base.Repos().Get(ctx, "libs")
	if err := base.Settings().Put(ctx, types.SettingTrashcan,
		types.TrashcanSettings{Enabled: false, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	deploy := func(ctx context.Context, svc *item.Service, p, body string) (types.Item, error) {
		return svc.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: libs, Path: p}, Body: strings.NewReader(body),
		})
	}
	// The file the racing deploy overwrites; a refused deploy must leave it.
	if _, err := deploy(ctx, plain, "g.txt", "the old content"); err != nil {
		t.Fatal(err)
	}
	var sha string
	if orphan {
		// Content the index never recorded: a collector removes it after
		// seeing it unindexed for the grace period.
		sha = blobtest.Put(ctx, t, disk, []byte(raced)).Digests.SHA256
	} else {
		it, err := deploy(ctx, plain, "f.txt", raced)
		if err != nil {
			t.Fatal(err)
		}
		sha = it.Actual.SHA256
		if _, err := plain.Delete(
			ctx,
			item.Target{Repo: libs, Path: "f.txt"},
			"alice",
		); err != nil {
			t.Fatal(err)
		}
	}
	seq := newSequence(order)
	st := seqStore{base, seq}
	blobs := seqBlobs{disk, seq}
	racing := &item.Service{Store: st, Blobs: blobs, Now: now}
	// One collector throughout: it remembers when it first saw an orphan, and
	// its hooks only sequence calls made as "gc", so this first pass runs
	// straight through.
	collector, err := gc.New(st, blobs, gc.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if orphan {
		if _, err := collector.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	clock = clock.Add(2 * gc.DefaultGrace)
	var (
		wg        sync.WaitGroup
		gcErr     error
		deployErr error
	)
	wg.Go(func() {
		defer seq.end("G")
		_, gcErr = collector.Run(as(ctx, "gc"))
	})
	wg.Go(func() {
		defer seq.end("D")
		_, deployErr = deploy(as(ctx, "deploy"), racing, "g.txt", raced)
	})
	wg.Wait()
	if gcErr != nil {
		t.Fatalf("collector: %v", gcErr)
	}

	got, err := base.Items().Get(ctx, "libs", false, "g.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := deployWins[strings.Join(order, "-")]; want != (deployErr == nil) {
		t.Fatalf("deploy stored = %v, want %v (err %v)", deployErr == nil, want, deployErr)
	}
	switch {
	case deployErr == nil:
		if got.Actual.SHA256 != sha {
			t.Fatalf("the deploy succeeded but g.txt holds %s", got.Actual.SHA256)
		}
	case errors.Is(deployErr, item.ErrCollected):
		if got.Actual.SHA256 == sha {
			t.Fatal("a refused deploy stored its item")
		}
		// Repeating the refused deploy stores it.
		if _, err := deploy(ctx, plain, "g.txt", raced); err != nil {
			t.Fatalf("repeated deploy: %v", err)
		}
	default:
		t.Fatalf("deploy: %v", deployErr)
	}
	// Whatever happened, every stored item's content is there to serve.
	for _, p := range []string{"g.txt"} {
		it, err := base.Items().Get(ctx, "libs", false, p)
		if err != nil {
			t.Fatal(err)
		}
		r, _, err := disk.Open(ctx, it.Actual.SHA256)
		if err != nil {
			t.Fatalf("%s references content that is gone: %v", p, err)
		}
		body, _ := io.ReadAll(r)
		_ = r.Close()
		row, err := base.Blobs().Get(ctx, it.Actual.SHA256)
		if err != nil || row.State != types.BlobLive || row.Refs < 1 {
			t.Fatalf("%s's blob row %+v, %v (content %q)", p, row, err, body)
		}
	}
}

// TestDeployFinishesACrashedCollection: a collector that marked a blob and
// stopped leaves it deleting. The next deploy of the same bytes finishes the
// removal itself and is refused once; repeated, it succeeds without waiting
// for another pass.
func TestDeployFinishesACrashedCollection(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.trashOff(t)
	sha := w.deploy(t, "f.txt", raced).Actual.SHA256
	w.remove(t, "f.txt")
	if _, err := w.db.Blobs().MarkDeleting(ctx, types.Blob{
		Checksums: types.Checksums{SHA256: sha}, Size: int64(len(raced)),
	}, w.now()); err != nil {
		t.Fatal(err)
	}
	_, err := w.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: w.libs, Path: "g.txt"}, Body: strings.NewReader(raced),
	})
	if !errors.Is(err, item.ErrCollected) {
		t.Fatalf("deploy over a deleting blob: %v", err)
	}
	if row, err := w.db.Blobs().Get(ctx, sha); err != nil || row.State != types.BlobDeleted ||
		w.stored(t, sha) {
		t.Fatalf(
			"the refused deploy finishes the removal: %+v, %v, stored %v",
			row,
			err,
			w.stored(t, sha),
		)
	}
	if it := w.deploy(t, "g.txt", raced); it.Actual.SHA256 != sha || !w.stored(t, sha) {
		t.Fatalf("repeated deploy: %+v", it)
	}
	// A checksum deploy of content being removed is a miss, so the client
	// uploads it in full.
	w.remove(t, "g.txt")
	if _, err := w.db.Blobs().MarkDeleting(ctx, types.Blob{
		Checksums: types.Checksums{SHA256: sha}, Size: int64(len(raced)),
	}, w.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.items.DeployByChecksum(ctx, item.Deploy{
		Target:   item.Target{Repo: w.libs, Path: "h.txt"},
		Declared: types.Checksums{SHA256: sha},
	}); !errors.Is(err, item.ErrNoBlob) {
		t.Fatalf("checksum deploy over a deleting blob: %v", err)
	}
}
