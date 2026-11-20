package ecosystem_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/golang"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
)

func TestUnitCleanupLeadUsesOwnedFormatIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, version, metadata, lead string
		resolver                           format.CleanupLeadResolver
		others                             []string
	}{
		{"docker", "team/image", "latest", "", "team/image/latest/manifest.json", docker.Handler{}, []string{"team/image/latest/list.manifest.json", "team/image/latest/sha256__layer", "team/image/other/manifest.json"}},
		{"go", "example.org/Module", "v1.2.0", "", "example.org/Module/@v/v1.2.0.mod", golang.Handler{}, []string{"example.org/Module/@v/v1.2.0.info", "example.org/Module/@v/v1.2.0.zip", "example.org/Other/@v/v1.2.0.mod", "example.org/Module/@v/v1.3.0.mod"}},
		{"helm", "widget", "1.0.0", `{"path":"second/widget.tgz"}`, "second/widget.tgz", helm.Handler{}, []string{"first/widget.tgz", "second/widget.tgz.prov", "index.yaml"}},
		{"nuget", "widget", "1.0.0", `{"path":"widget.1.0.0.nupkg"}`, "widget.1.0.0.nupkg", nuget.Handler{}, []string{"widget.1.0.0.snupkg", "widget.1.0.0.nuspec", "other.nupkg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectLead, err := tc.resolver.CleanupLead(model.PackageVersion{Version: tc.version, Metadata: []byte(tc.metadata)})
			if err != nil {
				t.Fatal(err)
			}
			v := api.IndexVersion{Package: api.IndexPackage{Name: tc.pkg}, Version: tc.version}
			lead := selectLead(api.IndexFile{Version: v, Path: tc.lead})
			if !lead.Found || !lead.Single {
				t.Fatal("canonical lead lost", lead)
			}
			for _, p := range tc.others {
				other := selectLead(api.IndexFile{Version: v, Path: p})
				if tc.name == "docker" && p == "team/image/latest/list.manifest.json" {
					if !other.Found || other.Rank >= lead.Rank {
						t.Fatal("manifest list overrode FolderManifest preference", other, lead)
					}
				} else if other.Found {
					t.Fatal("unrelated file substituted for the canonical lead", p)
				}
			}
		})
	}
	for _, resolver := range []format.CleanupLeadResolver{helm.Handler{}, nuget.Handler{}} {
		if selectLead, err := resolver.CleanupLead(model.PackageVersion{Metadata: []byte("bad metadata")}); err == nil || selectLead != nil {
			t.Fatal("malformed opaque metadata was guessed", err)
		}
		selectLead, err := resolver.CleanupLead(model.PackageVersion{Metadata: []byte(`{}`)})
		if err != nil || selectLead(api.IndexFile{Path: "symbols.snupkg"}).Found {
			t.Fatal("missing indexed archive acquired a fallback lead", err)
		}
	}
}
