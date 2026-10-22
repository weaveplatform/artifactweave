package layout_test

import (
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

func TestFolderPrefixesDistinguishKnownAbsentAndFilenameTokens(t *testing.T) {
	module := layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "lib"}
	for _, tc := range []struct {
		name, folder string
		bound        layout.Values
	}{
		{"maven-2-default", "org/acme/lib", module},
		{"gradle-default", "org.acme/lib", module},
		{"ivy-default", "org.acme/lib", module},
		{"maven-1-default", "org.acme", layout.Values{layout.TokenOrg: "org.acme"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bundled(t, tc.name).FolderPrefixes(module)[0]
			if got.Path != tc.folder || !reflect.DeepEqual(got.Bound, tc.bound) {
				t.Fatal(got)
			}
		})
	}
	values := layout.Values{
		layout.TokenOrg: "org.acme", layout.TokenModule: "lib", layout.TokenBaseRev: "1.0",
		layout.TokenFolderItegRev: "", layout.TokenFileItegRev: "",
	}
	l := bundled(t, "maven-2-default")
	prefix := l.FolderPrefixes(values)[0]
	if prefix.Path != "org/acme/lib/1.0" || prefix.Bound[layout.TokenBaseRev] != "1.0" {
		t.Fatal("known release did not reach its folder", prefix)
	}
	delete(values, layout.TokenFolderItegRev)
	prefix = l.FolderPrefixes(values)[0]
	if prefix.Path != "org/acme/lib" || prefix.Bound[layout.TokenBaseRev] != "" {
		t.Fatal("unknown snapshot suffix fabricated a version folder", prefix)
	}
	values[layout.TokenFolderItegRev] = "SNAPSHOT"
	if got := l.FolderPrefixes(values)[0]; got.Path != "org/acme/lib/1.0-SNAPSHOT" {
		t.Fatal(got)
	}
	if got := bundled(t, "gradle-default").FolderPrefixes(values)[0]; got.Bound[layout.TokenBaseRev] != "" {
		t.Fatal("incompatible snapshot grammar accepted", got)
	}
	if got := bundled(t, "simple-default").FolderPrefixes(values)[0]; got.Bound[layout.TokenBaseRev] != "" {
		t.Fatal("filename revision became folder authority", got)
	}
}
