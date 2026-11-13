package remote_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

var errDropped = errors.New("the store dropped the connection")

// timedOut is a store's read that timed out, as a net.Error says.
type timedOut struct{}

func (timedOut) Error() string   { return "the store timed out" }
func (timedOut) Timeout() bool   { return true }
func (timedOut) Temporary() bool { return true }

// A failed read of the server's own copy under check passes through
// PolicyError as it is, a server fault, where its check's refusal on its
// own maps to a 502 that serves no stale copy and that the circuit does
// not count. Every other refusal maps as it did, a timeout's 504 and a
// short read's connectivity 502 among them.
func TestUnitPolicyErrorPassesAStorageFault(t *testing.T) {
	short := fmt.Errorf("blob/s3: get x: %w", io.ErrUnexpectedEOF)
	for name, tc := range map[string]struct {
		refusal error
		want    error // nil: the fault itself
	}{
		"invalid result":       {refusal: source.ErrInvalidResult},
		"plain":                {refusal: errors.New("bad rock")},
		"rejection":            {refusal: &format.RejectionError{Status: 400, Message: "no"}},
		"integrity 502":        {refusal: &source.HTTPError{Status: 502, Kind: source.FailureKindIntegrity}},
		"security 502":         {refusal: &source.HTTPError{Status: 502, Kind: source.FailureKindSecurity}},
		"protocol 502":         {refusal: &source.HTTPError{Status: 502, Kind: source.FailureKindProtocol}},
		"protocol 504":         {refusal: &source.HTTPError{Status: 504, Kind: source.FailureKindProtocol}, want: &source.HTTPError{Status: 504, Kind: source.FailureKindProtocol}},
		"upstream 502":         {refusal: &source.HTTPError{Status: 502, Kind: source.FailureKindHTTP}, want: &source.HTTPError{Status: 502, Kind: source.FailureKindHTTP}},
		"upstream 503":         {refusal: &source.HTTPError{Status: 503, Kind: source.FailureKindHTTP}, want: &source.HTTPError{Status: 503, Kind: source.FailureKindHTTP}},
		"timeout":              {refusal: fmt.Errorf("read: %w", timedOut{}), want: &source.HTTPError{Status: 504, Kind: source.FailureKindTimeout}},
		"short read":           {refusal: short, want: &source.HTTPError{Status: 502, Kind: source.FailureKindConnectivity}},
		"not found":            {refusal: kernel.ErrNotFound, want: kernel.ErrNotFound},
		"denied":               {refusal: source.ErrDenied, want: source.ErrDenied},
		"canceled":             {refusal: context.Canceled, want: context.Canceled},
		"deadline":             {refusal: context.DeadlineExceeded, want: &source.HTTPError{Status: 504, Kind: source.FailureKindTimeout}},
		"connectivity no read": {refusal: remote.ErrConnection, want: &source.HTTPError{Status: 502, Kind: source.FailureKindConnectivity}},
	} {
		fault := fmt.Errorf(
			"cache adapter representation: %w",
			&item.StorageFault{
				Cause: errDropped,
				Err:   fmt.Errorf("cache validation: %w", tc.refusal),
			},
		)
		got := remote.PolicyError(fault)
		if tc.want == nil {
			if got != fault || !errors.Is(got, format.ErrStorageFault) ||
				!errors.Is(got, errDropped) {
				t.Fatalf("%s: %v", name, got)
			}
			continue
		}
		var gotHTTP, wantHTTP *source.HTTPError
		if errors.As(tc.want, &wantHTTP) {
			if !errors.As(got, &gotHTTP) || *gotHTTP != *wantHTTP {
				t.Fatalf("%s: %#v", name, got)
			}
			continue
		}
		if got != tc.want {
			t.Fatalf("%s: %v", name, got)
		}
	}
}

// shaFaults is a blob store whose opens of sha fail with open, or whose
// reads of what it opens of sha fail with read. What it opens of sha can
// only seek, as S3's, GCS's and Azure's content does.
type shaFaults struct {
	blob.Store
	sha        string
	open, read error
}

func (b *shaFaults) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha == b.sha && b.open != nil {
		return nil, blob.Info{}, b.open
	}
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil || sha != b.sha || b.read == nil {
		return r, info, err //nolint:wrapcheck // the store's own error
	}
	return failingBody{r, b.read}, info, nil
}

type failingBody struct {
	io.ReadSeekCloser
	err error
}

func (f failingBody) Read([]byte) (int, error) { return 0, f.err }

func shaOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// faulty puts a shaFaults store under w's policy and item service.
func (w *policyWorld) faulty() *shaFaults {
	b := &shaFaults{Store: w.blobs}
	w.p.Blobs, w.p.Items.Blobs = b, b
	return b
}

// mapped is a check that refuses for any error, a failed read included,
// as most remote hooks map any error to source.ErrInvalidResult; asIs
// returns the read's error as it is.
func mapped(_ context.Context, r io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
	if _, err := io.ReadAll(r); err != nil {
		return source.ErrInvalidResult
	}
	return nil
}

func asIs(_ context.Context, r io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
	_, err := io.ReadAll(r)
	return err //nolint:wrapcheck // the read's error as it is
}

func (w *policyWorld) circuit() int {
	w.t.Helper()
	state, err := w.db.RemoteCache().State(w.t.Context(), w.c.Key)
	if err != nil {
		w.t.Fatal(err)
	}
	return state.ConnectionFailures
}

func wantStorageFault(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, format.ErrStorageFault) || errors.As(err, new(*source.HTTPError)) {
		t.Fatalf("want a storage fault, got %v", err)
	}
}

// A new remote file whose copy fails a read while the cache checks it is a
// storage fault, not the 502 an invalid result is: nothing is cached, the
// circuit is untouched, and once the store reads again the file is fetched
// and kept.
func TestAccPolicyFailedReadOfANewCopyIsAStorageFault(t *testing.T) {
	for _, fault := range []string{"read", "open"} {
		t.Run(fault, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("content")})
			q := w.request("file.bin", source.ResourceKindArtifact)
			q.Resource.Variant = "storage-fault-v1"
			q.Resource.Validate = func(ctx context.Context, r io.ReadSeeker, n int64) error {
				return mapped(ctx, r, n, source.MetadataResponse{})
			}
			b := w.faulty()
			b.sha = shaOf("content")
			if fault == "read" {
				b.read = errDropped
			} else {
				b.open = errDropped
			}
			_, err := w.read(q)
			wantStorageFault(t, err)
			if !errors.Is(err, errDropped) || w.circuit() != 0 {
				t.Fatalf("%v, circuit %d", err, w.circuit())
			}
			if _, err = w.db.Items().
				Get(t.Context(), w.c.Key, true, "file.bin"); !errors.Is(
				err,
				kernel.ErrNotFound,
			) {
				t.Fatalf("cached after a storage fault: %v", err)
			}
			b.read, b.open = nil, nil
			w.mustRead(q, "content")
			if got := w.up.Count(http.MethodGet, "/file.bin"); got != 2 {
				t.Fatalf("upstream GETs %d, want 2", got)
			}
		})
	}
}

// A refresh of expired metadata whose new copy fails a read while it is
// checked answers as before wherever that served the stale copy: a read
// that timed out or ended short, returned as it is by the check, still
// serves it and counts toward the circuit. Where it answered 502 with no
// stale copy, a check that maps the failure to an invalid result or one
// that returns another read error as it is, it is now a storage fault. The
// stale entry is kept either way.
func TestAccPolicyFailedReadOfARefreshedCopyKeepsStaleRules(t *testing.T) {
	short := fmt.Errorf("blob/s3: get x: %w", io.ErrUnexpectedEOF)
	for name, tc := range map[string]struct {
		check   func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error
		read    error
		stale   bool
		circuit int
	}{
		"mapped dropped":  {check: mapped, read: errDropped},
		"mapped timeout":  {check: mapped, read: timedOut{}},
		"mapped short":    {check: mapped, read: short},
		"as is dropped":   {check: asIs, read: errDropped},
		"as is timeout":   {check: asIs, read: timedOut{}, stale: true, circuit: 1},
		"as is short":     {check: asIs, read: short, stale: true, circuit: 1},
		"as is cancelled": {check: asIs, read: context.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
			q := w.request("meta.txt", source.ResourceKindMetadata)
			q.Resource.Variant = "storage-fault-v1"
			q.Resource.ValidateMetadata = tc.check
			w.mustRead(q, "good")
			w.clock.Advance(time.Hour)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("newer")})
			b := w.faulty()
			b.sha, b.read = shaOf("newer"), tc.read
			r, err := w.read(q)
			switch {
			case tc.stale:
				if err != nil || w.bytes(r) != "good" {
					t.Fatalf("stale copy: %v", err)
				}
			case errors.Is(tc.read, context.Canceled):
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			default:
				wantStorageFault(t, err)
			}
			if got := w.circuit(); got != tc.circuit {
				t.Fatalf("circuit %d, want %d", got, tc.circuit)
			}
			q.Repo.Offline = ptr(true)
			w.mustRead(q, "good")
		})
	}
}

// A 304 that re-admits the cached metadata reads the cached copy again. A
// failed open or read of it is a storage fault where it answered 502, and
// a read that timed out still answers 504.
func TestAccPolicyFailedReadOfARevalidatedCopy(t *testing.T) {
	for name, tc := range map[string]struct {
		open, read error
		timeout    bool
	}{
		"open":    {open: errDropped},
		"read":    {read: errDropped},
		"timeout": {read: timedOut{}, timeout: true},
	} {
		t.Run(name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/index.json", fakeupstream.Resource{Body: []byte("good"), ETag: `"one"`})
			q := w.request("index.json", source.ResourceKindMetadata)
			q.Resource.Variant = "storage-fault-v1"
			q.Resource.ValidateMetadata = mapped
			w.mustRead(q, "good")
			w.clock.Advance(time.Hour)
			w.up.Set(
				"/index.json",
				fakeupstream.Resource{Status: http.StatusNotModified, ETag: `"one"`},
			)
			b := w.faulty()
			b.sha, b.open, b.read = shaOf("good"), tc.open, tc.read
			_, err := w.read(q)
			if tc.timeout {
				var f *source.HTTPError
				if !errors.As(err, &f) || f.Status != http.StatusGatewayTimeout {
					t.Fatal(err)
				}
			} else {
				wantStorageFault(t, err)
				if !errors.Is(err, errDropped) {
					t.Fatal(err)
				}
			}
			b.open, b.read = nil, nil
			w.mustRead(q, "good")
		})
	}
}

// writeFaults is a blob store whose uploads fail at Append with write, or
// at Commit with commit, as a store that cannot keep the copy does.
type writeFaults struct {
	blob.Store
	write, commit error
}

func (b *writeFaults) BeginUpload(
	ctx context.Context,
	opts ...blob.UploadOption,
) (blob.Upload, error) {
	u, err := b.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store's own error
	}
	return failingUpload{Upload: u, write: b.write, commit: b.commit}, nil
}

type failingUpload struct {
	blob.Upload
	write, commit error
}

func (u failingUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if u.write != nil {
		return 0, u.write
	}
	return u.Upload.Append(ctx, r) //nolint:wrapcheck // the store's own error
}

func (u failingUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if u.commit != nil {
		return blob.Info{}, u.commit
	}
	return u.Upload.Commit(ctx, want) //nolint:wrapcheck // the store's own error
}

// publishFaults is the item service's store whose publication of a cache
// entry fails with err.
type publishFaults struct {
	item.Store
	err error
}

func (s *publishFaults) RemoteCache() port.RemoteCacheStore {
	return failingPut{RemoteCacheStore: s.Store.RemoteCache(), err: s.err}
}

type failingPut struct {
	port.RemoteCacheStore
	err error
}

func (f failingPut) Put(
	ctx context.Context,
	token model.RemoteCacheToken,
	e model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	if f.err != nil {
		return model.RemoteCacheEntry{}, f.err
	}
	return f.RemoteCacheStore.Put(ctx, token, e) //nolint:wrapcheck // the store's own error
}

// storeFaults puts a writeFaults blob store and a publishFaults store
// under w's item service, failing as write, commit and publish say, and
// returns the function that cures them.
func (w *policyWorld) storeFaults(write, commit, publish error) func() {
	b := &writeFaults{Store: w.blobs, write: write, commit: commit}
	s := &publishFaults{Store: w.db, err: publish}
	w.p.Items.Blobs, w.p.Items.Store = b, s
	return func() { b.write, b.commit, s.err = nil, nil, nil }
}

// A new remote file whose copy the blob store fails to write or commit, or
// whose cache entry the store fails to publish, is a storage fault, not
// the 502 protocol failure it was: nothing is cached, the circuit is
// untouched, and once the store works again the file is fetched and kept.
func TestAccPolicyFailedWriteOfANewCopyIsAStorageFault(t *testing.T) {
	for name, tc := range map[string]struct{ write, commit, publish error }{
		"write":   {write: errDropped},
		"commit":  {commit: errDropped},
		"publish": {publish: errDropped},
	} {
		t.Run(name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/file.bin", fakeupstream.Resource{Body: []byte("content")})
			q := w.request("file.bin", source.ResourceKindArtifact)
			cure := w.storeFaults(tc.write, tc.commit, tc.publish)
			_, err := w.read(q)
			wantStorageFault(t, err)
			if !errors.Is(err, errDropped) || w.circuit() != 0 {
				t.Fatalf("%v, circuit %d", err, w.circuit())
			}
			if _, err = w.db.Items().
				Get(t.Context(), w.c.Key, true, "file.bin"); !errors.Is(
				err,
				kernel.ErrNotFound,
			) {
				t.Fatalf("cached after a storage fault: %v", err)
			}
			cure()
			w.mustRead(q, "content")
			if got := w.up.Count(http.MethodGet, "/file.bin"); got != 2 {
				t.Fatalf("upstream GETs %d, want 2", got)
			}
		})
	}
}

// A refresh of expired metadata whose new copy the blob store fails to
// write answers as before wherever that served the stale copy: a write
// that timed out or ended short still serves it and counts toward the
// circuit, as main does. A write or publication that fails otherwise
// answered a 502 with no stale copy, and is now a storage fault. The
// stale entry is kept either way.
func TestAccPolicyFailedWriteOfARefreshedCopyKeepsStaleRules(t *testing.T) {
	short := fmt.Errorf("blob/s3: put x: %w", io.ErrUnexpectedEOF)
	for name, tc := range map[string]struct {
		write, publish error
		stale          bool
		circuit        int
	}{
		"write dropped":   {write: errDropped},
		"write timeout":   {write: timedOut{}, stale: true, circuit: 1},
		"write short":     {write: short, stale: true, circuit: 1},
		"publish dropped": {publish: errDropped},
	} {
		t.Run(name, func(t *testing.T) {
			w := policyFixture(t)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("good")})
			q := w.request("meta.txt", source.ResourceKindMetadata)
			w.mustRead(q, "good")
			w.clock.Advance(time.Hour)
			w.up.Set("/meta.txt", fakeupstream.Resource{Body: []byte("newer")})
			w.storeFaults(tc.write, nil, tc.publish)
			r, err := w.read(q)
			if tc.stale {
				if err != nil || w.bytes(r) != "good" {
					t.Fatalf("stale copy: %v", err)
				}
			} else {
				wantStorageFault(t, err)
			}
			if got := w.circuit(); got != tc.circuit {
				t.Fatalf("circuit %d, want %d", got, tc.circuit)
			}
			q.Repo.Offline = ptr(true)
			w.mustRead(q, "good")
		})
	}
}

// nthOpen is a blob store whose open number fail of sha, counted from 0,
// fails with errDropped.
type nthOpen struct {
	blob.Store
	sha         string
	fail, opens int
}

func (b *nthOpen) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha == b.sha {
		b.opens++
		if b.opens-1 == b.fail {
			return nil, blob.Info{}, errDropped
		}
	}
	return b.Store.Open(ctx, sha) //nolint:wrapcheck // the store's own error
}

// A Hex tarball's copy is read by the format's inspection and again by
// its Deployed hook inside the publication, which links the package
// index. A failed open of either is a storage fault, where both answered
// the 502 protocol failure of an upstream's bad tarball: nothing is
// cached, the circuit is untouched, and the next request fetches the
// tarball again.
func TestAccPolicyFailedReadOfAHexTarballsHooksIsAStorageFault(t *testing.T) {
	tarball := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/ecto-3.12.5.tar")
	w := policyFixture(t)
	w.c.PackageType = repo.Hex
	err := w.db.Repos().Update(t.Context(), w.c)
	if err != nil {
		t.Fatal(err)
	}
	if w.permit.Identity, err = w.db.RemoteCache().Source(t.Context(), w.c.Key); err != nil {
		t.Fatal(err)
	}
	w.p.Items.Formats = func(repo.PackageType) format.Handler { return hexfmt.Handler{} }
	w.up.Set("/tarballs/ecto-3.12.5.tar", fakeupstream.Resource{Body: tarball})
	q := w.request("tarballs/ecto-3.12.5.tar", source.ResourceKindArtifact)
	b := &nthOpen{Store: w.blobs, sha: shaOf(string(tarball))}
	w.p.Items.Blobs = b
	failed := 0
	for ; ; b.fail++ {
		b.opens = 0
		_, err = w.read(q)
		if err == nil {
			break
		}
		wantStorageFault(t, err)
		if !errors.Is(err, errDropped) || w.circuit() != 0 {
			t.Fatalf("open %d: %v, circuit %d", b.fail, err, w.circuit())
		}
		if _, err = w.db.Items().
			Get(t.Context(), w.c.Key, true, "tarballs/ecto-3.12.5.tar"); !errors.Is(
			err,
			kernel.ErrNotFound,
		) {
			t.Fatalf("open %d: cached after a storage fault: %v", b.fail, err)
		}
		failed++
	}
	if failed < 2 {
		t.Fatalf("only %d opens failed: the inspection and Deployed each must", failed)
	}
	if got := w.up.Count(http.MethodGet, "/tarballs/ecto-3.12.5.tar"); got != failed+1 {
		t.Fatalf("upstream GETs %d, want %d", got, failed+1)
	}
}
