package swift

import "testing"

func TestUnitPackagePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"acme/Widget/Widget-1.2.3.zip", true},
		{"acme/Widget/Widget-1.2.3-beta.1.zip", true},
		{"acme/Widget/Widget-1.2.3+build.1.zip", true},
		{"acme/Widget/Other-1.2.3.zip", false},
		{"acme/Widget/Widget-01.2.3.zip", false},
		{"acme/Widget/Widget-1.2.zip", false},
		{"acme/_Widget/_Widget-1.2.3.zip", false},
		{"acme/Widget/../Widget-1.2.3.zip", false},
		{"acme/Widget/Widget-1.2.3.zip/extra", false},
	} {
		_, ok := ParseArchivePath(tc.path)
		if ok != tc.want {
			t.Errorf("%q accepted=%v, want %v", tc.path, ok, tc.want)
		}
	}
}
