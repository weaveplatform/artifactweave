package debian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// digestBlobs serves each body under its SHA-256, as the blob store does.
type digestBlobs map[string][]byte

func (b digestBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	body, ok := b[sha]
	if !ok {
		return nil, blob.Info{}, errUnavailableBlob
	}
	return debianReadCloser{bytes.NewReader(body)}, blob.Info{Size: int64(len(body))}, nil
}

func (b digestBlobs) Stat(_ context.Context, sha string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b[sha]))}, nil
}

func (b digestBlobs) add(body []byte) string {
	sha := fmt.Sprintf("%x", sha256.Sum256(body))
	b[sha] = body
	return sha
}

// TestUnitMalformedBinaryLifecycle (#1355): a stored binary ReadBinary
// refuses gets deb.index.status in place of deb.name and deb.version, even
// when a client set those, and still queues the rebuild; a readable one
// gets its deb.name and deb.version and loses a deb.index.status a client
// set. Deleting or reindexing a malformed binary touches no version, and
// the reindex marks it as the deploy does.
func TestUnitMalformedBinaryLifecycle(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
	st := inmem.New()
	if err := st.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	blobs := digestBlobs{}
	put := func(name string, body []byte, props model.Properties) model.Item {
		t.Helper()
		it, err := st.Items().Put(ctx, model.Item{
			RepoKey: c.Key, Name: name, Size: int64(len(body)),
			Actual: kernel.Checksums{SHA256: blobs.add(body)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Items().SetProperties(ctx, it.ID, props, time.Time{}); err != nil {
			t.Fatal(err)
		}
		return it
	}
	bad := put("pool/main/b/bad/bad_1.0_amd64.deb", []byte("not a debian package"),
		model.Properties{PropName: {"forged"}, PropVersion: {"9"}})
	good := put("pool/main/h/hello/hello_1.0-1_amd64.deb", fixture(t, validControl),
		model.Properties{PropIndexStatus: {"stale"}})
	queued := 0
	env := format.Env{
		Store: st,
		Blobs: blobs,
		Enqueue: func(context.Context, string, string, []byte) error {
			queued++
			return nil
		},
	}
	wantProps := func(it model.Item, want model.Properties) {
		t.Helper()
		got, err := st.Items().Properties(ctx, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{PropName, PropVersion, PropIndexStatus} {
			if !slices.Equal(got[key], want[key]) {
				t.Errorf("%s %s = %q, want %q", it.Name, key, got[key], want[key])
			}
		}
	}
	malformed := model.Properties{PropIndexStatus: {"debian: invalid binary package: ar magic"}}
	readable := model.Properties{PropName: {"hello"}, PropVersion: {"1.0-1"}}
	for _, it := range []model.Item{bad, good} {
		if err := h.Deployed(ctx, env, c, it); err != nil {
			t.Fatalf("deploy of %s: %v", it.Name, err)
		}
	}
	wantProps(bad, malformed)
	wantProps(good, readable)
	if queued != 2 {
		t.Errorf("queued %d rebuilds, want 2", queued)
	}
	packages, _, err := st.Packages().List(ctx, c.Key, kernel.Page{})
	if err != nil || len(packages) != 1 || packages[0].Name != "hello" {
		t.Fatalf("package index holds %+v, %v; want hello alone", packages, err)
	}
	if err := h.Deleted(ctx, env, c, []model.Item{bad}); err != nil {
		t.Fatalf("delete of a malformed package: %v", err)
	}
	if err := st.Items().SetProperties(ctx, bad.ID,
		model.Properties{PropName: {"forged"}}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatalf("reindex with a malformed package: %v", err)
	}
	wantProps(bad, malformed)
	wantProps(good, readable)
	packages, _, err = st.Packages().List(ctx, c.Key, kernel.Page{})
	if err != nil || len(packages) != 1 || packages[0].Name != "hello" {
		t.Fatalf("package index after reindex holds %+v, %v; want hello alone", packages, err)
	}
}

// TestUnitReadBinaryFaultIsNotMalformed: a read that stops for another
// reason than the package's bytes, a canceled request here, is an error,
// not a malformed package to store, nor the 400 the content refusal gives.
func TestUnitReadBinaryFaultIsNotMalformed(t *testing.T) {
	blobs := digestBlobs{}
	sha := blobs.add(fixture(t, validControl))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, fault, err := readBinary(ctx, blobs, sha)
	var reject *format.RejectionError
	if fault != "" || !errors.Is(err, context.Canceled) || errors.As(err, &reject) {
		t.Fatalf("canceled read gave fault %q, error %v; want the cancellation alone", fault, err)
	}
}
