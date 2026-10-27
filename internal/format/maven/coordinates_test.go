package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func mustLayout(t *testing.T, name string) *layout.Layout {
	t.Helper()
	l, err := layout.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestUnitIsMavenLayout(t *testing.T) {
	m2 := mustLayout(t, repo.LayoutMaven2).Definition()
	compile := func(d layout.Definition) *layout.Layout {
		l, err := layout.Compile(d)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	renamed, edited, padded := m2, m2, m2
	renamed.Name = "my-maven"
	edited.FileIntegrationRevisionRegExp = "SNAPSHOT"
	padded.ArtifactPathPattern = " " + m2.ArtifactPathPattern + " "
	for _, tc := range []struct {
		what     string
		l        *layout.Layout
		expected bool
	}{
		{"maven-2-default", compile(m2), true},
		{"the same patterns under another name", compile(renamed), false},
		{"maven-2-default edited", compile(edited), false},
		{"maven-2-default with padded patterns", compile(padded), true},
		{"gradle-default", mustLayout(t, repo.LayoutGradle), false},
		{"no layout", nil, false},
	} {
		if got := maven.IsMavenLayout(tc.l); got != tc.expected {
			t.Errorf("%s: %v", tc.what, got)
		}
	}
}

// Real publication paths of each client, read through their layouts: the
// package is groupId:artifactId and the version is the folder a version's
// files share, so a unique build is a file of its snapshot version.
func TestUnitRead(t *testing.T) {
	for _, tc := range []struct {
		layout, path, pkg, version string
	}{
		{repo.LayoutMaven2, "com/acme/app/1.0/app-1.0.jar", "com.acme:app", "1.0"},
		{repo.LayoutMaven2, "com/acme/app/1.0/app-1.0.pom", "com.acme:app", "1.0"},
		{repo.LayoutMaven2, "com/acme/app/1.0/app-1.0-sources.jar", "com.acme:app", "1.0"},
		{repo.LayoutMaven2, "com/acme/app/1.0/app-1.0.tar.gz", "com.acme:app", "1.0"},
		{
			repo.LayoutMaven2, "com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar",
			"com.acme:app", "1.0-SNAPSHOT",
		},
		{
			repo.LayoutMaven2, "com/acme/app/1.0-SNAPSHOT/app-1.0-20261017.090000-1-sources.jar",
			"com.acme:app", "1.0-SNAPSHOT",
		},
		{
			repo.LayoutMaven2, "com/acme/app/1.0-beta-SNAPSHOT/app-1.0-beta-20261017.090000-1.pom",
			"com.acme:app", "1.0-beta-SNAPSHOT",
		},
		// An integration revision in a release folder's file names the folder.
		{repo.LayoutMaven2, "com/acme/app/1.0/app-1.0-SNAPSHOT.jar", "com.acme:app", "1.0"},
		{repo.LayoutGradle, "com.acme/app/1.0/app-1.0.jar", "com.acme:app", "1.0"},
		{repo.LayoutGradle, "com.acme/app/ivy-1.0.xml", "com.acme:app", "1.0"},
		{
			repo.LayoutGradle, "com.acme/app/2.0-20261017090000/app-2.0-20261017090000.jar",
			"com.acme:app", "2.0-20261017090000",
		},
		// Gradle's descriptor sits outside the version folder, so its own
		// revision completes the version its artifacts' folder names.
		{
			repo.LayoutGradle, "com.acme/app/ivy-2.0-20261017090000.xml",
			"com.acme:app", "2.0-20261017090000",
		},
		{repo.LayoutIvy, "com.acme/app/1.0/jars/app-1.0.jar", "com.acme:app", "1.0"},
		{repo.LayoutIvy, "com.acme/app/1.0/ivys/ivy-1.0.xml", "com.acme:app", "1.0"},
		{
			repo.LayoutIvy, "com.acme/app/1.0-20261017090000/jars/app-1.0-20261017090000.jar",
			"com.acme:app", "1.0-20261017090000",
		},
		{repo.LayoutSBT, "com.acme/plug/scala_2.12/sbt_1.0/1.0/jars/plug.jar", "com.acme:plug", "1.0"},
		{repo.LayoutSBT, "com.acme/plug/scala_2.12/sbt_1.0/1.0/ivys/ivy.xml", "com.acme:plug", "1.0"},
		{repo.LayoutMaven1, "commons-lang/jars/commons-lang-2.1.jar", "commons-lang:commons-lang", "2.1"},
		{repo.LayoutMaven1, "commons-lang/poms/commons-lang-2.1.pom", "commons-lang:commons-lang", "2.1"},
	} {
		got, ok := maven.Read(mustLayout(t, tc.layout), tc.path)
		if !ok || got.Package != tc.pkg || got.Version != tc.version {
			t.Errorf("%s %s: %v %q %q", tc.layout, tc.path, ok, got.Package, got.Version)
		}
	}
}

// Metadata, checksums, index files and paths the layout does not read
// belong to no version.
func TestUnitReadNoVersion(t *testing.T) {
	m2 := mustLayout(t, repo.LayoutMaven2)
	for _, p := range []string{
		"com/acme/app/maven-metadata.xml",
		"com/acme/app/1.0-SNAPSHOT/maven-metadata.xml",
		"com/acme/app/1.0/app-1.0.jar.sha1",
		".index/nexus-maven-repository-index.gz",
		"app-1.0.jar",
		"com/acme/app/1.0/other-1.0.jar",
	} {
		if got, ok := maven.Read(m2, p); ok {
			t.Errorf("%s: %+v", p, got)
		}
	}
	// A layout with no organisation names no groupId.
	noOrg, err := layout.Compile(layout.Definition{
		Name: "flat", ArtifactPathPattern: "[module]/[baseRev]/[module]-[baseRev].[ext]",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := maven.Read(noOrg, "app/1.0/app-1.0.jar"); ok {
		t.Errorf("no organisation: %+v", got)
	}
}
