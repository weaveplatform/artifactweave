package hex_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/blobtest"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

// errStoreRead is a blob store's failed read of a copy it opened.
var errStoreRead = errors.New("hex test: the store failed to read the copy")

// failedRead is a served copy whose reads fail as the store's do.
type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, errStoreRead }
func (failedRead) Close() error             { return nil }

// A remote Hex tarball, and the signed registry resource the route reads
// for it first, read the copy the source service serves them, a stream or,
// for the tarball, a cached item. A failed read of that copy is the
// server's storage fault, 500 "internal error", logged once; the route
// answered 502 "invalid upstream Hex registry resource", as for a copy that
// fails its checks (#1488).
func TestAccHexRemoteAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	const tarball = "tarballs/decimal-2.0.0.tar"
	keys := testKeys()
	signer, err := keymaterial.RSAPrivateKey(keys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture := mustReadHexFixture(t)
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := hexfmt.EncodePackage(hexfmt.RegistryPackage{
		Name: "decimal", Repository: "hexpm", Releases: []hexfmt.Package{pkg},
	}, signer)
	if err != nil {
		t.Fatal(err)
	}
	cached := fmt.Sprintf("%x", sha256.Sum256(fixture))
	failing, served := "", "stream"
	backend := hexSource(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			body := registry
			if q.Resource.Path == tarball {
				body = fixture
				if failing == tarball && served == "item" {
					return source.Result{Item: &artmodel.Item{
						RepoKey: "hex-r", Path: "tarballs", Name: "decimal-2.0.0.tar", Cached: true,
						Actual: kernel.Checksums{SHA256: cached},
					}}, nil
				}
			}
			var rc io.ReadCloser = io.NopCloser(bytes.NewReader(body))
			if failing == q.Resource.Path {
				rc = failedRead{}
			}
			return source.Result{Stream: &source.Stream{
				Body: rc, Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	var logs bytes.Buffer
	blobs := &blobtest.ReadFaults{Err: errStoreRead}
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP: backend,
			Resolve: func(context.Context, repo.Config) (artmodel.SourceIdentity, error) {
				return artmodel.SourceIdentity{
					Kind:       artmodel.ExternalHTTP,
					Repository: "hex-r",
					ID:         "upstream",
					Generation: "rev-1",
				}, nil
			},
			Revalidate: func(context.Context, source.Request, source.Permit) error { return nil },
		}
		blobs.Store = c.Blobs
		c.Blobs = blobs
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	if err = w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-key", Type: api.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	if err = w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "hex-r", Class: repo.Remote, PackageType: repo.Hex,
		URL: "https://up.example/", PrimaryKeyPairRef: "hex-key", HexPublicKey: keys.public,
	})); err != nil {
		t.Fatal(err)
	}
	// The cached tarball's bytes, which the item answer names.
	if got := w.do(http.MethodPut, "/artifactory/other-local/decimal-2.0.0.tar", "admin",
		string(fixture)); got.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", got.Code, got.Body)
	}
	blobs.Cut(cached, 0)
	for _, c := range []struct{ failing, served string }{
		{tarball, "stream"}, {tarball, "item"}, {"packages/decimal", "stream"},
	} {
		failing, served = c.failing, c.served
		logs.Reset()
		got := w.do(http.MethodGet, "/artifactory/api/hex/hex-r/"+tarball, "admin", "")
		if got.Code != http.StatusInternalServerError ||
			got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
			t.Errorf("%s %s: %d %q", c.failing, c.served, got.Code, got.Body)
		}
		if n := strings.Count(logs.String(), errStoreRead.Error()); n != 1 {
			t.Errorf("%s %s: the fault was logged %d times, want once:\n%s", c.failing, c.served,
				n, logs.String())
		}
	}
}
