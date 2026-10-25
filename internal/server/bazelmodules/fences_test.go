package bazelmodules_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type bazelBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (b bazelBackend) Read(ctx context.Context, r source.Request, p source.Permit) (source.Result, error) {
	return b(ctx, r, p)
}

// Every representation has a delivery fence. Revoking a source after its
// validated read must prevent a stale response, including archive and patch
// bytes that are served by the shared source-result adapter.
func TestAccSourceRevocationBeforeDelivery(t *testing.T) {
	module := []byte("module(name = \"hello\", version = \"1.0.0\")\n")
	archiveBytes := archive(t)
	archiveSum := sha256.Sum256(archiveBytes)
	patch := []byte("--- a/x\n+++ b/x\n")
	patchSum := sha256.Sum256(patch)
	doc, err := json.Marshal(map[string]any{
		"type": "archive", "url": "https://source.example/registry/archive.tar.gz",
		"integrity": "sha256-" + base64.StdEncoding.EncodeToString(archiveSum[:]),
		"patches":   map[string]string{"fix.patch": "sha256-" + base64.StdEncoding.EncodeToString(patchSum[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{
		"modules/hello/metadata.json":           []byte(`{"versions":["1.0.0"]}`),
		"modules/hello/1.0.0/MODULE.bazel":      module,
		"modules/hello/1.0.0/source.json":       doc,
		"modules/hello/1.0.0/patches/fix.patch": patch,
		"archives/hello/1.0.0/source.tar.gz":    archiveBytes,
	}
	for _, tc := range []struct{ name, target, request string }{
		{"metadata", "modules/hello/metadata.json", "modules/hello/metadata.json"},
		{"module", "modules/hello/1.0.0/MODULE.bazel", "modules/hello/1.0.0/MODULE.bazel"},
		{"source", "modules/hello/1.0.0/source.json", "modules/hello/1.0.0/source.json"},
		{"patch", "modules/hello/1.0.0/patches/fix.patch", "modules/hello/1.0.0/patches/fix.patch"},
		{"archive", "archives/hello/1.0.0/source.tar.gz", "archives/hello/1.0.0/source.tar.gz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := map[string]int{}
			backend := bazelBackend(func(_ context.Context, r source.Request, _ source.Permit) (source.Result, error) {
				body, ok := data[r.Resource.Path]
				if !ok {
					return source.Result{}, errors.New("unexpected Bazel source path")
				}
				if r.Resource.ValidateMetadata != nil {
					if err := r.Resource.ValidateMetadata(t.Context(), bytes.NewReader(body), int64(len(body)), source.MetadataResponse{}); err != nil {
						return source.Result{}, err
					}
				}
				return source.Result{
					Stream: &source.Stream{Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
						Representation: source.Representation{Size: int64(len(body))}},
					Revalidate: func(context.Context) error {
						checks[r.Resource.Path]++
						if r.Resource.Path == tc.target && checks[r.Resource.Path] >= 2 {
							return source.ErrDenied
						}
						return nil
					},
				}, nil
			})
			service := &source.Service{
				HTTP: backend, Cache: backend,
				Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
					return types.SourceIdentity{Kind: types.ExternalHTTP, Repository: "bazel-remote", ID: "source-1", Generation: "revision-1"}, nil
				},
			}
			f := newFixtureWith(t, func(*item.Service) *source.Service { return service })
			c := *repo.Defaults(&repo.Config{Key: "bazel-remote", Class: repo.Remote, PackageType: repo.BazelModules, URL: "https://source.example/registry/"})
			if err := f.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			w := f.request(http.MethodGet, "/artifactory/api/bazelmodules/"+c.Key+"/"+tc.request)
			if w.Code == 200 || bytes.Contains(w.Body.Bytes(), module) || bytes.Contains(w.Body.Bytes(), archiveBytes) {
				t.Fatalf("revoked %s served: %d %s", tc.name, w.Code, w.Body.String())
			}
			if checks[tc.target] < 2 {
				t.Fatalf("%s was not fenced at delivery: %+v, status %d", tc.target, checks, w.Code)
			}
		})
	}
}
