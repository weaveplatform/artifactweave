package bower

import (
	"testing"
)

func TestUnitBowerManifestIdentityAndPaths(t *testing.T) {
	for _, test := range []struct {
		path  string
		valid bool
	}{
		{"hello/1.0.0/bower.json", true},
		{"hello/1.0.0-alpha.1/lib/index.js", true},
		{"hello/1.0.0+build.1/lib/index.js", true},
		{"hello/1.0/bower.json", false},
		{"hello/1.0.0/../secret", false},
		{"hello/1.0.0/lib\\secret", false},
		{"hello/1.0.0/secret:stream", false},
		{"/hello/1.0.0/bower.json", false},
	} {
		_, _, _, valid := ParsePath(test.path)
		if valid != test.valid {
			t.Errorf("path %q valid=%t, want %t", test.path, valid, test.valid)
		}
	}
	for _, test := range []struct {
		json  string
		valid bool
	}{
		{`{"name":"hello","version":"1.0.0"}`, true},
		{`{"name":"hello","version":"1.0.0-alpha.1","dependencies":{"dep":"^1.0.0"}}`, true},
		{`{"name":"hello","version":"1.0.0","name":"other"}`, false},
		{`{"name":"../hello","version":"1.0.0"}`, false},
		{`{"name":"hello","version":"1.0"}`, false},
		{`{"name":"hello","version":"1.0.0"} true`, false},
	} {
		_, err := DecodeManifest([]byte(test.json))
		if (err == nil) != test.valid {
			t.Errorf("manifest %s err=%v", test.json, err)
		}
	}
}
