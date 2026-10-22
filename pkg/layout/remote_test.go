package layout_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

func TestRemotePathUsesUniqueCanonicalProjection(t *testing.T) {
	gradle, maven := bundled(t, "gradle-default"), bundled(t, "maven-2-default")
	for _, c := range []struct{ canonical, upstream string }{
		{"org.acme/lib/ivy-1.0.xml", "org/acme/lib/1.0/lib-1.0.pom"},
		{"org.acme/lib/1.0/lib-1.0.jar", "org/acme/lib/1.0/lib-1.0.jar"},
		{"org.acme/lib/1.0/lib-1.0-sources.jar", "org/acme/lib/1.0/lib-1.0-sources.jar"},
	} {
		got, err := layout.RemotePath(gradle, maven, c.canonical)
		if err != nil || got != c.upstream {
			t.Fatal(c, got, err)
		}
	}
	for _, p := range []string{
		"org.acme/lib/1.0/lib-1.0.pom",
		"org.acme/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-1.jar",
	} {
		if _, err := layout.RemotePath(gradle, maven, p); err == nil {
			t.Fatal("alias or incompatible integration grammar admitted", p, err)
		}
	}
	if _, err := layout.RemotePath(gradle, maven, "notes.txt"); !errors.Is(err, layout.ErrNoMatch) {
		t.Fatal(err)
	}
	if _, err := layout.Translate(
		gradle,
		maven,
		"org.acme/lib/ivy-1.0.xml",
	); !errors.Is(
		err,
		layout.ErrLossy,
	) {
		t.Fatal("copy/move collision protection changed", err)
	}
}

func TestRemotePathRoundTripsLosslessCorpus(t *testing.T) {
	for fromName, paths := range translations {
		upstream := bundled(t, fromName)
		for _, definition := range layout.Bundled() {
			canonical := bundled(t, definition.Name)
			seen := map[string]string{}
			for _, p := range paths {
				projected, err := layout.Translate(upstream, canonical, p)
				if err != nil {
					continue
				}
				got, err := layout.RemotePath(canonical, upstream, projected)
				if err != nil || got != p {
					t.Fatalf(
						"%s -> %s: %s -> %s -> %s: %v",
						fromName,
						definition.Name,
						p,
						projected,
						got,
						err,
					)
				}
				if other, ok := seen[projected]; ok && other != got {
					t.Fatal("two upstream identities share canonical authority", other, got)
				}
				seen[projected] = got
			}
		}
	}
}
