package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
)

func TestUnitCleanupLeadUsesSnapshotBuildIdentity(t *testing.T) {
	selectLead, err := (maven.Handler{}).CleanupLead(model.PackageVersion{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		found bool
		rank  int64
	}{
		{"org/example/pkg/1.0-SNAPSHOT/pkg-1.0-20261120.120000-9.pom", true, 9},
		{"org/example/pkg/1.0-SNAPSHOT/pkg-1.0-20261120.120000-10.pom", true, 10},
		{"org/example/pkg/1.0-SNAPSHOT/pkg-1.0-SNAPSHOT.pom", true, -1},
		{"org/example/pkg/1.0-SNAPSHOT/pkg-1.0-20261120.120000-11.jar", false, 0},
		{"org/example/pkg/1.0-SNAPSHOT/pkg-1.0-20261120.120000-12.pom.sha256", false, 0},
	} {
		file := api.IndexFile{Path: tc.path}
		// Identical POM content in two builds must still have distinct priority.
		file.Actual.SHA256 = "same-content-across-builds"
		lead := selectLead(file)
		if lead.Found != tc.found || lead.Found && (!lead.Single || lead.Rank != tc.rank || lead.TieBreak != tc.path) {
			t.Fatalf("lead classification for %s = %+v", tc.path, lead)
		}
	}
}

func TestUnitCleanupLeadUsesConfiguredDescriptorPattern(t *testing.T) {
	for _, tc := range []struct{ layout, descriptor, other string }{
		{"sbt-default", "org.example/widget/scala_2.13/sbt_1.0/1.0/ivys/ivy.xml", "org.example/widget/scala_2.13/sbt_1.0/1.0/jars/widget.xml"},
		{"ivy-default", "org.example/widget/1.0/ivys/ivy-1.0.xml", "org.example/widget/1.0/jars/widget-1.0.xml"},
		{"maven-2-default", "org/example/widget/1.0/widget-1.0.pom", "org/example/widget/1.0/widget-1.0.xml"},
	} {
		t.Run(tc.layout, func(t *testing.T) {
			l, err := layout.Lookup(tc.layout)
			if err != nil {
				t.Fatal(err)
			}
			selectLead, err := (maven.Handler{}).CleanupLeadWithLayout(l, model.PackageVersion{})
			if err != nil || !selectLead(api.IndexFile{Path: tc.descriptor}).Found || selectLead(api.IndexFile{Path: tc.other}).Found {
				t.Fatal("descriptor selection ignored the actual layout", err)
			}
		})
	}
	custom, err := layout.Compile(layout.Definition{
		Name: "custom", ArtifactPathPattern: "[orgPath]/[module]/[baseRev]/[module].[ext]",
		DistinctiveDescriptorPathPattern: true, DescriptorPathPattern: "[orgPath]/[module]/[baseRev]/descriptor.xml",
	})
	if err != nil {
		t.Fatal(err)
	}
	selectLead, err := (maven.Handler{}).CleanupLeadWithLayout(custom, model.PackageVersion{})
	if err != nil || !selectLead(api.IndexFile{Path: "org/example/widget/1.0/descriptor.xml"}).Found || selectLead(api.IndexFile{Path: "org/example/widget/1.0/widget.pom"}).Found {
		t.Fatal("custom descriptor was replaced by a filename fallback", err)
	}
	selectLead, err = (maven.Handler{}).CleanupLeadWithLayout(nil, model.PackageVersion{})
	if err != nil || selectLead(api.IndexFile{Path: "org/example/widget/1.0/widget.pom"}).Found {
		t.Fatal("an unresolved layout invented a descriptor", err)
	}
}
