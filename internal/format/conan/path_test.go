package conan

import (
	"strings"
	"testing"
)

func TestUnitConanStoredPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"_/hello/1.0/_/rev/export/conanmanifest.txt", true},
		{"_/hello/1.0/_/rev/package/id/prev/conaninfo.txt", true},
		{"_/hello/1.0/_/rev/export/metadata/signature", true},
		{"_/hello/1.0/_/rev/export/../conanfile.py", false},
		{"_/hello/1.0/_/rev/export/.hidden", true},
		{"_/hello/1.0/_/rev/package/id", false},
		{"_/hello/1.0/_/rev/package/../prev/conaninfo.txt", false},
		{"_/hello/1.0/_/rev/other/conanfile.py", false},
		{"_/hello/1.0/_/rev/export/bad%file", false},
		{"_/hello/1.0/_/rev/export/bad\\file", false},
		{"_/hello/1.0/_/rev/export/", false},
		{"_/../1.0/_/rev/export/conanfile.py", false},
		{"_/hello/1.0/_/rev/export/conanfile.py/", false},
	} {
		_, ok := ParseStored(tc.path)
		if ok != tc.want {
			t.Errorf("ParseStored(%q) = %v, want %v", tc.path, ok, tc.want)
		}
	}
	for _, name := range []string{"", "/conanfile.py", "metadata//signature", "conanfile.py?x", "a\nb", "a;b"} {
		if SafeFile(name) {
			t.Errorf("SafeFile(%q) accepted unsafe name", name)
		}
	}
	if SafeFile("metadata/" + strings.Repeat("a", 256)) {
		t.Error("SafeFile accepted oversized component")
	}
}
