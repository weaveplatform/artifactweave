package swift

import (
	"strings"
	"testing"
)

func TestUnitPackagePath(t *testing.T) {
	if ValidName("") || ValidName(strings.Repeat("a", 101)) {
		t.Fatal("empty or overlong Swift package name accepted")
	}
	for _, tc := range []struct {
		path     string
		expected bool
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
		if ok != tc.expected {
			t.Errorf("%q accepted=%v, want %v", tc.path, ok, tc.expected)
		}
	}
}
