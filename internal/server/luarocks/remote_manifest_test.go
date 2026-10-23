package luarocks

import (
	"strings"
	"testing"
)

const fixtureManifest = `commands = {}
modules = {}
repository = {
  ["visible"] = {
    ["1.0-1"] = {
      { arch = "rockspec", dependencies = {"lua >= 5.1"} },
      { arch = "src" },
    },
  },
  ["hidden"] = { ["2.0-1"] = { { arch = "rockspec" } } },
}
`

func TestRemoteManifestPreservesAllowedMetadata(t *testing.T) {
	m, err := parseRemoteManifest([]byte(fixtureManifest))
	if err != nil {
		t.Fatal(err)
	}
	if !m.has("visible-1.0-1.rockspec") || !m.has("visible-1.0-1.src.rock") ||
		!m.has("hidden-2.0-1.rockspec") || m.has("missing-1.0-1.rockspec") {
		t.Fatalf("wrong remote file index: %v", m.files)
	}
	body, paths, err := m.filtered(
		func(path string) bool { return strings.HasPrefix(path, "visible-") },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.Contains(string(body), `dependencies`) ||
		strings.Contains(string(body), "hidden") {
		t.Fatalf("unsafe manifest projection: %s %v", body, paths)
	}
	filtered, err := parseRemoteManifest(body)
	if err != nil || !filtered.has("visible-1.0-1.rockspec") ||
		filtered.has("hidden-2.0-1.rockspec") {
		t.Fatalf("native manifest projection: %v %v", filtered.files, err)
	}
}

func TestRemoteManifestAllowsPublicDottedNamesAndSkipsLegacyVersions(t *testing.T) {
	data := []byte(`commands = {}
modules = {}
repository = {
  ["15puzzle.nvim"] = {
    ["1.4.0-1"] = {{ arch = "rockspec" }, { arch = "src" }},
    ["main"] = {{ arch = "rockspec" }},
  },
}`)
	m, err := parseRemoteManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if !m.has("15puzzle.nvim-1.4.0-1.rockspec") ||
		!m.has("15puzzle.nvim-1.4.0-1.src.rock") || m.has("15puzzle.nvim-main.rockspec") {
		t.Fatalf("wrong public package index: %v", m.files)
	}
	projection, _, err := m.filtered(func(string) bool { return true })
	if err != nil || strings.Contains(string(projection), "main") {
		t.Fatalf("legacy version leaked into projection: %v %s", err, projection)
	}
}

func TestRemoteManifestRejectsExecutableOrAmbiguousInput(t *testing.T) {
	for name, data := range map[string]string{
		"code":       `commands = {} modules = {} repository = os.execute("bad")`,
		"truncated":  `commands = {} modules = {} repository = { ["x"] = {`,
		"path":       `commands = {} modules = {} repository = { ["../x"] = { ["1-1"] = {{ arch="src" }} } }`,
		"duplicate":  `commands = {} modules = {} repository = { ["x"] = { ["1-1"] = {{ arch="src" }, { arch="src" }} } }`,
		"auxiliary":  `commands = { x = "hidden" } modules = {} repository = {}`,
		"wrong arch": `commands = {} modules = {} repository = { ["x"] = { ["1-1"] = {{ arch="../src" }} } }`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRemoteManifest([]byte(data)); err == nil {
				t.Fatal("accepted invalid remote manifest")
			}
		})
	}
}
