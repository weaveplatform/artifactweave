package source_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestUnitModuleChecksumContractRejectsInvalidRequestsBeforeResolution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*source.Resource)
	}{
		{"artifact", func(r *source.Resource) { r.Kind = source.ResourceKindArtifact }},
		{"head", func(r *source.Resource) { r.Method = http.MethodHead }},
		{"no-validation", func(r *source.Resource) { r.ValidateMetadata = nil }},
		{"no-variant", func(r *source.Resource) { r.Variant = "" }},
		{"checksum-on-metadata", func(r *source.Resource) { r.Checksums.SHA256 = strings.Repeat("a", 64) }},
		{"wrong-mime", func(r *source.Resource) { r.ExpectedContentType = "text/plain" }},
		{"registry-projection", func(r *source.Resource) { r.ModuleDownloadLocation = true }},
		{"vanity-projection", func(r *source.Resource) { r.ModuleVanityDocument = true }},
		{"nuget-projection", func(r *source.Resource) { r.NuGetEmptyFeed = true }},
		{"smart", func(r *source.Resource) { r.RequireSmart = true }},
		{"no-destination", func(r *source.Resource) { r.ExternalURL = "" }},
		{"no-guard", func(r *source.Resource) { r.CheckURL = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request(t, "")
			q.Resource = source.Resource{
				Kind:                   source.ResourceKindMetadata,
				Path:                   "module/checksum",
				Method:                 http.MethodGet,
				ExternalURL:            "https://up.example/SUMS",
				CheckURL:               func(context.Context, *url.URL) error { return nil },
				ModuleChecksumDocument: true,
				ExpectedContentType:    "application/json",
				Variant:                "module-checksum-v1",
				ValidateMetadata:       func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
			}
			tc.change(&q.Resource)
			s := source.Service{Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
				t.Fatal("invalid checksum contract reached resolution")
				return model.SourceIdentity{}, nil
			}}
			if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
				t.Fatal(err)
			}
		})
	}
}

func TestUnitModuleChecksumRejectionRechecksCurrentCallerBeforeMutation(t *testing.T) {
	q := request(t, "")
	q.Resource = source.Resource{
		Kind:                   source.ResourceKindMetadata,
		Path:                   "module/checksum",
		Method:                 http.MethodGet,
		ExternalURL:            "https://up.example/SUMS",
		CheckURL:               func(context.Context, *url.URL) error { return nil },
		ModuleChecksumDocument: true,
		ExpectedContentType:    "application/json",
		Variant:                "checksum-rejection-v1",
		ValidateMetadata:       func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
	}
	allowed := true
	rejections := 0
	s := source.Service{Resolve: identity, Revalidate: func(context.Context, source.Request, source.Permit) error {
		if !allowed {
			return source.ErrDenied
		}
		return nil
	}, HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
		result := stream(io.NopCloser(strings.NewReader("{}")))
		result.RejectMetadata = func(context.Context) error { rejections++; return nil }
		return result, nil
	})}
	result, err := s.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	allowed = false
	if err := result.RejectMetadata(t.Context()); !errors.Is(err, source.ErrDenied) || rejections != 0 {
		t.Fatal("revoked caller mutated checksum metadata", err, rejections)
	}
}
