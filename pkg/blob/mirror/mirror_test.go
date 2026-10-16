package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const testChunk = 16 << 10

// fakes returns n fake members named m0, m1, ... ; every one signs storage
// URLs when presign is set.
func fakes(n int, presign bool) ([]*fake, []Member) {
	fs := make([]*fake, n)
	ms := make([]Member, n)
	for i := range n {
		fs[i] = newFake()
		var st blob.Store = fs[i]
		if presign {
			st = presigner{fs[i]}
		}
		ms[i] = Member{Name: fmt.Sprintf("m%d", i), Store: st, WriteEnabled: true}
	}
	return fs, ms
}

// newMirror builds a mirror with in-memory sessions and replicas.
func newMirror(t *testing.T, ms []Member, tune func(*Options)) (*Store, store.ReplicaIndex) {
	t.Helper()
	o := Options{
		Members: ms, ChunkSize: testChunk,
		Sessions: session.NewMemory(), Replicas: inmem.New().Replicas(),
	}
	if tune != nil {
		tune(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s, o.Replicas
}

var contractOptions = blobtest.Options{LargeSize: 3*testChunk + 1234}

func TestContractOnThreeFakes(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		_, ms := fakes(3, true)
		s, _ := newMirror(t, ms, nil)
		return s
	}, contractOptions)
}

func TestContractOnFilesystemsAndFake(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		var ms []Member
		for i := range 2 {
			st, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ms = append(ms, Member{Name: fmt.Sprintf("disk%d", i), Store: st, WriteEnabled: true})
		}
		ms = append(ms, Member{Name: "cloud", Store: presigner{newFake()}, WriteEnabled: true})
		// Every member is in the write quorum. The cloud fake is the only one
		// that signs, and the contract signs straight after Put: with a
		// quorum of two disks the cloud could still be committing, holding
		// no copy to sign (#212, TestSignBeforeTheSigningMemberCommits).
		s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = len(ms) })
		// Anything the mirror still runs in the background must finish
		// before the temp dirs are removed (cleanups run last registered
		// first): Windows cannot remove a directory that a straggler is
		// still renaming a blob into.
		t.Cleanup(func() { _ = s.WaitIdle(context.Background()) })
		return s
	}, contractOptions)
}

func TestContractTwoOfThree(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		_, ms := fakes(3, true)
		s, _ := newMirror(t, ms, func(o *Options) { o.Redundancy = 2 })
		return s
	}, contractOptions)
}

func TestContractWithDurableSessions(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		_, ms := fakes(3, true)
		s, _ := newMirror(t, ms, func(o *Options) {
			o.Sessions = session.InUploads(inmem.New().Uploads(), "mirror:test")
		})
		return s
	}, contractOptions)
}

// put stores body through the mirror and returns its digests.
func put(t *testing.T, s *Store, body []byte) blob.Info {
	t.Helper()
	return blobtest.Put(context.Background(), t, s, body)
}

// states returns each member's replica state for sha.
func states(t *testing.T, r store.ReplicaIndex, sha string) map[string]types.ReplicaState {
	t.Helper()
	recs, err := r.ForBlob(context.Background(), sha)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]types.ReplicaState{}
	for _, rec := range recs {
		out[rec.Member] = rec.State
	}
	return out
}

func wantStates(
	t *testing.T,
	r store.ReplicaIndex,
	sha string,
	want map[string]types.ReplicaState,
) {
	t.Helper()
	got := states(t, r, sha)
	if len(got) != len(want) {
		t.Fatalf("replicas %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("replicas %v, want %v", got, want)
		}
	}
}

func TestMemberDownAtAppend(t *testing.T) {
	f, ms := fakes(3, false)
	s, r := newMirror(t, ms, nil)
	f[2].failOn("append", 0, errInjected)
	body := blobtest.Content(1, 2*testChunk+5)
	info := put(t, s, body)
	wantStates(t, r, info.Digests.SHA256, map[string]types.ReplicaState{
		"m0": types.ReplicaPresent, "m1": types.ReplicaPresent, "m2": types.ReplicaPending,
	})
	if f[2].has(info.Digests.SHA256) {
		t.Fatal("the ejected member stored the blob")
	}
	rec, err := r.Get(context.Background(), info.Digests.SHA256, "m2")
	if err != nil || rec.LastError == "" || rec.Size != int64(len(body)) {
		t.Fatalf("pending record %+v %v", rec, err)
	}
}

func TestMemberFailsAtCommit(t *testing.T) {
	f, ms := fakes(3, false)
	s, r := newMirror(t, ms, nil)
	f[1].failOn("commit", 0, errInjected)
	info := put(t, s, blobtest.Content(2, 100))
	wantStates(t, r, info.Digests.SHA256, map[string]types.ReplicaState{
		"m0": types.ReplicaPresent, "m1": types.ReplicaPending, "m2": types.ReplicaPresent,
	})
}

func TestQuorumNotMet(t *testing.T) {
	ctx := context.Background()
	t.Run("begin", func(t *testing.T) {
		f, ms := fakes(3, false)
		s, _ := newMirror(t, ms, nil)
		f[0].failOn("begin", 0, errInjected)
		f[1].failOn("begin", 0, errInjected)
		if _, err := s.BeginUpload(
			ctx,
		); !errors.Is(err, ErrQuorum) ||
			!errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if len(f[2].uploads) != 0 {
			t.Fatal("the member that began was not aborted")
		}
	})
	t.Run("append", func(t *testing.T) {
		f, ms := fakes(3, false)
		s, _ := newMirror(t, ms, nil)
		u, err := s.BeginUpload(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f[0].failOn("append", 0, errInjected)
		f[1].failOn("append", 0, errInjected)
		if _, err := u.Append(ctx, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrQuorum) {
			t.Fatal(err)
		}
	})
	t.Run(
		"commit, then a resumed retry succeeds",
		func(t *testing.T) { retryAfterPartialCommit(t, true) },
	)
	t.Run("commit, then a retry on the same upload succeeds", func(t *testing.T) {
		retryAfterPartialCommit(t, false)
	})
}

// retryAfterPartialCommit commits with two of three members failing, then
// retries after one of them committed without the mirror hearing of it.
func retryAfterPartialCommit(t *testing.T, resume bool) {
	ctx := context.Background()
	{
		f, ms := fakes(3, false)
		s, r := newMirror(t, ms, nil)
		body := blobtest.Content(3, 500)
		u, err := s.BeginUpload(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := u.Append(ctx, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		f[1].failOn("commit", 0, errInjected)
		f[2].failOn("commit", 0, errInjected)
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, ErrQuorum) {
			t.Fatal(err)
		}
		sha := blobtest.Digest(body).SHA256
		wantStates(t, r, sha, map[string]types.ReplicaState{
			"m0": types.ReplicaPresent, "m1": types.ReplicaPending, "m2": types.ReplicaPending,
		})
		// m1's commit went through after all but the answer was lost: its
		// session is gone and it holds the blob.
		f[1].heal()
		for _, up := range f[1].uploads {
			if _, err := up.Commit(ctx, blob.Digests{}); err != nil {
				t.Fatal(err)
			}
		}
		f[2].heal()
		retry := u
		if resume {
			retry, err = s.ResumeUpload(ctx, u.ID())
			if err != nil {
				t.Fatal(err)
			}
		}
		info, err := retry.Commit(ctx, blob.Digests{})
		if err != nil || info.Digests.SHA256 != sha || info.Digests.MD5 == "" {
			t.Fatalf("retried commit %+v %v", info, err)
		}
		synctestWaitStragglers(t, r, sha, 3)
	}
}

// synctestWaitStragglers waits (outside a bubble) until n members are
// present for sha.
func synctestWaitStragglers(t *testing.T, r store.ReplicaIndex, sha string, n int) {
	t.Helper()
	for range 200 {
		present := 0
		for _, st := range states(t, r, sha) {
			if st == types.ReplicaPresent {
				present++
			}
		}
		if present == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("replicas %v never reached %d present", states(t, r, sha), n)
}

func TestStragglers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ms := fakes(3, false)
		s, r := newMirror(t, ms, func(o *Options) { o.MemberTimeout = 30 * time.Second })
		f[2].slow("commit", 10*time.Second)
		info := put(t, s, blobtest.Content(4, 64))
		sha := info.Digests.SHA256
		if states(t, r, sha)["m2"] != types.ReplicaPending {
			t.Fatalf("a member still committing must be pending: %v", states(t, r, sha))
		}
		time.Sleep(11 * time.Second)
		synctest.Wait()
		if states(t, r, sha)["m2"] != types.ReplicaPresent {
			t.Fatalf("the straggler finished: %v", states(t, r, sha))
		}

		f[2].slow("commit", time.Hour)
		info = put(t, s, blobtest.Content(5, 64))
		time.Sleep(31 * time.Second)
		synctest.Wait()
		rec, err := r.Get(context.Background(), info.Digests.SHA256, "m2")
		if err != nil || rec.State != types.ReplicaPending ||
			!bytes.Contains([]byte(rec.LastError), []byte("canceled")) {
			t.Fatalf("a straggler past the timeout stays pending: %+v %v", rec, err)
		}
	})
}

// TestDeleteWaitsForStragglers: a member still committing when the blob is
// deleted would bring it back afterwards, so Delete waits for it.
func TestDeleteWaitsForStragglers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		f, ms := fakes(3, false)
		s, _ := newMirror(t, ms, nil)
		f[2].slow("commit", 10*time.Second)
		info := put(t, s, blobtest.Content(14, 64))
		sha := info.Digests.SHA256
		short, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := s.Delete(short, sha); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("delete gives up when its context ends first: %v", err)
		}
		if err := s.Delete(ctx, sha); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if ok, err := s.Exists(ctx, sha); err != nil || ok || f[2].has(sha) {
			t.Fatalf("the straggler brought the blob back: %v %v %v", ok, err, f[2].has(sha))
		}
	})
}

func TestWaitIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		f, ms := fakes(3, false)
		s, r := newMirror(t, ms, nil)
		if err := s.WaitIdle(ctx); err != nil {
			t.Fatalf("nothing running: %v", err)
		}
		f[2].slow("commit", 10*time.Second)
		sha := put(t, s, blobtest.Content(15, 64)).Digests.SHA256
		short, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := s.WaitIdle(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WaitIdle gives up when its context ends first: %v", err)
		}
		if err := s.WaitIdle(ctx); err != nil {
			t.Fatal(err)
		}
		if got := states(t, r, sha)["m2"]; got != types.ReplicaPresent {
			t.Fatalf("WaitIdle returned before the straggler was recorded: %v", got)
		}
	})
}

func TestCommitCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, ms := fakes(3, false)
		s, _ := newMirror(t, ms, nil)
		u, err := s.BeginUpload(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range f {
			x.slow("commit", time.Minute)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if _, err := s.ResumeUpload(context.Background(), u.ID()); err != nil {
			t.Fatalf("a cancelled commit leaves the session: %v", err)
		}
	})
}

func TestReadFailover(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	// A full quorum, so every member holds the blob when the read starts.
	s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 3 })
	body := blobtest.Content(6, 5000)
	info := put(t, s, body)
	f[0].failOn("read", 1, errInjected)
	rc, _, err := s.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("failover read: %d bytes, %v", len(got), err)
	}
	if _, err := rc.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 10)
	if _, err := io.ReadFull(rc, b); err != nil || !bytes.Equal(b, body[100:110]) {
		t.Fatalf("read after seek: %v", err)
	}
	if _, err := rc.Seek(0, 9); !errors.Is(err, ErrSeek) {
		t.Fatal(err)
	}
	_ = rc.Close()
	_ = rc.Close()

	for _, x := range f {
		x.failOn("read", 0, errInjected)
	}
	rc, _, err = s.Open(ctx, info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); !errors.Is(err, errInjected) {
		t.Fatalf("every member failing: %v", err)
	}
}

// shortStore serves its blobs cut short while reporting the full size, the
// shape of a stream that ends early.
type shortStore struct{ *fake }

func (s shortStore) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	rc, info, err := s.fake.Open(ctx, sha)
	if err != nil {
		return nil, info, err
	}
	b, _ := io.ReadAll(rc)
	return &fakeReader{Reader: bytes.NewReader(b[:len(b)/2]), f: s.fake, ctx: ctx}, info, nil
}

func TestReadEarlyEOFFailsOver(t *testing.T) {
	f, ms := fakes(2, false)
	ms[0].Store = shortStore{f[0]}
	s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 2 })
	body := blobtest.Content(7, 1000)
	info := put(t, s, body)
	rc, _, err := s.Open(context.Background(), info.Digests.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(rc); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("early EOF read: %d bytes, %v", len(got), err)
	}
}

func TestBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		f, ms := fakes(3, false)
		s, r := newMirror(t, ms, func(o *Options) {
			o.BreakerThreshold, o.BreakerCooldown = 2, 15*time.Second
		})
		info := put(t, s, blobtest.Content(8, 100))
		synctest.Wait() // let the third member's commit land
		sha := info.Digests.SHA256
		f[0].failOn("stat", 0, errInjected)
		for range 2 {
			if _, err := s.Stat(ctx, sha); err != nil {
				t.Fatal(err)
			}
		}
		if !s.byName["m0"].health.open(s.now()) {
			t.Fatal("two failures must open the breaker")
		}
		calls := f[0].calls["stat"]
		if _, err := s.Stat(ctx, sha); err != nil || f[0].calls["stat"] != calls {
			t.Fatalf("an open member must be tried last: %v, %d calls", err, f[0].calls["stat"])
		}
		// While open it gets no new copies; its copy is deferred.
		info2 := put(t, s, blobtest.Content(9, 100))
		wantStates(t, r, info2.Digests.SHA256, map[string]types.ReplicaState{
			"m0": types.ReplicaPending, "m1": types.ReplicaPresent, "m2": types.ReplicaPresent,
		})
		time.Sleep(16 * time.Second)
		if _, err := s.Stat(ctx, sha); err != nil || f[0].calls["stat"] != calls+1 {
			t.Fatalf("after the cooldown one call goes through: %v", err)
		}
		if !s.byName["m0"].health.open(s.now()) {
			t.Fatal("a failure after the cooldown reopens at once")
		}
		f[0].heal()
		time.Sleep(16 * time.Second)
		if _, err := s.Stat(ctx, sha); err != nil || s.byName["m0"].health.open(s.now()) {
			t.Fatalf("a success closes the breaker: %v", err)
		}
	})
}

func TestDeleteWithMemberDown(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	s, r := newMirror(t, ms, nil)
	info := put(t, s, blobtest.Content(10, 100))
	sha := info.Digests.SHA256
	f[1].failOn("delete", 0, errInjected)
	if err := s.Delete(ctx, sha); err != nil {
		t.Fatal(err)
	}
	wantStates(t, r, sha, map[string]types.ReplicaState{"m1": types.ReplicaDeleting})
	if ok, err := s.Exists(ctx, sha); err != nil || ok {
		t.Fatalf("a tombstoned blob exists: %v %v", ok, err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, blob.ErrNotFound) {
		t.Fatal(err)
	}
	if !f[1].has(sha) {
		t.Fatal("the unreachable member's copy is left for the repair worker")
	}
	// Uploading the blob again makes it live everywhere.
	f[1].heal()
	put(t, s, blobtest.Content(10, 100))
	if ok, _ := s.Exists(ctx, sha); !ok {
		t.Fatal("re-uploaded blob is missing")
	}
}

// TestSignBeforeTheSigningMemberCommits: a signing member still committing
// after the quorum holds no copy yet, so the mirror cannot sign until it
// finishes, and a redirected download streams meanwhile (decision 0029).
// Signing straight after Put is certain only when the write quorum cannot be
// met without a member that signs.
func TestSignBeforeTheSigningMemberCommits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		f := newFake()
		ms := []Member{
			{Name: "plain", Store: newFake(), WriteEnabled: true},
			{Name: "signer", Store: presigner{f}, WriteEnabled: true},
		}
		storage := blob.SignOptions{TTL: time.Minute}
		s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 1 })
		f.slow("commit", 10*time.Second)
		info := put(t, s, blobtest.Content(15, 10))
		sha := info.Digests.SHA256
		if _, err := s.SignURL(ctx, sha, storage); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("sign while the only signer is still committing: %v", err)
		}
		time.Sleep(11 * time.Second)
		synctest.Wait()
		if url, err := s.SignURL(ctx, sha, storage); err != nil || url != "fake://"+sha {
			t.Fatalf("sign once the signer holds the blob: %q %v", url, err)
		}

		s, _ = newMirror(t, ms, func(o *Options) { o.WriteQuorum = 2 })
		info = put(t, s, blobtest.Content(16, 10))
		if _, err := s.SignURL(ctx, info.Digests.SHA256, storage); err != nil {
			t.Fatalf("sign after a put whose quorum included the signer: %v", err)
		}
	})
}

func TestSign(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	ms := []Member{
		{Name: "plain", Store: newFake(), WriteEnabled: true},
		{Name: "signer", Store: presigner{f}, WriteEnabled: true},
	}
	s, r := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 2 })
	if !s.Signs(blob.ViaStorage) || s.Signs(blob.ViaCDN) {
		t.Fatal("the mirror signs the routes its members sign")
	}
	storage := blob.SignOptions{TTL: time.Minute}
	info := put(t, s, blobtest.Content(11, 10))
	url, err := s.SignURL(ctx, info.Digests.SHA256, storage)
	if err != nil || url != "fake://"+info.Digests.SHA256 {
		t.Fatalf("sign %q %v", url, err)
	}
	_, err = s.SignURL(
		ctx,
		info.Digests.SHA256,
		blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute},
	)
	if !errors.Is(err, blob.ErrCannotSign) {
		t.Fatalf("no member has a CDN: %v", err)
	}
	// Without index records only a member holding the blob signs.
	if _, err := r.DeleteBlob(ctx, info.Digests.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SignURL(ctx, info.Digests.SHA256, storage); err != nil {
		t.Fatalf("probing sign: %v", err)
	}
	missing := blobtest.Digest([]byte("missing")).SHA256
	if _, err := s.SignURL(ctx, missing, storage); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("sign of a missing blob: %v", err)
	}
	f.failOn("sign", 0, errInjected)
	if _, err := s.SignURL(ctx, info.Digests.SHA256, storage); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}

	_, plain := fakes(2, false)
	s, _ = newMirror(t, plain, nil)
	info = put(t, s, blobtest.Content(12, 10))
	if s.Signs(blob.ViaStorage) {
		t.Fatal("no member signs")
	}
	if _, err := s.SignURL(ctx, info.Digests.SHA256, storage); !errors.Is(err, blob.ErrCannotSign) {
		t.Fatalf("no member can sign: %v", err)
	}
	if _, err := s.SignURL(ctx, "bad", storage); !errors.Is(err, blob.ErrInvalidDigest) {
		t.Fatal(err)
	}
}

// The CDN route skips members without a CDN, even ahead in read order, and
// passes the options through.
func TestSignPicksAMemberForTheRoute(t *testing.T) {
	// The bubble's frozen clock gives both members the same latency, so
	// read order is configuration order.
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		ms := []Member{
			{Name: "storage", Store: presigner{newFake()}, WriteEnabled: true},
			{Name: "edge", Store: cdnSigner{newFake()}, WriteEnabled: true},
		}
		s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 2 })
		if !s.Signs(blob.ViaCDN) {
			t.Fatal("a member with a CDN gives the mirror one")
		}
		sha := put(t, s, blobtest.Content(14, 10)).Digests.SHA256
		cdn := blob.SignOptions{Via: blob.ViaCDN, TTL: time.Minute, Filename: "a.tgz"}
		if url, err := s.SignURL(ctx, sha, cdn); err != nil || url != "cdn://"+sha+"/a.tgz" {
			t.Fatalf("CDN sign %q %v", url, err)
		}
		storage := blob.SignOptions{TTL: time.Minute}
		if url, err := s.SignURL(ctx, sha, storage); err != nil || url != "fake://"+sha {
			t.Fatalf("storage sign comes from the first member in read order: %q %v", url, err)
		}
	})
}

// A member that cannot sign (Azure on a SAS token, GCS without signBlob)
// fails every signature with its configuration error. Counting that as a
// health failure opened its breaker and pushed its reads behind the other
// members, although its storage was fine.
func TestSignFailureKeepsHealth(t *testing.T) {
	// The bubble's frozen clock gives both members the same latency, so
	// read order is configuration order and the unsigned member comes first.
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		unsigned := newFake()
		ms := []Member{
			{Name: "unsigned", Store: presigner{unsigned}, WriteEnabled: true},
			{Name: "signer", Store: presigner{newFake()}, WriteEnabled: true},
		}
		s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum, o.BreakerThreshold = 2, 2 })
		sha := put(t, s, blobtest.Content(13, 10)).Digests.SHA256
		unsigned.failOn("sign", 0, fmt.Errorf("%w: cannot sign", ErrConfig))
		for range 10 {
			if url, err := s.SignURL(
				ctx,
				sha,
				blob.SignOptions{TTL: time.Minute},
			); err != nil ||
				url != "fake://"+sha {
				t.Fatalf("sign %q %v", url, err)
			}
		}
		if s.byName["unsigned"].health.open(s.now()) {
			t.Fatal("signing failures opened the breaker")
		}
		// Still first in read order, so it was asked every time.
		if n := unsigned.calls["sign"]; n != 10 {
			t.Fatalf("unsigned member asked %d times, want 10", n)
		}
	})
}

func TestNewValidates(t *testing.T) {
	st := newFake()
	m := func(name string) Member { return Member{Name: name, Store: st, WriteEnabled: true} }
	base := Options{Sessions: session.NewMemory(), Replicas: inmem.New().Replicas()}
	cases := map[string]Options{
		"no members":        {},
		"bad name":          {Members: []Member{m("Bad-Name")}},
		"duplicate":         {Members: []Member{m("a"), m("a")}},
		"no store":          {Members: []Member{{Name: "a", WriteEnabled: true}}},
		"redundancy high":   {Members: []Member{m("a")}, Redundancy: 2},
		"quorum high":       {Members: []Member{m("a"), m("b")}, Redundancy: 1, WriteQuorum: 2},
		"none writable":     {Members: []Member{{Name: "a", Store: st}}},
		"negative chunk":    {Members: []Member{m("a")}, ChunkSize: -1},
		"negative timeout":  {Members: []Member{m("a")}, MemberTimeout: -1},
		"negative quorum":   {Members: []Member{m("a")}, WriteQuorum: -1},
		"negative breakers": {Members: []Member{m("a")}, BreakerThreshold: -1},
	}
	for name, o := range cases {
		if o.Members != nil || name == "no members" {
			o.Sessions, o.Replicas = base.Sessions, base.Replicas
		}
		if _, err := New(o); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New(Options{Members: []Member{m("a")}}); !errors.Is(err, ErrConfig) {
		t.Errorf("missing session store and replicas: %v", err)
	}
	s, err := New(Options{
		Members: []Member{m("a"), m("b"), m("c")}, Sessions: base.Sessions, Replicas: base.Replicas,
	})
	if err != nil || s.redundancy != 3 || s.quorum != 2 || s.chunk != DefaultChunkSize {
		t.Fatalf("defaults: %+v %v", s, err)
	}
}

func TestChunkSize(t *testing.T) {
	const mib = 1 << 20
	sized := func(parts ...int64) []Member {
		ms := []Member{{Name: "unsized", Store: newFake()}}
		for i, p := range parts {
			ms = append(ms, Member{Name: fmt.Sprintf("m%d", i), Store: parted{newFake(), p}})
		}
		return ms
	}
	for name, tc := range map[string]struct {
		explicit int64
		members  []Member
		want     int64
	}{
		"unsized members":    {members: sized(), want: DefaultChunkSize},
		"backend defaults":   {members: sized(8*mib, 8*mib, 16*mib), want: 16 * mib},
		"small parts":        {members: sized(256<<10, mib), want: DefaultChunkSize},
		"rounded up":         {members: sized(3 * mib), want: 9 * mib},
		"no common divisor":  {members: sized(5*mib, 16*mib), want: 80 * mib},
		"zero part ignored":  {members: sized(0), want: DefaultChunkSize},
		"explicit multiple":  {explicit: 32 * mib, members: sized(8*mib, 16*mib), want: 32 * mib},
		"explicit unchecked": {explicit: 1000, members: sized(), want: 1000},
	} {
		if got, err := chunkSize(tc.explicit, tc.members); err != nil || got != tc.want {
			t.Errorf("%s: chunk %d, %v; want %d", name, got, err, tc.want)
		}
	}
	for name, tc := range map[string]struct {
		explicit int64
		members  []Member
	}{
		"negative":                {explicit: -1, members: sized()},
		"explicit not a multiple": {explicit: 24 * mib, members: sized(8*mib, 16*mib)},
		"common multiple too big": {members: sized(5*mib, 7*mib, 11*mib)},
		"one part too big":        {members: sized(MaxDerivedChunkSize + 1)},
	} {
		if got, err := chunkSize(tc.explicit, tc.members); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: chunk %d, %v; want ErrConfig", name, got, err)
		}
	}
}

// TestAppendsAlignWithPartSizes checks what the derived chunk is for: every
// member receives appends that are whole parts, save the last, so no member
// session is left holding a partial part between chunks.
func TestAppendsAlignWithPartSizes(t *testing.T) {
	const mib = 1 << 20
	parts := []int64{3 * mib, mib}
	var ms []Member
	var stores []*fake
	for i, p := range parts {
		f := newFake()
		stores = append(stores, f)
		ms = append(
			ms,
			Member{Name: fmt.Sprintf("m%d", i), Store: parted{f, p}, WriteEnabled: true},
		)
	}
	s, _ := newMirror(t, ms, func(o *Options) { o.ChunkSize = 0 })
	if s.chunk != 9*mib {
		t.Fatalf("chunk %d, want 9 MiB: the default 8 MiB is not a multiple of 3 MiB", s.chunk)
	}
	put(t, s, blobtest.Content(39, 2*int(s.chunk)+123))
	for i, f := range stores {
		f.mu.Lock()
		got := slices.Clone(f.appends)
		f.mu.Unlock()
		if len(got) != 3 || got[2] != 123 {
			t.Fatalf("m%d appends %v, want two chunks and a 123-byte tail", i, got)
		}
		for _, n := range got[:2] {
			if int64(n)%parts[i] != 0 {
				t.Errorf(
					"m%d got a %d-byte append, not a multiple of its %d-byte parts",
					i,
					n,
					parts[i],
				)
			}
		}
	}
}

func TestPlacementPrefersZoneAndRotates(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	ms[2].Zone = "home"
	s, _ := newMirror(
		t,
		ms,
		func(o *Options) { o.Redundancy, o.WriteQuorum, o.Zone = 1, 1, "home" },
	)
	info := put(t, s, blobtest.Content(13, 10))
	if !f[2].has(info.Digests.SHA256) {
		t.Fatal("a single copy goes to this instance's zone")
	}
	// Without a zone, single copies rotate.
	f, ms = fakes(3, false)
	s, _ = newMirror(t, ms, func(o *Options) { o.Redundancy, o.WriteQuorum = 1, 1 })
	for i := range 3 {
		info := put(t, s, blobtest.Content(byte(20+i), 10))
		if !f[i].has(info.Digests.SHA256) {
			t.Fatalf("upload %d went elsewhere", i)
		}
	}
	// A drained member gets no new copies but still serves reads.
	f, ms = fakes(2, false)
	ms[1].WriteEnabled = false
	s, _ = newMirror(t, ms, func(o *Options) { o.WriteQuorum = 1 })
	info = put(t, s, blobtest.Content(30, 10))
	if f[1].has(info.Digests.SHA256) {
		t.Fatal("a drained member received a copy")
	}
	f[1].blobs[blobtest.Digest([]byte("old")).SHA256] = []byte("old")
	if ok, err := s.Exists(ctx, blobtest.Digest([]byte("old")).SHA256); err != nil || !ok {
		t.Fatalf("a drained member serves reads: %v %v", ok, err)
	}
}

func TestResumeDropsLostMembers(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	sessions := session.NewMemory()
	s, _ := newMirror(t, ms, func(o *Options) { o.Sessions = sessions })
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f[0].failOn("resume", 0, errInjected)
	resumed, err := s.ResumeUpload(ctx, u.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(resumed.(*upload).children); got != 2 {
		t.Fatalf("%d members after resume", got)
	}
	// A member removed from the configuration drops out too.
	other, _ := newMirror(t, ms[1:], func(o *Options) { o.Sessions = sessions; o.WriteQuorum = 1 })
	if _, err := other.ResumeUpload(ctx, u.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeUpload(
		ctx,
		"0123456789abcdef0123456789abcdef",
	); !errors.Is(
		err,
		blob.ErrUploadUnknown,
	) {
		t.Fatal(err)
	}
}

func TestAbortReportsMemberErrors(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	s, _ := newMirror(t, ms, nil)
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f[0].failOn("abort", 0, errInjected)
	if err := u.Abort(ctx); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
		t.Fatal("abort forgets the session even when a member fails")
	}
}

func TestWalkFailures(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(2, false)
	s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 1 })
	put(t, s, blobtest.Content(31, 10))
	f[1].failOn("walk", 0, errInjected)
	if err := s.Walk(ctx, func(blob.Info) error { return nil }); !errors.Is(err, errInjected) {
		t.Fatalf("a member that cannot be listed fails the walk: %v", err)
	}
}

func TestReadErrors(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(2, false)
	s, _ := newMirror(t, ms, func(o *Options) { o.WriteQuorum = 1 })
	info := put(t, s, blobtest.Content(32, 10))
	sha := info.Digests.SHA256
	for _, x := range f {
		x.failOn("stat", 0, errInjected)
		x.failOn("open", 0, errInjected)
	}
	if _, err := s.Stat(
		ctx,
		sha,
	); !errors.Is(err, errInjected) ||
		errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("stat with every member failing: %v", err)
	}
	if _, err := s.Exists(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	f[0].slow("stat", time.Hour)
	cancel()
	if _, err := s.Stat(cctx, sha); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// failingReplicas is a replica index whose operations fail on demand.
type failingReplicas struct {
	store.ReplicaIndex
	put, forBlob, deleteBlob error
}

func (f failingReplicas) Put(ctx context.Context, r types.BlobReplica) error {
	if f.put != nil {
		return f.put
	}
	return f.ReplicaIndex.Put(ctx, r) //nolint:wrapcheck // test wrapper
}

func (f failingReplicas) ForBlob(ctx context.Context, sha string) ([]types.BlobReplica, error) {
	if f.forBlob != nil {
		return nil, f.forBlob
	}
	return f.ReplicaIndex.ForBlob(ctx, sha) //nolint:wrapcheck // test wrapper
}

func (f failingReplicas) DeleteBlob(ctx context.Context, sha string) (int64, error) {
	if f.deleteBlob != nil {
		return 0, f.deleteBlob
	}
	return f.ReplicaIndex.DeleteBlob(ctx, sha) //nolint:wrapcheck // test wrapper
}

func TestReplicaIndexFailures(t *testing.T) {
	ctx := context.Background()
	_, ms := fakes(3, true)
	base := inmem.New().Replicas()
	s, _ := newMirror(
		t,
		ms,
		func(o *Options) { o.Replicas = failingReplicas{ReplicaIndex: base, put: errInjected} },
	)
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
		t.Fatalf("an unrecorded commit fails: %v", err)
	}
	if _, err := s.ResumeUpload(ctx, u.ID()); err != nil {
		t.Fatalf("and leaves the session to retry: %v", err)
	}

	sha := blobtest.Digest(nil).SHA256
	s, _ = newMirror(
		t,
		ms,
		func(o *Options) { o.Replicas = failingReplicas{ReplicaIndex: base, forBlob: errInjected} },
	)
	if _, err := s.Stat(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	_, err = s.SignURL(ctx, sha, blob.SignOptions{TTL: time.Minute})
	if !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	s, _ = newMirror(
		t,
		ms,
		func(o *Options) { o.Replicas = failingReplicas{ReplicaIndex: base, deleteBlob: errInjected} },
	)
	if err := s.Delete(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	f, ms2 := fakes(2, false)
	s, _ = newMirror(t, ms2, func(o *Options) {
		o.WriteQuorum = 1
		o.Replicas = failingReplicas{ReplicaIndex: base, put: errInjected}
	})
	f[0].failOn("delete", 0, errInjected)
	if err := s.Delete(ctx, sha); !errors.Is(err, errInjected) {
		t.Fatalf("an unrecorded tombstone fails the delete: %v", err)
	}
}

// failingSessions fails Put after the first n calls.
type failingSessions struct {
	*session.Memory
	puts *int
	n    int
}

func (f failingSessions) Put(ctx context.Context, id string, state []byte, size int64) error {
	*f.puts++
	if *f.puts > f.n {
		return errInjected
	}
	return f.Memory.Put(ctx, id, state, size) //nolint:wrapcheck // test wrapper
}

func TestSessionSaveFailures(t *testing.T) {
	ctx := context.Background()
	f, ms := fakes(3, false)
	puts := 0
	s, _ := newMirror(t, ms, func(o *Options) {
		o.Sessions = failingSessions{Memory: session.NewMemory(), puts: &puts, n: 1}
	})
	if _, err := s.BeginUpload(ctx); !errors.Is(err, errInjected) {
		t.Fatalf("a session that cannot be saved fails the begin: %v", err)
	}
	for i, x := range f {
		if len(x.uploads) != 0 {
			t.Fatalf("member %d keeps an upload", i)
		}
	}
	puts = -10
	u, err := s.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	puts = 100
	if _, err := u.Append(ctx, bytes.NewReader([]byte("x"))); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	if _, err := u.Append(
		ctx,
		io.MultiReader(bytes.NewReader([]byte("x")), errReader{}),
	); !errors.Is(
		err,
		errInjected,
	) {
		t.Fatal(err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errInjected }
