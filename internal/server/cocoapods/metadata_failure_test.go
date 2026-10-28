package cocoapods

import (
	"context"
	"io"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"testing"

	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type metadataBackend func(source.Request) (source.Result, error)

func (f metadataBackend) Read(
	_ context.Context,
	q source.Request,
	_ source.Permit,
) (source.Result, error) {
	return f(q)
}

type metadataBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *metadataBody) Close() error {
	b.closed.Store(true)
	return nil
}

func metadataResult(body io.ReadCloser) source.Result {
	return source.Result{Stream: &source.Stream{
		Body: body, Status: http.StatusOK,
		Representation: source.Representation{Size: -1},
	}}
}

func TestAccMetadataRequiresStreamRepresentation(t *testing.T) {
	// A stored item is a valid shared-source result, but CDN metadata must be
	// read, checked and rewritten before any bytes can reach the native client.
	for _, tc := range []struct {
		repo, resource string
		status         int
	}{
		{"pods-remote", "all_pods.txt", http.StatusInternalServerError},
		{"pods-remote", "sources/" + sourcePath("RemotePod", "2.0.0", ".tar.gz"), http.StatusBadGateway},
		{"pods-virtual", "all_pods.txt", http.StatusBadGateway},
		{"pods-virtual", podfmt.SpecPath("RemotePod", "2.0.0"), http.StatusBadGateway},
		{"pods-virtual", "sources/" + sourcePath("RemotePod", "2.0.0", ".tar.gz"), http.StatusBadGateway},
	} {
		t.Run(tc.repo+"/"+tc.resource, func(t *testing.T) {
			f := newRouteFixture(t)
			calls := 0
			f.source.HTTP = metadataBackend(func(q source.Request) (source.Result, error) {
				calls++
				dir, name := path.Split(q.Resource.Path)
				return source.Result{Item: &types.Item{
					RepoKey: q.Repo.Key,
					Path:    strings.TrimSuffix(dir, "/"),
					Name:    name,
					Cached:  true,
				}}, nil
			})
			response := f.get(tc.repo, tc.resource)
			if response.Code != tc.status || calls != 1 {
				t.Fatalf(
					"response = %d %s; backend calls = %d",
					response.Code,
					response.Body,
					calls,
				)
			}
		})
	}
}

func TestAccMetadataReadFailuresPreventArchiveFetch(t *testing.T) {
	for _, tc := range []struct{ repo, resource string }{
		{"pods-remote", "sources/" + sourcePath("RemotePod", "2.0.0", ".tar.gz")},
		{"pods-virtual", "sources/" + sourcePath("RemotePod", "2.0.0", ".tar.gz")},
		{"pods-virtual", "all_pods.txt"},
	} {
		t.Run(tc.repo+"/"+tc.resource, func(t *testing.T) {
			f := newRouteFixture(t)
			body := &metadataBody{Reader: brokenCDNBody{}}
			calls := 0
			f.source.HTTP = metadataBackend(func(q source.Request) (source.Result, error) {
				calls++
				if q.Resource.Kind != source.ResourceKindMetadata {
					t.Error("archive fetch attempted after unreadable metadata")
				}
				return metadataResult(body), nil
			})
			response := f.get(tc.repo, tc.resource)
			if response.Code != http.StatusBadGateway || !body.closed.Load() || calls != 1 {
				t.Fatalf(
					"response = %d %s; closed = %t, calls = %d",
					response.Code,
					response.Body,
					body.closed.Load(),
					calls,
				)
			}
			if strings.Contains(response.Body.String(), "upstream stream broke") {
				t.Fatal("backend error leaked to client")
			}
		})
	}
}

func TestAccVirtualSourceRejectsCorruptedMetadata(t *testing.T) {
	for _, tc := range []struct {
		origin string
		status int
	}{
		{`{"svn":"https://private.example/secret"}`, http.StatusNotImplemented},
		{`{"git":"https://private.example/secret.git","tag":"2.0.0","depth":1}`, http.StatusBadGateway},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			f := newRouteFixture(t)
			body := &metadataBody{Reader: strings.NewReader(
				`{"name":"RemotePod","version":"2.0.0","source":` + tc.origin + `}`,
			)}
			calls := 0
			f.source.HTTP = metadataBackend(func(source.Request) (source.Result, error) {
				calls++
				return metadataResult(body), nil
			})
			response := f.get(
				"pods-virtual",
				"sources/"+sourcePath("RemotePod", "2.0.0", ".tar.gz"),
			)
			if response.Code != tc.status || !body.closed.Load() || calls != 1 ||
				strings.Contains(response.Body.String(), "private.example") {
				t.Fatalf(
					"response = %d %s; closed = %t, calls = %d",
					response.Code,
					response.Body,
					body.closed.Load(),
					calls,
				)
			}
		})
	}
}

func TestAccSourceArchiveRechecksAuthorityBeforeDelivery(t *testing.T) {
	for _, revoke := range []string{"archive", "spec"} {
		t.Run(revoke, func(t *testing.T) {
			f := newRouteFixture(t)
			fetched := false
			checks := 0
			used := false
			archive := &metadataBody{Reader: strings.NewReader("private archive bytes")}
			f.source.HTTP = metadataBackend(func(q source.Request) (source.Result, error) {
				if q.Resource.Kind == source.ResourceKindMetadata {
					result := metadataResult(io.NopCloser(strings.NewReader(
						`{"name":"RemotePod","version":"2.0.0","source":{"http":"` + f.up.URL + `/RemotePod.tar.gz"}}`,
					)))
					result.Revalidate = func(context.Context) error {
						if revoke == "spec" && fetched {
							return source.ErrDenied
						}
						return nil
					}
					return result, nil
				}
				fetched = true
				result := metadataResult(archive)
				result.Revalidate = func(context.Context) error {
					checks++
					if revoke == "archive" && checks > 1 {
						return source.ErrDenied
					}
					return nil
				}
				result.Used = func(context.Context) error { used = true; return nil }
				return result, nil
			})
			response := f.get("pods-remote", "sources/"+sourcePath("RemotePod", "2.0.0", ".tar.gz"))
			if response.Code != http.StatusForbidden || !fetched || checks != 2 ||
				!archive.closed.Load() ||
				used ||
				strings.Contains(response.Body.String(), "private archive bytes") {
				t.Fatalf("response = %d %s; fetched = %t, checks = %d, closed = %t, used = %t",
					response.Code, response.Body, fetched, checks, archive.closed.Load(), used)
			}
		})
	}
}
