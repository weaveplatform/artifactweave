package hex

import "testing"

func TestTarballPathGrammar(t *testing.T) {
	for _, tc := range []struct{ path, name, version string }{
		{"tarballs/decimal-2.0.0.tar", "decimal", "2.0.0"},
		{"tarballs/my-lib-1.0.0-rc.1.tar", "my-lib", "1.0.0-rc.1"},
		{"tarballs/../secret.tar", "", ""},
		{"tarballs/pkg.tar", "", ""},
	} {
		name, version, ok := splitTarball(tc.path)
		if ok != (tc.name != "") || name != tc.name || version != tc.version {
			t.Errorf("%s => %q %q %v", tc.path, name, version, ok)
		}
	}
}
