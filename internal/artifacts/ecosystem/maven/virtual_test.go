package maven

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func TestUnitPOMCleanupJDOMRawFixtures(t *testing.T) {
	for _, name := range []string{"raw", "profiles", "attrs"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile("testdata/pom-cleanup/" + name + ".xml")
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/pom-cleanup/" + name + ".expected.xml")
			if err != nil {
				t.Fatal(err)
			}
			got, changed := CleanPOM(body, "discard_active_reference")
			if !changed || !bytes.Equal(got, want) {
				t.Fatalf("changed %v\ngot  %q\nwant %q", changed, got, want)
			}
		})
	}
}

func TestUnitPOMCleanupPoliciesAndUnchanged(t *testing.T) {
	body := []byte(
		`<project><repositories/><profiles><profile><repositories/><pluginRepositories/></profile><profile><activation><activeByDefault>TRUE</activeByDefault></activation><repositories/></profile></profiles></project>`,
	)
	got, changed := CleanPOM(body, "discard_any_reference")
	if !changed || bytes.Contains(got, []byte("repositories")) || bytes.Contains(got, []byte("pluginRepositories")) {
		t.Fatalf("%v %s", changed, got)
	}
	for _, body := range []string{`<project><name>original</name></project>`, `<project><repositories></project>`, `<project><repositories/></project><other/>`, `text<project/>`, `<project><wrong:repositories/></project>`} {
		got, changed = CleanPOM([]byte(body), "discard_active_reference")
		if changed || string(got) != body {
			t.Fatalf("unchanged: %v %q", changed, got)
		}
	}
	got, changed = CleanPOM(body, "nothing")
	if changed || !bytes.Equal(got, body) {
		t.Fatal("nothing rewrote POM")
	}
	namespaced := `<p:project xmlns:p="urn:p"><p:repositories/><repositories/><p:profiles><p:profile><p:repositories/></p:profile></p:profiles></p:project>`
	got, changed = CleanPOM([]byte(namespaced), "discard_any_reference")
	if !changed || !strings.Contains(string(got), "<repositories />") || strings.Contains(string(got), "<p:repositories") {
		t.Fatal(string(got))
	}
}

func TestUnitPOMCleanupTypeAndPolicyGate(t *testing.T) {
	for _, kind := range []repo.PackageType{repo.Maven, repo.Gradle, repo.Ivy, repo.SBT, repo.Generic, repo.NPM, repo.P2} {
		v := repo.Config{Class: repo.Virtual, PackageType: kind}
		policy, ok := POMCleanupPolicy(v, "com/acme/a.pom")
		want := kind != repo.NPM && kind != repo.P2
		if ok != want || want && policy != "discard_active_reference" {
			t.Fatalf("%s %q %v", kind, policy, ok)
		}
		v.PomRepositoryReferencesCleanupPolicy = "nothing"
		_, ok = POMCleanupPolicy(v, "a.pom")
		if ok != (kind == repo.Generic) {
			t.Fatalf("%s nothing: %v", kind, ok)
		}
		if _, ok = POMCleanupPolicy(v, "a.jar"); ok {
			t.Fatal("JAR")
		}
		v.Class = repo.Local
		if _, ok = POMCleanupPolicy(v, "a.pom"); ok {
			t.Fatal("local")
		}
	}
}
