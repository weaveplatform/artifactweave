package federated_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

func binaryFixture(t *testing.T) (federated.Binaries, store) {
	t.Helper()
	s := newStore(t)
	b := federated.Binaries{
		Store:        s,
		Repositories: &repositories{values: map[string]repo.Config{"fed": {Key: "fed", Class: repo.Federated}, "local": {Key: "local", Class: repo.Local}}},
		Authorize:    func(context.Context) error { return nil },
		Read:         func(context.Context, string) error { return nil },
	}
	for i := range 3 {
		sha := fmt.Sprintf("%064x", i+1)
		if _, err := s.Outbox().
			RecordFailedFetch(t.Context(), outbox.FailedBlobFetch{Stream: "repo:fed", RepoKey: "fed", SHA256: sha, SHA1: fmt.Sprintf("%040x", i+1), FirstFailed: t0}); err != nil {
			t.Fatal(err)
		}
		if err := s.Stamps().Record(t.Context(), model.ItemStamp{RepoKey: "fed", Path: fmt.Sprint("file", i), SHA256: sha, Pending: []byte(`{"path":"x"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Outbox().RecordFailedFetch(t.Context(), outbox.FailedBlobFetch{Stream: "repo:foreign", SHA256: "foreign"}); err != nil {
		t.Fatal(err)
	}
	return b, s
}

func TestUnitFailedBinariesPagesFiltersAndCounts(t *testing.T) {
	b, s := binaryFixture(t)
	ctx := t.Context()
	for _, tc := range []struct {
		page, limit, want int
		more              bool
	}{{1, 2, 2, true}, {2, 2, 1, false}, {3, 2, 0, false}, {int(^uint(0) >> 1), 1000, 0, false}} {
		got, more, err := b.List(ctx, "fed", tc.limit, tc.page)
		if err != nil || len(got) != tc.want || more != tc.more {
			t.Fatalf("page %d: %+v, %t, %v", tc.page, got, more, err)
		}
		if len(got) > 0 && (got[0].Path == "" || got[0].SHA1 == "" || !got[0].Created.Equal(t0) || got[0].RepoKey != "fed") {
			t.Fatalf("task %+v", got[0])
		}
	}
	for _, tc := range []struct{ limit, page int }{{0, 1}, {1001, 1}, {1, 0}} {
		if _, _, err := b.List(ctx, "fed", tc.limit, tc.page); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatalf("bad page accepted: %v", err)
		}
	}
	for _, read := range []bool{true, false} {
		c, err := b.Counts(ctx, "fed", read)
		if err != nil || c.FailedTasks != 3 {
			t.Fatalf("counts %+v %v", c, err)
		}
	}
	sha1 := fmt.Sprintf("%040X", 2)
	if n, err := b.Delete(ctx, "fed", sha1); err != nil || n != 1 {
		t.Fatalf("delete %d %v", n, err)
	}
	if n, err := b.Delete(ctx, "fed", sha1); err != nil || n != 0 {
		t.Fatalf("delete twice %d %v", n, err)
	}
	if n, err := b.Delete(ctx, "fed", ""); err != nil || n != 2 {
		t.Fatalf("delete all %d %v", n, err)
	}
	pending, err := s.Stamps().PendingOn(ctx, "fed", fmt.Sprintf("%064x", 1))
	if err != nil || len(pending) != 1 {
		t.Fatalf("delete discarded pending stamp: %+v %v", pending, err)
	}
	fs, err := s.Outbox().FailedFetches(ctx, "")
	if err != nil || len(fs) != 1 || fs[0].Stream != "repo:foreign" {
		t.Fatalf("foreign task changed: %+v %v", fs, err)
	}
	if n, err := b.Replay(ctx, "fed", ""); err != nil || n != 0 {
		t.Fatalf("empty replay %d %v", n, err)
	}
}

func TestUnitFailedBinariesAuthorizeBeforeValidationAndLookup(t *testing.T) {
	b, _ := binaryFixture(t)
	ctx := t.Context()
	b.Authorize = func(context.Context) error { return kernel.ErrForbidden }
	for _, call := range []func() error{
		func() error { _, _, e := b.List(ctx, "missing", 0, 0); return e },
		func() error { _, e := b.Delete(ctx, "missing", "invalid"); return e },
		func() error { _, e := b.Replay(ctx, "missing", "invalid"); return e },
		func() error { _, e := b.Counts(ctx, "missing", false); return e },
	} {
		if err := call(); !errors.Is(err, kernel.ErrForbidden) {
			t.Fatal(err)
		}
	}
	b.Read = func(context.Context, string) error { return kernel.ErrForbidden }
	if _, err := b.Counts(ctx, "missing", true); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal(err)
	}
	b.Authorize = func(context.Context) error { return nil }
	if _, _, err := b.List(ctx, "missing", 1, 1); !errors.Is(err, federated.ErrNoRepository) {
		t.Fatal(err)
	}
	if _, _, err := b.List(ctx, "local", 1, 1); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { _, e := b.Delete(ctx, "fed", "invalid"); return e },
		func() error { _, e := b.Replay(ctx, "fed", "invalid"); return e },
	} {
		if err := call(); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := b.Replay(ctx, "fed", strings.Repeat("0", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Replay(ctx, "fed", ""); !errors.Is(err, federated.ErrNoStreams) {
		t.Fatal(err)
	}
}

// selectiveBlobs keeps one digest locally; every other digest is unavailable
// because the fixture has no remote members. This checks that replay continues
// past an unavailable task and publishes a later task in the same request.
type selectiveBlobs struct {
	blob.Store
	held string
}

func (b selectiveBlobs) Exists(_ context.Context, sha string) (bool, error) {
	return sha == b.held, nil
}

func TestUnitReplayAttemptsEveryTaskAndResetsAttempts(t *testing.T) {
	b, s := binaryFixture(t)
	if err := s.Federation().SetMembers(t.Context(), "fed", []model.FederationMember{{RepoKey: "fed", PeerInstanceID: "b", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	k := federated.Stream{Store: s, Items: &items{}}
	e := &streamsync.Engine{Store: s, Kinds: &streamsync.Registry{}, Blobs: selectiveBlobs{held: fmt.Sprintf("%064x", 2)}, Remote: unavailableBinaryRemote{}}
	if err := e.Kinds.Register(k); err != nil {
		t.Fatal(err)
	}
	b.Streams = func() *streamsync.Engine { return e }
	if n, err := b.Replay(t.Context(), "fed", ""); err != nil || n != 3 {
		t.Fatalf("replay %d %v", n, err)
	}
	fs, err := s.Outbox().FailedFetches(t.Context(), "repo:fed")
	if err != nil || len(fs) != 2 {
		t.Fatalf("failures %+v %v", fs, err)
	}
	for _, f := range fs {
		if f.Failures != 1 {
			t.Fatalf("replay did not reset attempts: %+v", f)
		}
	}
	stamp, err := s.Stamps().Stamp(t.Context(), "fed", "file1")
	if err != nil || len(stamp.Pending) != 0 {
		t.Fatalf("later task did not publish: %+v %v", stamp, err)
	}
}

type binaryFaultStore struct {
	federated.BinaryStore
	out    outbox.OutboxStore
	stamps port.ItemStampStore
}

func (s binaryFaultStore) Outbox() outbox.OutboxStore  { return s.out }
func (s binaryFaultStore) Stamps() port.ItemStampStore { return s.stamps }

type binaryFaultOutbox struct {
	outbox.OutboxStore
	list, clear, reset error
}

func (o binaryFaultOutbox) FailedFetches(ctx context.Context, s string) ([]outbox.FailedBlobFetch, error) {
	if o.list != nil {
		return nil, o.list
	}
	return o.OutboxStore.FailedFetches(ctx, s)
}

func (o binaryFaultOutbox) ClearFailedFetch(ctx context.Context, s, sha string) error {
	if o.clear != nil {
		return o.clear
	}
	return o.OutboxStore.ClearFailedFetch(ctx, s, sha)
}

func (o binaryFaultOutbox) ResetFailedFetch(ctx context.Context, s, sha string) error {
	if o.reset != nil {
		return o.reset
	}
	return o.OutboxStore.ResetFailedFetch(ctx, s, sha)
}

type binaryFaultStamps struct {
	port.ItemStampStore
	err error
}

func (s binaryFaultStamps) PendingOn(context.Context, string, string) ([]model.ItemStamp, error) {
	return nil, s.err
}

func TestUnitBinaryStoreFailuresSurface(t *testing.T) {
	boom := errors.New("database disconnected")
	for _, what := range []string{"list", "stamps", "clear", "reset", "repository", "bad pending"} {
		t.Run(what, func(t *testing.T) {
			b, s := binaryFixture(t)
			f := binaryFaultStore{BinaryStore: s, out: s.Outbox(), stamps: s.Stamps()}
			switch what {
			case "list":
				f.out = binaryFaultOutbox{OutboxStore: s.Outbox(), list: boom}
			case "stamps":
				f.stamps = binaryFaultStamps{ItemStampStore: s.Stamps(), err: boom}
			case "clear":
				f.out = binaryFaultOutbox{OutboxStore: s.Outbox(), clear: boom}
			case "reset":
				f.out = binaryFaultOutbox{OutboxStore: s.Outbox(), reset: boom}
			case "repository":
				b.Repositories = &repositories{getErr: boom}
			case "bad pending":
				if err := s.Stamps().Record(t.Context(), model.ItemStamp{RepoKey: "fed", Path: "file0", SHA256: fmt.Sprintf("%064x", 1), Pending: []byte("{")}); err != nil {
					t.Fatal(err)
				}
			}
			b.Store = f
			b.Streams = func() *streamsync.Engine { return &streamsync.Engine{} }
			var err error
			switch what {
			case "clear":
				_, err = b.Delete(t.Context(), "fed", "")
			case "reset":
				_, err = b.Replay(t.Context(), "fed", "")
			default:
				_, _, err = b.List(t.Context(), "fed", 1, 1)
			}
			if err == nil {
				t.Fatal("failure hidden")
			}
			if what == "list" {
				if _, err := b.Counts(t.Context(), "fed", false); !errors.Is(err, boom) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUnitFailedBinaryMetadataSurvivesPendingChange(t *testing.T) {
	f := newStreamFixture(t)
	ctx := t.Context()
	for _, stream := range []string{"unknown:x", "repo:fed"} {
		if got, err := f.k.FailedSHA1(ctx, stream, sha1); err != nil || got != "" {
			t.Fatalf("empty %s: %s %v", stream, got, err)
		}
	}
	p := `{"path":"pending","actual":{"sha1":"` + strings.Repeat("a", 40) + `"}}`
	if err := f.s.Stamps().Record(ctx, model.ItemStamp{RepoKey: "fed", Path: "pending", SHA256: sha1, Pending: []byte(p)}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.k.FailedSHA1(ctx, "repo:fed", sha1); err != nil || got != strings.Repeat("a", 40) {
		t.Fatalf("checksum %s %v", got, err)
	}
	if err := f.s.Stamps().Record(ctx, model.ItemStamp{RepoKey: "fed", Path: "pending", SHA256: sha1, Pending: []byte("{")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.k.FailedSHA1(ctx, "repo:fed", sha1); err == nil {
		t.Fatal("malformed pending metadata accepted")
	}
	f.k.Store = binaryFaultStreamStore{StreamStore: f.s, stamps: binaryFaultStamps{ItemStampStore: f.s.Stamps(), err: errors.New("stamps disconnected")}}
	if _, err := f.k.FailedSHA1(ctx, "repo:fed", sha1); err == nil {
		t.Fatal("store failure hidden")
	}
	// Historical failure rows with no recorded SHA1 derive it while pending.
	b, s := binaryFixture(t)
	if err := s.Outbox().ClearFailedFetch(ctx, "repo:fed", fmt.Sprintf("%064x", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outbox().RecordFailedFetch(ctx, outbox.FailedBlobFetch{Stream: "repo:fed", SHA256: fmt.Sprintf("%064x", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Stamps().Record(ctx, model.ItemStamp{RepoKey: "fed", Path: "file0", SHA256: fmt.Sprintf("%064x", 1), Pending: []byte(p)}); err != nil {
		t.Fatal(err)
	}
	got, _, err := b.List(ctx, "fed", 1, 1)
	if err != nil || len(got) != 1 || got[0].SHA1 != strings.Repeat("a", 40) {
		t.Fatalf("legacy %+v %v", got, err)
	}
}

type binaryFaultStreamStore struct {
	federated.StreamStore
	stamps port.ItemStampStore
}

func (s binaryFaultStreamStore) Stamps() port.ItemStampStore { return s.stamps }

type unavailableBinaryRemote struct{ streamsync.Remote }

func (unavailableBinaryRemote) FetchBlob(context.Context, model.Peer, string, func(io.Reader) error) error {
	return errors.New("source unavailable")
}
