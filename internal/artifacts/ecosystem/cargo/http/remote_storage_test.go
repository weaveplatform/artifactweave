package cargohttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

var errDropped = errors.New("the store dropped the connection")

// copyFaults is a blob store that, once cut, opens sha as content that can
// only seek, as S3's, GCS's and Azure's does, whose first sound reads are
// served and whose later ones fail with errDropped.
type copyFaults struct {
	blob.Store
	mu    sync.Mutex
	sha   string
	sound int
}

func (b *copyFaults) cut(sha string, sound int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sha, b.sound = sha, sound
}

func (b *copyFaults) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := b.Store.Open(ctx, sha)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil || sha != b.sha {
		return r, info, err //nolint:wrapcheck // the store's own error
	}
	return &cutCopy{r: r, sound: b.sound}, info, nil
}

type cutCopy struct {
	r     io.ReadSeekCloser
	sound int
}

func (c *cutCopy) Read(p []byte) (int, error) {
	if c.sound == 0 {
		return 0, errDropped
	}
	c.sound--
	return c.r.Read(p) //nolint:wrapcheck // the store's own error
}

func (c *cutCopy) Seek(off int64, whence int) (int64, error) {
	return c.r.Seek(off, whence) //nolint:wrapcheck // the store's own error
}

func (c *cutCopy) Close() error { return c.r.Close() } //nolint:wrapcheck // the store's own error

// faultyRemote is a remote Cargo repository over up whose blob store, which
// the remote policy and the item service share, is blobs.
func faultyRemote(t *testing.T, up *fakeupstream.Server, blobs *copyFaults) *fixture {
	t.Helper()
	return newFixture(t, repo.Remote, func(db *inmem.Store, items *item.Service) *source.Service {
		blobs.Store = items.Blobs
		items.Blobs = blobs
		c, err := items.Store.Repos().Get(t.Context(), "cargo-test")
		if err != nil {
			t.Fatal(err)
		}
		c.URL = up.URL + "/cargo/"
		if err = items.Store.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		key := []byte(strings.Repeat("k", 32))
		resolver, err := upstream.NewResolver(db, nil, upstream.ResolverOptions{RevisionKey: key})
		if err != nil {
			t.Fatal(err)
		}
		pool := new(upstream.Pool)
		t.Cleanup(pool.Close)
		policy := &remote.Policy{
			Store:   db,
			Blobs:   items.Blobs,
			Items:   items,
			Pool:    pool,
			Options: resolver.Options,
			Key:     key,
		}
		return &source.Service{
			HTTP: policy, Cache: policy,
			Resolve: func(ctx context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return items.Store.RemoteCache().Source(ctx, c.Key)
			},
		}
	})
}

// serveCrate has up index hello_cargo 1.0.0 with crate's checksum and
// serve crate as its download, and returns that checksum.
func serveCrate(up *fakeupstream.Server, crate []byte) string {
	sum := sha256.Sum256(crate)
	sha := hex.EncodeToString(sum[:])
	up.Set(
		"/cargo/config.json",
		fakeupstream.Resource{Body: []byte(`{"dl":"` + up.URL + `/cargo/crates"}`)},
	)
	up.Set("/cargo/he/ll/hello_cargo", fakeupstream.Resource{Body: []byte(
		`{"name":"hello_cargo","vers":"1.0.0","deps":[],"features":{},"cksum":"` + sha + `"}` + "\n",
	)})
	up.Set("/cargo/crates/hello_cargo/1.0.0/download", fakeupstream.Resource{Body: crate})
	return sha
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Fatalf("%d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// A remote crate whose cached copy fails a read while Cargo checks it, in
// the archive or in the checksum pass, answers 500 "internal error" and
// is not cached, so the next request fetches it again. It answered 502
// {"errors":[{"detail":"Bad Gateway"}]}, as for an upstream's bad crate;
// the 500 is the router's, as for the Cargo routes' other server faults. A
// crate that matches the index's checksum but is no gzip archive still
// answers that 502.
func TestAccRemoteCrateAnswersAFailedReadOfItsCopyAsAServerFault(t *testing.T) {
	const (
		download = "/artifactory/api/cargo/cargo-test/api/v1/crates/hello_cargo/1.0.0/download"
		cached   = "crates/hello_cargo/hello_cargo-1.0.0.crate"
	)
	up := fakeupstream.New(t, fakeupstream.Options{})
	blobs := new(copyFaults)
	f := faultyRemote(t, up, blobs)
	sha := serveCrate(up, archive(t, "hello_cargo", "1.0.0"))
	failed := 0
	for sound := 0; ; sound++ {
		blobs.cut(sha, sound)
		rec := f.do(http.MethodGet, download, "admin", nil)
		if rec.Code == http.StatusOK {
			break
		}
		wantError(t, rec, http.StatusInternalServerError, "internal error")
		if _, err := f.db.Items().Get(t.Context(), "cargo-test", true, cached); err == nil {
			t.Fatalf("%d sound reads: the crate was cached", sound)
		}
		failed++
	}
	if failed < 2 {
		t.Fatalf("only %d reads failed", failed)
	}
	if got := up.Count(
		http.MethodGet,
		"/cargo/crates/hello_cargo/1.0.0/download",
	); got != failed+1 {
		t.Fatalf("upstream fetched the crate %d times, want %d", got, failed+1)
	}
	if _, err := f.db.Items().Get(t.Context(), "cargo-test", true, cached); err != nil {
		t.Fatal(err)
	}
	up = fakeupstream.New(t, fakeupstream.Options{})
	f = faultyRemote(t, up, new(copyFaults))
	serveCrate(up, []byte("no gzip archive"))
	rec := f.do(http.MethodGet, download, "admin", nil)
	if rec.Code != http.StatusBadGateway ||
		strings.TrimSpace(rec.Body.String()) != `{"errors":[{"detail":"Bad Gateway"}]}` {
		t.Fatalf("an invalid crate: %d %s", rec.Code, rec.Body)
	}
}
