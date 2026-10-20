package pypi_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/pypi"
)

func TestIsPrerelease(t *testing.T) {
	for version, want := range map[string]bool{"1.0": false, "1.0rc1": true, "1.0a1": true, "v2.0-dev1": true, "1.0.post1": false, "1.0.post1.dev2": true, "1.0+dev": false, "legacy": false, "": false} {
		if got := pypi.IsPrerelease(version); got != want {
			t.Errorf("%s: %v", version, got)
		}
	}
}

func TestDescription(t *testing.T) {
	for text, want := range map[string]string{
		"": "", "Name: demo\n": "", "Name: demo\n\nBody\n": "Body\n", "Name: demo\r\n\r\nBody\r\n": "Body\r\n",
		"Description: Legacy\n continuation\n": "Legacy\n continuation", "Description: Legacy\n\nBody": "Body",
		"\xef\xbb\xbfName: demo\n\nBody": "Body", "Name: demo\nBody without separator": "Body without separator",
	} {
		if got := pypi.Description(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}
