package maven_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	mavenfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type metadataBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (b metadataBackend) Read(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
	return b(ctx, q, p)
}

type metadataSourceBody struct {
	io.ReadCloser
	failure error
	ended   func()
	closed  *int
}

func (b *metadataSourceBody) Read(p []byte) (int, error) {
	if b.failure != nil {
		return 0, b.failure
	}
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF && b.ended != nil {
		b.ended()
	}
	return n, err
}
func (b *metadataSourceBody) Close() error { (*b.closed)++; return b.ReadCloser.Close() }

func TestAccVirtualMetadataSourceFailuresAndFinalAuthority(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Maven, repo.Generic} {
				t.Run(string(kind), func(t *testing.T) {
					for _, mode := range []string{"served-copy", "served-deadline", "served-store", "served-marked-storage", "upstream503", "malformed", "upstream-timeout", "source-denied", "source-missing", "cache-store", "cache-store-afterlocal", "late-denied", "late-store", "late-superseded"} {
						t.Run(mode, func(t *testing.T) {
							w := metadataFaultWorld(t, kind, backend)
							putMetadata(t, w, "second", virtualMetadataPath, virtualMetadata("2.0"), 100)
							w.Repo.PriorityResolution = repo.Flex(mode != "cache-store-afterlocal")
							w.Update()
							c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
							if err != nil {
								t.Fatal(err)
							}
							c.Repositories = []string{"remote", "second"}
							if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
								t.Fatal(err)
							}
							remote := fakeupstream.Resource{Body: []byte(virtualMetadata("1.0"))}
							if mode == "upstream503" {
								remote = fakeupstream.Resource{Status: 503}
							}
							if mode == "malformed" {
								remote.Body = []byte("<broken")
							}
							w.Upstream.Set("/"+virtualMetadataPath, remote)
							svc := *w.Runtime.Artifacts.RemoteSources()
							original := svc.HTTP
							closed, checks, used := 0, 0, 0
							finished := false
							svc.HTTP = metadataBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
								switch mode {
								case "upstream-timeout":
									return source.Result{}, context.DeadlineExceeded
								case "source-denied":
									return source.Result{}, source.ErrDenied
								case "source-missing":
									return source.Result{}, fmt.Errorf("member absent: %w", kernel.ErrNotFound)
								case "cache-store", "cache-store-afterlocal":
									return source.Result{}, fmt.Errorf("cache read: %w", format.ErrStorageFault)
								}
								result, err := original.Read(ctx, q, p)
								if err != nil || result.Stream == nil {
									return result, err
								}
								var failure error
								if mode == "served-copy" {
									failure = io.ErrUnexpectedEOF
								}
								if mode == "served-deadline" {
									failure = fmt.Errorf("copy deadline: %w", context.DeadlineExceeded)
								}
								if mode == "served-store" {
									failure = fmt.Errorf("copy read: %w", kernel.ErrStore)
								}
								if mode == "served-marked-storage" {
									failure = fmt.Errorf("copy read: %w", format.ErrStorageFault)
								}
								result.Stream.Body = &metadataSourceBody{ReadCloser: result.Stream.Body, failure: failure, ended: func() { finished = true }, closed: &closed}
								previous := result.Revalidate
								result.Revalidate = func(ctx context.Context) error {
									checks++
									if finished {
										switch mode {
										case "late-denied":
											return source.ErrDenied
										case "late-store":
											return fmt.Errorf("copy state: %w", format.ErrStorageFault)
										case "late-superseded":
											return source.ErrSuperseded
										}
									}
									if previous != nil {
										return previous(ctx)
									}
									return nil
								}
								previousUsed := result.Used
								result.Used = func(ctx context.Context) error {
									used++
									if previousUsed != nil {
										return previousUsed(ctx)
									}
									return nil
								}
								return result, nil
							})
							w.H = harness.Router(
								harness.Config{
									Store:         w.Runtime.Store,
									Blobs:         w.Runtime.Blobs,
									Auth:          w.Runtime.Auth,
									Tokens:        w.Runtime.Tokens,
									Keys:          w.Runtime.Keys,
									RemoteSources: &svc,
									RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
								},
							)
							response := w.Request("GET", "/artifactory/virtual/"+virtualMetadataPath, "admin", "")
							want := 200
							switch mode {
							case "cache-store", "cache-store-afterlocal", "late-store":
								want = 500
							case "late-denied":
								want = 403
							case "late-superseded":
								want = 503
							}
							harness.WantStatus(t, response, want)
							if want == 200 && !strings.Contains(response.Body.String(), "<version>2.0</version>") {
								t.Fatal("later authorized local part lost", response.Body.String())
							}
							if (strings.HasPrefix(mode, "served-") || strings.HasPrefix(mode, "late-")) && closed != 1 {
								t.Fatal("source body not closed", closed)
							}
							if strings.HasPrefix(mode, "late-") && (checks < 3 || strings.Contains(response.Body.String(), "<metadata")) {
								t.Fatal("final source fence missed", checks, response.Body.String())
							}
							if want != 200 && used != 0 {
								t.Fatal("failed metadata delivery recorded use", used)
							}
							if mode == "late-superseded" && response.Header().Get("Retry-After") != "1" {
								t.Fatal(response.Header())
							}
						})
					}
				})
			}
		})
	}
}

func TestAccVirtualMetadataResolveConfigurationFault(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := metadataFaultWorld(t, repo.Maven, backend)
			putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("1.0"), 100)
			c, err := w.Runtime.Store.Repos().Get(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			c.HandleReleases = new(false)
			if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			root, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
			if err != nil {
				t.Fatal(err)
			}
			root.Repositories = []string{"local"}
			if err = w.Runtime.Store.Repos().Update(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			w.H = harness.Router(
				harness.Config{
					Store:  w.Runtime.Store,
					Blobs:  w.Runtime.Blobs,
					Auth:   w.Runtime.Auth,
					Tokens: w.Runtime.Tokens,
					Keys:   w.Runtime.Keys,
					Logger: slog.New(slog.NewTextHandler(&logs, nil)),
				},
			)
			path := "/artifactory/virtual/" + virtualMetadataPath
			harness.WantStatus(t, w.Request("GET", path, "admin", ""), 200)
			db := w.Runtime.Store.DB()
			if _, err = db.ExecContext(t.Context(), `ALTER TABLE config_documents RENAME TO config_documents_held`); err != nil {
				t.Fatal(err)
			}
			response := w.Request("GET", path, "admin", "")
			if _, err = db.ExecContext(t.Context(), `ALTER TABLE config_documents_held RENAME TO config_documents`); err != nil {
				t.Fatal(err)
			}
			harness.WantStatus(t, response, 500)
			if !strings.Contains(logs.String(), "config_documents") || strings.Count(logs.String(), "Maven metadata storage") != 1 {
				t.Fatal("underlying configuration statement fault not logged once", logs.String())
			}
			if strings.Contains(response.Body.String(), "config_documents") {
				t.Fatal("database details disclosed", response.Body.String())
			}
		})
	}
}

type metadataCancelledReader struct {
	io.ReadSeekCloser
	cancel context.CancelFunc
	closed *int
}

func (r *metadataCancelledReader) Read([]byte) (int, error) { r.cancel(); return 0, context.Canceled }

func (r *metadataCancelledReader) Close() error { (*r.closed)++; return r.ReadSeekCloser.Close() }

type metadataCancelStore struct {
	blob.Store
	cancel context.CancelFunc
	closed *int
}

func (b metadataCancelStore) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	body, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return body, info, err
	}
	return &metadataCancelledReader{ReadSeekCloser: body, cancel: b.cancel, closed: b.closed}, info, nil
}

func TestAccVirtualMetadataEndedRequestIsNotStorageFailure(t *testing.T) {
	w := metadataWorld(t, repo.Generic)
	putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("1.0"), 100)
	c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"local"}
	if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	closed := 0
	metadataFaultRouter(w, metadataCancelStore{Store: w.Runtime.Blobs, cancel: cancel, closed: &closed})
	request := httptest.NewRequestWithContext(ctx, "GET", "/artifactory/virtual/"+virtualMetadataPath, nil)
	request.SetBasicAuth("admin", "pw")
	response := httptest.NewRecorder()
	w.H.ServeHTTP(response, request)
	harness.WantStatus(t, response, 404)
	if closed != 1 || strings.Contains(response.Body.String(), "internal error") {
		t.Fatal("ended request relabeled as storage", closed, response.Body.String())
	}
}

func TestAccVirtualMetadataContentFailureRetainsMemberDenial(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := metadataFaultWorld(t, repo.Generic, backend)
			putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("1.0"), 100)
			w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("2.0"))})
			svc := *w.Runtime.Artifacts.RemoteSources()
			original := svc.HTTP
			closed := 0
			svc.HTTP = metadataBackend(func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
				result, err := original.Read(ctx, q, p)
				if err == nil && result.Stream != nil {
					result.Stream.Body = &metadataSourceBody{ReadCloser: result.Stream.Body, failure: fmt.Errorf("member content: %w", format.ErrStorageFault), closed: &closed}
				}
				return result, err
			})
			w.H = harness.Router(
				harness.Config{
					Store:         w.Runtime.Store,
					Blobs:         w.Runtime.Blobs,
					Auth:          w.Runtime.Auth,
					Tokens:        w.Runtime.Tokens,
					Keys:          w.Runtime.Keys,
					RemoteSources: &svc,
					RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
				},
			)
			path := "/artifactory/virtual/" + virtualMetadataPath
			harness.WantStatus(t, w.Request("GET", path, "admin", ""), 200)
			denied := w.Request("GET", path, "reader", "")
			harness.WantStatus(t, denied, 403)
			if !strings.Contains(denied.Body.String(), "local:"+virtualMetadataPath) {
				t.Fatal("retained denial replaced", denied.Body.String())
			}
			if closed != 2 {
				t.Fatal("content readers not closed", closed)
			}
		})
	}
}

type metadataCachedOpenFault struct {
	blob.Store
	target  string
	calls   *int
	failure error
}

func (b metadataCachedOpenFault) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	if sha != b.target {
		return b.Store.Open(ctx, sha)
	}
	(*b.calls)++
	return nil, blob.Info{}, fmt.Errorf("cached content open: %w", b.failure)
}

func TestAccVirtualMetadataCachedContentAcquisitionKeepsPassover(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, fault := range []struct {
			name string
			err  error
		}{{"format-marker", format.ErrStorageFault}, {"database-marker", kernel.ErrStore}} {
			t.Run(backend+"/"+fault.name, func(t *testing.T) {
				w := metadataFaultWorld(t, repo.Maven, backend)
				putMetadata(t, w, "second", virtualMetadataPath, virtualMetadata("2.0"), 100)
				w.Repo.PriorityResolution = repo.Flex(true)
				w.Update()
				c, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
				if err != nil {
					t.Fatal(err)
				}
				c.Repositories = []string{"remote", "second"}
				if err = w.Runtime.Store.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				raw := virtualMetadata("1.0")
				w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(raw)})
				path := "/artifactory/virtual/" + virtualMetadataPath
				harness.WantStatus(t, w.Request("GET", path, "admin", ""), 200)
				svc := *w.Runtime.Artifacts.RemoteSources()
				backend, ok := svc.HTTP.(*mavenfmt.MavenBackend)
				if !ok {
					t.Fatalf("unexpected runtime backend %T", svc.HTTP)
				}
				copyBackend := *backend
				originalPolicy := backend.Policy
				// Reuse runtime dependencies, with fresh synchronization state. This plain
				// controlled HTTP source has no smart peer to delegate back to the original.
				policy := &remote.Policy{
					MetadataChanged: originalPolicy.MetadataChanged,
					Fetch:           originalPolicy.Fetch, GitFetch: originalPolicy.GitFetch,
					PrepareUpload: originalPolicy.PrepareUpload, ProjectResponse: originalPolicy.ProjectResponse,
					Store: originalPolicy.Store, Blobs: originalPolicy.Blobs, Items: originalPolicy.Items,
					Pool: originalPolicy.Pool, Options: originalPolicy.Options, CheckCurrent: originalPolicy.CheckCurrent,
					Key: originalPolicy.Key, Now: originalPolicy.Now, MetadataLimit: originalPolicy.MetadataLimit,
				}
				calls := 0
				policy.Blobs = metadataCachedOpenFault{Store: policy.Blobs, target: pomSums(raw).SHA256, calls: &calls, failure: fault.err}
				copyBackend.Policy = policy
				svc.HTTP = &copyBackend
				w.H = harness.Router(
					harness.Config{
						Store:         w.Runtime.Store,
						Blobs:         w.Runtime.Blobs,
						Auth:          w.Runtime.Auth,
						Tokens:        w.Runtime.Tokens,
						Keys:          w.Runtime.Keys,
						RemoteSources: &svc,
						RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
					},
				)
				// Trusted storage markers answer direct source500; the selected-content
				// origin still lets metadata keep a healthy member's partial response.
				harness.WantStatus(t, w.Request("GET", "/artifactory/remote/"+virtualMetadataPath, "admin", ""), 500)
				calls = 0
				response := w.Request("GET", path, "admin", "")
				if calls != 1 {
					t.Fatal("actual cached-blob Open boundary not exercised", calls)
				}
				harness.WantStatus(t, response, 200)
				if !strings.Contains(response.Body.String(), "<version>2.0</version>") {
					t.Fatal("healthy later member lost", response.Body.String())
				}
			})
		}
	}
}
