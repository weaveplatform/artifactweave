package source_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestSmartMetadataContractAndAuthorization(t *testing.T) {
	q := request(t, "")
	q.Resource.Kind = source.Metadata
	q.Resource.RequireSmart = true
	q.Resource.ExternalURL = "https://up.example/api/storage/repo/file"
	q.Resource.Variant = "smart-metadata-v1"
	q.Resource.ValidateMetadata = func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil }
	calls := 0
	s := source.Service{Resolve: identity, HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		calls++
		return stream(io.NopCloser(strings.NewReader("{}"))), nil
	})}
	result, err := s.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	for _, change := range []func(*source.Resource){
		func(r *source.Resource) { r.Kind = source.Artifact },
		func(r *source.Resource) { r.ExternalURL = "" },
		func(r *source.Resource) { r.Variant = "" },
		func(r *source.Resource) { r.ValidateMetadata = nil },
	} {
		bad := q
		change(&bad.Resource)
		if _, err = s.Read(t.Context(), bad); !errors.Is(err, source.ErrInvalidRequest) || calls != 1 {
			t.Fatal("invalid contract reached network", err, calls)
		}
	}
	q.Repo.IncludesPattern = new("other/**")
	if _, err = s.Read(t.Context(), q); err == nil || calls != 1 {
		t.Fatal("unauthorized path reached discovery", err, calls)
	}
}
