package ecosystem_test

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentpackages"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/ansible"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conan"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cran"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/golang"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/machinelearning"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/opkg"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/skills"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/swift"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/vagrant"
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

func TestUnitCleanupArchiveLeadsKeepFormatBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, version string
		resolver           format.CleanupLeadResolver
		single             bool
		leads, nonleads    []string
	}{
		{
			name: "agentplugins", pkg: "widget", version: "1.0.0", resolver: agentplugins.Handler{}, single: true,
			leads: []string{"widget/1.0.0/widget-1.0.0.zip"}, nonleads: []string{"widget/1.0.0/widget-1.0.0.zip.sha256", "marketplace.json", "widget/2.0.0/widget-2.0.0.zip"},
		},
		{
			name: "alpine", pkg: "widget", version: "1.0.0-r0", resolver: alpine.Handler{},
			leads:    []string{"v3.22/main/x86_64/widget-1.0.0-r0.apk", "v3.22/main/aarch64/widget-1.0.0-r0.apk"},
			nonleads: []string{"v3.22/main/x86_64/APKINDEX.tar.gz", "v3.22/main/x86_64/widget-1.0.0-r0.apk.asc", "arbitrary/widget.apk"},
		},
		{
			name: "ansible", pkg: "team.widget", version: "1.0.0", resolver: ansible.Handler{}, single: true,
			leads:    []string{"collections/team/widget/1.0.0/team-widget-1.0.0.tar.gz"},
			nonleads: []string{"collections/team/widget/1.0.0/MANIFEST.json", "collections/team/widget/2.0.0/team-widget-2.0.0.tar.gz", "collections/other/widget/1.0.0/other-widget-1.0.0.tar.gz"},
		},
		{
			name: "bower", pkg: "widget", version: "1.0.0", resolver: bower.Handler{}, single: true,
			leads: []string{"widget/1.0.0/bower.json"}, nonleads: []string{"widget/1.0.0/archive.zip", "other/1.0.0/bower.json", "widget/2.0.0/bower.json"},
		},
		{
			name: "chef", pkg: "widget", version: "1.0.0", resolver: chef.Handler{}, single: true,
			leads: []string{"widget/widget-1.0.0.tar.gz"}, nonleads: []string{"widget/metadata.json", "widget/widget-2.0.0.tar.gz", "other/other-1.0.0.tar.gz"},
		},
		{
			name: "cocoapods", pkg: "Widget", version: "1.0.0", resolver: cocoapods.Handler{}, single: true,
			leads: []string{"Widget/1.0.0/Widget-1.0.0.tar.gz"}, nonleads: []string{"Widget/1.0.0/Widget.podspec.json", "Widget/2.0.0/Widget-2.0.0.tar.gz", "Other/1.0.0/Other-1.0.0.tar.gz"},
		},
		{
			name: "conda", pkg: "widget", version: "1.0.0", resolver: conda.Handler{},
			leads:    []string{"linux-64/widget-1.0.0-h123_0.tar.bz2", "noarch/widget-1.0.0-py_0.conda"},
			nonleads: []string{"linux-64/repodata.json", "linux-64/widget-1.0.0-h123_0.tar.bz2.sha256", "outside/widget.conda"},
		},
		{
			name: "cran", pkg: "widget", version: "1.0.0", resolver: cran.Handler{},
			leads:    []string{"src/contrib/widget_1.0.0.tar.gz", "bin/windows/contrib/4.5/widget_1.0.0.zip"},
			nonleads: []string{"src/contrib/PACKAGES.gz", "src/contrib/widget_1.0.0.tar.gz.asc", "outside/widget_1.0.0.tar.gz"},
		},
		{
			name: "gems", pkg: "widget", version: "1.0.0", resolver: gems.Handler{},
			leads:    []string{"gems/widget-1.0.0.gem", "gems/widget-1.0.0-java.gem"},
			nonleads: []string{"specs.4.8.gz", "gems/widget-1.0.0.gem.asc", "outside/widget-1.0.0.gem"},
		},
		{
			name: "hex", pkg: "widget", version: "1.0.0", resolver: hex.Handler{}, single: true,
			leads: []string{"tarballs/widget-1.0.0.tar"}, nonleads: []string{"packages/widget", "tarballs/widget-1.0.0.tar.sha256", "tarballs/other-1.0.0.tar", "tarballs/widget-2.0.0.tar"},
		},
		{
			name: "puppet", pkg: "team-widget", version: "1.0.0", resolver: puppet.Handler{}, single: true,
			leads: []string{"team-widget/team-widget-1.0.0.tar.gz"}, nonleads: []string{"team-widget/metadata.json", "team-widget/team-widget-2.0.0.tar.gz", "other-widget/other-widget-1.0.0.tar.gz"},
		},
		{
			name: "pypi", pkg: "widget", version: "1.0.0", resolver: pypi.Handler{},
			leads:    []string{"dist/widget-1.0.0.tar.gz", "dist/widget-1.0.0-py3-none-any.whl"},
			nonleads: []string{"simple/widget/index.html", "dist/widget-1.0.0-py3-none-any.whl.metadata", "dist/widget-1.0.0.tar.gz.asc"},
		},
		{
			name: "rpm", pkg: "widget", version: "1.0.0-1", resolver: rpm.Handler{},
			leads:    []string{"Packages/widget-1.0.0-1.x86_64.rpm", "Packages/widget-1.0.0-1.aarch64.rpm"},
			nonleads: []string{"repodata/repomd.xml", "Packages/widget-1.0.0-1.x86_64.rpm.asc", "../widget.rpm"},
		},
		{
			name: "swift", pkg: "team.widget", version: "1.0.0", resolver: swift.Handler{}, single: true,
			leads: []string{"team/widget/widget-1.0.0.zip"}, nonleads: []string{"team/widget/Package.swift", "team/widget/widget-2.0.0.zip", "other/widget/widget-1.0.0.zip"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectLead, err := tc.resolver.CleanupLead(model.PackageVersion{})
			if err != nil {
				t.Fatal(err)
			}
			version := api.IndexVersion{Package: api.IndexPackage{Name: tc.pkg}, Version: tc.version}
			for _, p := range tc.leads {
				lead := selectLead(api.IndexFile{Version: version, Path: p})
				if !lead.Found || lead.Single != tc.single {
					t.Fatalf("indexed archive %q was not its format's lead: %+v", p, lead)
				}
			}
			for _, p := range tc.nonleads {
				if selectLead(api.IndexFile{Version: version, Path: p}).Found {
					t.Fatal("generated metadata, signature or foreign identity substituted for a lead", p)
				}
			}
		})
	}
}

func TestUnitCleanupManifestLeadsKeepIndexedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, version string
		resolver           format.CleanupLeadResolver
		single             bool
		leads, nonleads    []string
	}{
		{
			name: "agentpackages", pkg: "team/widget", version: "1.0.0", resolver: agentpackages.Handler{}, single: true,
			leads: []string{"team/widget/widget-1.0.0.zip"}, nonleads: []string{"team/widget/widget-1.0.0.zip.sha256", "other/widget/widget-1.0.0.zip", "team/widget/widget-2.0.0.zip"},
		},
		{
			name: "conan", pkg: "widget@team/stable", version: "1.0.0", resolver: conan.Handler{},
			leads:    []string{"team/widget/1.0.0/stable/r1/export/conanfile.py", "team/widget/1.0.0/stable/r2/export/conanfile.py"},
			nonleads: []string{"team/widget/1.0.0/stable/r1/export/conanmanifest.txt", "team/widget/1.0.0/stable/r1/package/pid/prev/conanfile.py", "team/widget/2.0.0/stable/r1/export/conanfile.py"},
		},
		{
			name: "huggingface model", pkg: "models/acme/widget", version: "0000000000000000000000000000000000000000", resolver: huggingface.Handler{}, single: true,
			leads:    []string{"models--acme--widget/snapshots/0000000000000000000000000000000000000000/.jfrog_huggingface_model_info.json"},
			nonleads: []string{"models--acme--widget/snapshots/0000000000000000000000000000000000000000/config.json", "models--acme--widget/snapshots/0000000000000000000000000000000000000000/.jfrog_huggingface_dataset_info.json", "models--acme--widget/refs/main"},
		},
		{
			name: "huggingface dataset", pkg: "datasets/acme/widget", version: "0000000000000000000000000000000000000000", resolver: huggingface.Handler{}, single: true,
			leads:    []string{"datasets--acme--widget/snapshots/0000000000000000000000000000000000000000/.jfrog_huggingface_dataset_info.json", "datasets--acme--widget/snapshots/0000000000000000000000000000000000000000/.jfrog_huggingface_dataset_info"},
			nonleads: []string{"datasets--acme--widget/snapshots/0000000000000000000000000000000000000000/data.json", "datasets--acme--widget/snapshots/0000000000000000000000000000000000000000/.jfrog_huggingface_model_info.json"},
		},
		{
			name: "luarocks", pkg: "widget", version: "1.0-1", resolver: luarocks.Handler{},
			leads: []string{"widget-1.0-1.rockspec", "widget-1.0-1.src.rock"}, nonleads: []string{"manifest", "widget-1.0-1.src.rock.asc", "other-1.0-1.rockspec"},
		},
		{
			name: "machinelearning", pkg: "team/widget", version: "1.0", resolver: machinelearning.Handler{}, single: true,
			leads: []string{"models/team/widget/1.0/model-manifest.json"}, nonleads: []string{"models/team/widget/1.0/files/model.bin", "models/team/widget/tmp/1.0/1700000000000/model-manifest.json", "models/other/widget/1.0/model-manifest.json"},
		},
		{
			name: "nix", pkg: "widget-1.0", version: "00000000000000000000000000000000", resolver: nix.Handler{}, single: true,
			leads:    []string{"binary-cache/00000000000000000000000000000000/00000000000000000000000000000000.narinfo"},
			nonleads: []string{"binary-cache/00000000000000000000000000000000/widget.nar.xz", "channels/stable/nixos/release/nixexprs.tar.xz", "binary-cache/11111111111111111111111111111111/11111111111111111111111111111111.narinfo"},
		},
		{
			name: "opkg", pkg: "widget", version: "1.0", resolver: opkg.Handler{},
			leads: []string{"feed/widget_1.0_aarch64.ipk", "feed/widget_1.0_x86_64.ipk"}, nonleads: []string{"feed/Packages.gz", "feed/widget_1.0_aarch64.ipk.asc", "../widget.ipk"},
		},
		{
			name: "pub", pkg: "widget", version: "1.0.0", resolver: pub.Handler{}, single: true,
			leads: []string{"widget/widget-1.0.0.tar.gz"}, nonleads: []string{"widget/pubspec.yaml", "widget/widget-2.0.0.tar.gz", "other/other-1.0.0.tar.gz"},
		},
		{
			name: "skills", pkg: "widget", version: "1.0.0", resolver: skills.Handler{}, single: true,
			leads: []string{"widget/1.0.0/widget-1.0.0.zip"}, nonleads: []string{"widget/1.0.0/SKILL.md", "widget/2.0.0/widget-2.0.0.zip", "other/1.0.0/other-1.0.0.zip"},
		},
		{
			name: "terraform module", pkg: "team/widget/aws", version: "1.0.0", resolver: terraform.Handler{}, single: true,
			leads: []string{"team/widget/aws/1.0.0.zip", "team/widget/aws/v1.0.0.zip"}, nonleads: []string{"team/widget/aws/1.0.0.zip.sha256", "team/other/aws/1.0.0.zip", "team/widget/aws/2.0.0.zip"},
		},
		{
			name: "terraform provider", pkg: "hashicorp/aws", version: "1.0.0", resolver: terraform.Handler{},
			leads:    []string{"hashicorp/aws/1.0.0/terraform-provider-aws_1.0.0_linux_amd64.zip", "hashicorp/aws/1.0.0/terraform-provider-aws_1.0.0_darwin_arm64.zip"},
			nonleads: []string{"hashicorp/aws/1.0.0/terraform-provider-aws_1.0.0_SHA256SUMS", "hashicorp/aws/1.0.0/terraform-provider-aws_1.0.0_SHA256SUMS.sig", "hashicorp/aws/2.0.0/terraform-provider-aws_2.0.0_linux_amd64.zip"},
		},
		{
			name: "terraformbackend", pkg: "demo", version: "1700000000000", resolver: terraformbackend.Handler{}, single: true,
			leads:    []string{"workspaces/demo/state.latest.json", "workspaces/demo/state.1700000000000.json"},
			nonleads: []string{"workspaces/demo/workspace.json", "workspaces/demo/state.1700000000001.json", "workspaces/other/state.latest.json"},
		},
		{
			name: "vagrant", pkg: "team/widget", version: "1.0.0", resolver: vagrant.Handler{},
			leads: []string{"team/widget/1.0.0/virtualbox.box", "team/widget/1.0.0/vmware.box"}, nonleads: []string{"team/widget/metadata.json", "team/widget/1.0.0/virtualbox.box.sha256"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectLead, err := tc.resolver.CleanupLead(model.PackageVersion{})
			if err != nil {
				t.Fatal(err)
			}
			version := api.IndexVersion{Package: api.IndexPackage{Name: tc.pkg}, Version: tc.version}
			var first api.IndexLead
			for i, p := range tc.leads {
				lead := selectLead(api.IndexFile{Version: version, Path: p, Created: time.UnixMilli(1700000000000)})
				if !lead.Found || lead.Single != tc.single {
					t.Fatalf("canonical indexed lead %q lost: %+v", p, lead)
				}
				if i == 0 {
					first = lead
				} else if tc.single && lead.Rank >= first.Rank {
					t.Fatal("indexed canonical preference was reversed", lead, first)
				}
			}
			for _, p := range tc.nonleads {
				if selectLead(api.IndexFile{Version: version, Path: p, Created: time.UnixMilli(1700000000000)}).Found {
					t.Fatal("nonlead or unrelated indexed identity was admitted", p)
				}
			}
		})
	}
}
