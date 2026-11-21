package source_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func pullStorageRequest(t *testing.T) source.Request {
	t.Helper()
	q := request(t, "")
	q.Resource = source.Resource{
		Kind: source.ResourceKindMetadata, Method: http.MethodGet, Path: ".replication-storage",
		PermissionFolder: true, PullStorage: true, NoCacheFile: true, Transient: true,
		Variant: "replication-storage-v1", ExternalURL: "https://peer.example/artifactory/api/storage/source",
		CheckURL:         func(context.Context, *url.URL) error { return nil },
		ValidateMetadata: func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
	}
	return q
}

func TestUnitPullStorageAdmitsOnlyClosedTransientReadOperations(t *testing.T) {
	for _, test := range []struct {
		name, query string
	}{
		{"info", ""}, {"properties", "?properties"}, {"files", "?list&deep=1&listFolders=0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := pullStorageRequest(t)
			q.Resource.ExternalURL += test.query
			calls := 0
			s := source.Service{Resolve: identity, HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
				calls++
				return stream(io.NopCloser(strings.NewReader(`{}`))), nil
			})}
			out, err := s.Read(t.Context(), q)
			if err != nil || out.Stream == nil || calls != 1 {
				t.Fatalf("closed storage read %+v %v calls=%d", out, err, calls)
			}
			if err = out.Stream.Body.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, test := range []struct {
		name string
		edit func(*source.Resource)
	}{
		{"unmarked-transient", func(r *source.Resource) { r.PullStorage = false }},
		{"post", func(r *source.Resource) { r.Method = http.MethodPost }},
		{"artifact", func(r *source.Resource) { r.Kind = source.ResourceKindArtifact }},
		{"persistent", func(r *source.Resource) { r.Transient = false }},
		{"cache-file", func(r *source.Resource) { r.NoCacheFile = false }},
		{"variant", func(r *source.Resource) { r.Variant = "other" }},
		{"unvalidated", func(r *source.Resource) { r.ValidateMetadata = nil }},
		{"unguarded", func(r *source.Resource) { r.CheckURL = nil }},
		{"smart", func(r *source.Resource) { r.RequireSmart = true }},
		{"partial", func(r *source.Resource) { r.Range = "bytes=0-1" }},
		{"client-conditional", func(r *source.Resource) { r.IfNoneMatch = "client" }},
		{"body", func(r *source.Resource) { r.PostBody = []byte("data") }},
		{"other-operation", func(r *source.Resource) { r.ExternalURL += "?stats" }},
		{"mixed-modes", func(r *source.Resource) { r.ExternalURL += "?list&deep=1&listFolders=0&properties" }},
		{"duplicate-query", func(r *source.Resource) { r.ExternalURL += "?properties&properties" }},
		{"invalid-query", func(r *source.Resource) { r.ExternalURL += "?properties=%xx" }},
		{"other-api", func(r *source.Resource) { r.ExternalURL = "https://peer.example/artifactory/api/security/users" }},
		{"embedded-auth", func(r *source.Resource) {
			r.ExternalURL = "https://user:password@peer.example/artifactory/api/storage/source"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := pullStorageRequest(t)
			test.edit(&q.Resource)
			calls := 0
			s := source.Service{Resolve: identity, HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
				calls++
				return stream(io.NopCloser(strings.NewReader(`{}`))), nil
			})}
			if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) || calls != 0 {
				t.Fatalf("invalid storage operation reached transport: %v calls=%d", err, calls)
			}
		})
	}
}
