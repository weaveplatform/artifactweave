package luarocks

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestAccVirtualSourceRequiresProtocolAwareResolution(t *testing.T) {
	if _, err := (virtualSource{}).Item(
		t.Context(),
		nil,
		repo.Config{},
		"manifest",
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatalf("generic virtual item resolution: %v", err)
	}
}

func TestUnitMergeManifestsPreservesIdentityAndMemberOrder(t *testing.T) {
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

func TestUnitMergeManifestsRejectsInvalidMemberRecords(t *testing.T) {
	record := func(arch luaValue) remoteManifest {
		return remoteManifest{repository: luaValue{kind: '{', fields: []luaField{{
			key: "safe", value: luaValue{kind: '{', fields: []luaField{{
				key: "1.0-1", value: luaValue{kind: '{', fields: []luaField{{
					value: luaValue{kind: '{', fields: []luaField{{key: "arch", value: arch}}},
				}}},
			}}},
		}}}}
	}
	if _, err := mergeManifests(
		[]remoteManifest{record(luaValue{kind: 'n', text: "5"})},
	); err == nil {
		t.Fatal("accepted a member record with no string architecture")
	}
	unsafe := record(luaValue{kind: 's', text: "../src"})
	if _, err := mergeManifests([]remoteManifest{unsafe}); err == nil {
		t.Fatal("accepted an unsafe member rock filename")
	}
	deep := record(luaValue{kind: 's', text: "src"})
	nested := luaValue{kind: 's', text: "leaf"}
	for range remoteManifestDepth + 1 {
		nested = luaValue{kind: '{', fields: []luaField{{value: nested}}}
	}
	archRecord := &deep.repository.fields[0].value.fields[0].value.fields[0].value
	archRecord.fields = append(archRecord.fields, luaField{key: "extra", value: nested})
	if _, err := mergeManifests([]remoteManifest{deep}); err == nil {
		t.Fatal("accepted member metadata deeper than the safe Lua limit")
	}
}
