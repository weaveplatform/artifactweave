package pypihttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/blake2b"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

func TestUnitCachedPyPIArtifactChecksEveryAdvertisedHash(t *testing.T) {
	storage, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("verified distribution bytes")
	upload, err := storage.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = upload.Append(t.Context(), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	counted := &hashReadBlobs{Store: storage}
	service := &item.Service{Blobs: counted, Formats: ecosystem.For}
	h := handler{
		k: pkgroute.Mount(
			chi.NewRouter(),
			pkgroute.Options{Items: func() *item.Service { return service }},
			Protocol(Options{}),
		),
	}
	result := source.Result{Item: &model.Item{Actual: kernel.Checksums{
		SHA256: info.Digests.SHA256,
		SHA512: info.Digests.SHA512,
		SHA1:   info.Digests.SHA1,
		MD5:    info.Digests.MD5,
	}}}
	for _, tc := range []struct {
		name, digest string
		opens        int
	}{
		{"sha256", info.Digests.SHA256, 0},
		{"sha512", info.Digests.SHA512, 0},
		{"sha1", info.Digests.SHA1, 0},
		{"md5", info.Digests.MD5, 0},
		{"sha384", fmt.Sprintf("%x", sha512.Sum384(data)), 1},
		{"sha224", fmt.Sprintf("%x", sha256.Sum224(data)), 1},
		{"blake2b_256", fmt.Sprintf("%x", blake2b.Sum256(data)), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counted.opens, counted.closes = 0, 0
			pins := map[string]string{tc.name: strings.ToUpper(tc.digest)}
			if err := h.checkRemoteHashes(t.Context(), result, pins); err != nil {
				t.Fatalf("matching cached hash rejected: %v", err)
			}
			if counted.opens != tc.opens || counted.closes != tc.opens {
				t.Fatalf("blob reads=%d closes=%d want=%d", counted.opens, counted.closes, tc.opens)
			}
			// A matching SHA256 must not mask a conflicting additional hash.
			pins["sha256"] = info.Digests.SHA256
			pins[tc.name] = strings.Repeat("0", len(tc.digest))
			if err := h.checkRemoteHashes(t.Context(), result, pins); err == nil {
				t.Fatal("conflicting cached hash accepted")
			}
		})
	}
	result.Item.Actual.SHA256 = strings.Repeat("0", 64)
	if err := h.checkRemoteHashes(
		t.Context(),
		result,
		map[string]string{"sha384": fmt.Sprintf("%x", sha512.Sum384(data))},
	); err == nil {
		t.Fatal("missing cached content passed hash verification")
	}
}

type hashReadBlobs struct {
	blob.Store
	opens, closes int
}

func (b *hashReadBlobs) Open(
	ctx context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	b.opens++
	r, info, err := b.Store.Open(ctx, digest)
	if err != nil {
		return nil, info, err
	}
	return hashReadCloser{ReadSeekCloser: r, owner: b}, info, nil
}

type hashReadCloser struct {
	io.ReadSeekCloser
	owner *hashReadBlobs
}

func (r hashReadCloser) Close() error { r.owner.closes++; return r.ReadSeekCloser.Close() }
