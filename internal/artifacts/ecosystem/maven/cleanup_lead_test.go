package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
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
