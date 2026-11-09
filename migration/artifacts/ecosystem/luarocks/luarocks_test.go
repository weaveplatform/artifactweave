package luarocks

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnitIdentityAndRockspecAdmission(t *testing.T) {
	for _, tc := range []struct {
		path, name, version, arch string
		valid                     bool
	}{
		{"my-rock-1.2.3-4.rockspec", "my-rock", "1.2.3-4", "rockspec", true},
		{"my-rock-1.2.3-4.src.rock", "my-rock", "1.2.3-4", "src", true},
		{"my-rock-1.2.3-4.linux-x86_64.rock", "my-rock", "1.2.3-4", "linux-x86_64", true},
		{"nested/my-rock-1.2.3-4.rockspec", "", "", "", false},
		{"my-rock-1.2.3.rockspec", "", "", "", false},
	} {
		got, ok := Parse(tc.path)
		if ok != tc.valid ||
			ok && (got.Name != tc.name || got.Version != tc.version || got.Arch != tc.arch) {
			t.Errorf("Parse(%q) = %+v, %t", tc.path, got, ok)
		}
	}
	id, _ := Parse("my-rock-1.2.3-4.rockspec")
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{"package = \"my-rock\"\nversion = \"1.2.3-4\"\n", true},
		{"package = \"other\"\nversion = \"1.2.3-4\"\n", false},
		{"package = \"my-rock\"\npackage = \"my-rock\"\nversion = \"1.2.3-4\"\n", false},
		{"package = \"my-rock\"\nversion = load(\"bad\")\n", false},
	} {
		if ok := validateRockspec([]byte(tc.body), id) == nil; ok != tc.valid {
			t.Errorf("validateRockspec(%q) valid=%t", tc.body, ok)
		}
	}
}

func TestUnitManifestIsValidLuaAssignmentsForNativeClient(t *testing.T) {
	data, err := encodeManifest([]manifestEntry{
		{"a", "1.0-1", "rockspec"},
		{"a", "1.0-1", "src"},
		{"a", "2.0-1", "rockspec"},
		{"b", "1.0-1", "all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"commands = {}", "modules = {}", `repository = {`, `["a"] = {`, `["1.0-1"] = {`, `arch = "src"`, `["b"] = {`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("manifest missing %q: %s", want, data)
		}
	}
	if strings.Count(string(data), "[\"a\"]") != 1 {
		t.Fatalf("duplicate package in %s", data)
	}
}
