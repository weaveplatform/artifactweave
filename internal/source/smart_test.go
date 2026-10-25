package source_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestUnitSmartMetadataContractAndAuthorization(t *testing.T) {
	q := request(t, "")
	q.Resource.Kind = source.Metadata
	q.Resource.RequireSmart = true
	q.Resource.ExternalURL = "https://up.example/api/storage/repo/file"
	q.Resource.Variant = "smart-metadata-v1"
	q.Resource.ValidateMetadata = func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil }
	calls := 0
	s := source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			calls++
			return stream(io.NopCloser(strings.NewReader("{}"))), nil
		}),
	}
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
		if _, err = s.Read(
			t.Context(),
			bad,
		); !errors.Is(err, source.ErrInvalidRequest) ||
			calls != 1 {
			t.Fatal("invalid contract reached network", err, calls)
		}
	}
	q.Repo.IncludesPattern = new("other/**")
	if _, err = s.Read(t.Context(), q); err == nil || calls != 1 {
		t.Fatal("unauthorized path reached discovery", err, calls)
	}
}

func TestUnitSmartAQLContractRejectsActionsBeforeNetwork(t *testing.T) {
	q := request(t, "")
	q.Resource = source.Resource{
		Kind:                source.Metadata,
		Path:                "query",
		PermissionFolder:    true,
		Method:              "GET",
		RequireSmart:        true,
		SmartAQL:            `items.find({"repo":"peer"}).limit(10)`,
		ExternalURL:         "https://up.example/api/search/aql",
		Variant:             "smart-aql-v1",
		ExpectedContentType: "application/json",
		ValidateMetadata:    func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
	}
	calls := 0
	s := source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			calls++
			return stream(io.NopCloser(strings.NewReader("{}"))), nil
		}),
	}
	result, err := s.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	for _, change := range []func(*source.Resource){
		func(r *source.Resource) { r.SmartAQL = `items.delete({"repo":"peer"}).limit(10)` },
		func(r *source.Resource) { r.SmartAQL = `items.find({"repo":"peer"}).limit(10).transitive()` },
		func(r *source.Resource) { r.SmartAQL = `items.find({"repo":"peer"})` },
		func(r *source.Resource) { r.SmartAQL = `items.find({"repo":"peer"}).limit(10002)` },
		func(r *source.Resource) { r.SmartAQL = `items.find({}).limit(10)` },
		func(r *source.Resource) { r.RequireSmart = false },
		func(r *source.Resource) { r.Method = "HEAD" },
		func(r *source.Resource) { r.PermissionFolder = false },
		func(r *source.Resource) { r.PermissionPath = "nested" },
		func(r *source.Resource) { r.ExpectedContentType = "text/plain" },
		func(r *source.Resource) { r.ModuleDownloadLocation = true },
		func(r *source.Resource) { r.ExpectedContentTypes = []string{"application/json"} },
	} {
		bad := q
		change(&bad.Resource)
		if _, err = s.Read(
			t.Context(),
			bad,
		); !errors.Is(err, source.ErrInvalidRequest) ||
			calls != 1 {
			t.Fatalf("invalid request reached backend: %+v %v calls=%d", bad.Resource, err, calls)
		}
	}
	q.Repo.ExcludesPattern = new("**")
	result, err = s.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	if err = result.CheckRead(t.Context(), "blocked.txt"); err == nil {
		t.Fatal("ordinary artifact check escaped patterns")
	}
	q.Subject.Admin = true
	q.Repo.BlackedOut = new(true)
	if _, err = s.Read(t.Context(), q); err == nil {
		t.Fatal("admin AQL escaped repository blackout")
	}
}
