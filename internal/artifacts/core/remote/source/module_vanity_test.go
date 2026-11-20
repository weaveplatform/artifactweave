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

func TestUnitModuleVanityContractRejectsInvalidRequestsBeforeResolution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*source.Resource)
	}{
		{"artifact", func(r *source.Resource) { r.Kind = source.ResourceKindArtifact }},
		{"versions", func(r *source.Resource) { r.Kind = source.ResourceKindVersions }},
		{"head", func(r *source.Resource) { r.Method = http.MethodHead }},
		{"no-validation", func(r *source.Resource) { r.ValidateMetadata = nil }},
		{"no-version", func(r *source.Resource) { r.Variant = "" }},
		{"checksum", func(r *source.Resource) { r.Checksums.SHA256 = strings.Repeat("a", 64) }},
		{"wrong-mime", func(r *source.Resource) { r.ExpectedContentType = "text/html" }},
		{"plural-mime", func(r *source.Resource) { r.ExpectedContentTypes = []string{"application/json"} }},
		{"registry-projection", func(r *source.Resource) { r.ModuleDownloadLocation = true }},
		{"nuget-projection", func(r *source.Resource) { r.NuGetEmptyFeed = true }},
		{"smart", func(r *source.Resource) { r.RequireSmart = true }},
		{"no-destination", func(r *source.Resource) { r.ExternalURL = "" }},
		{"no-guard", func(r *source.Resource) { r.CheckURL = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request(t, "")
			q.Resource = source.Resource{
				Kind: source.ResourceKindMetadata, Path: "module/vanity", Method: http.MethodGet,
				ExternalURL: "https://up.example/module", CheckURL: func(context.Context, *url.URL) error { return nil },
				ModuleVanityDocument: true, ExpectedContentType: "application/json", Variant: "module-vanity-v1",
				ValidateMetadata: func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil },
			}
			tc.change(&q.Resource)
			s := source.Service{Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
				t.Fatal("invalid vanity contract reached resolution")
				return model.SourceIdentity{}, nil
			}}
			if _, err := s.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
				t.Fatal(err)
			}
		})
	}
}

func TestUnitModuleVanityIdentityCanonicalizesControlsWithoutDisclosingQuery(t *testing.T) {
	base, err := source.ModuleVanityIdentity("https://UP.example/module?token=private&part=1")
	if err != nil || len(base) != 64 || strings.Contains(base, "private") {
		t.Fatal("document identity was not an opaque digest", err)
	}
	for _, raw := range []string{
		"https://up.example/module?part=1&token=private&terraform-get=1",
		"https://up.example/module?archive=zip&checksum=sha256:ignored&token=private&part=1",
	} {
		got, err := source.ModuleVanityIdentity(raw)
		if err != nil || got != base {
			t.Fatal("equivalent document identity changed", err)
		}
	}
	other, err := source.ModuleVanityIdentity("https://up.example/module?token=private&part=2")
	if err != nil || other == base {
		t.Fatal("meaningful query was dropped", err)
	}
	for _, raw := range []string{"file:///module", "https://user:pass@up.example/module", "https://up.example/module#fragment", "https://up.example/module?bad=%zz"} {
		if _, err := source.ModuleVanityIdentity(raw); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatal("invalid document identity accepted", err)
		}
	}
}
