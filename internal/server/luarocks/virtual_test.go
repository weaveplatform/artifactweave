package luarocks

import (
	"strings"
	"testing"
)

func TestMergeManifestsPreservesIdentityAndMemberOrder(t *testing.T) {
	first, err := parseRemoteManifest([]byte(`commands = {}
modules = {}
repository = { ["shared"] = { ["1.0-1"] = { { arch = "rockspec", marker = "first" }, { arch = "src" } } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseRemoteManifest([]byte(`commands = {}
modules = {}
repository = { ["shared"] = { ["1.0-1"] = { { arch = "rockspec", marker = "second" }, { arch = "linux-x86_64" } }, ["2.0-1"] = { { arch = "rockspec" } } },
["other"] = { ["3.0-1"] = { { arch = "rockspec" } } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := mergeManifests([]remoteManifest{first, second})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := parseRemoteManifest(data)
	if err != nil {
		t.Fatalf("merged manifest invalid: %v\n%s", err, data)
	}
	for _, file := range []string{
		"shared-1.0-1.rockspec", "shared-1.0-1.src.rock",
		"shared-1.0-1.linux-x86_64.rock", "shared-2.0-1.rockspec", "other-3.0-1.rockspec",
	} {
		if !merged.has(file) {
			t.Fatalf("missing %s in %s", file, data)
		}
	}
	if strings.Contains(string(data), "second") {
		// Exact identities are represented by package/version/arch fields,
		// so only the marker proves which member won.
		t.Fatalf("later member replaced exact identity: %s", data)
	}
	if !strings.Contains(string(data), "first") {
		t.Fatalf("first member record lost: %s", data)
	}
}
