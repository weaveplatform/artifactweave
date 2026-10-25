package luarocks

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
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

func TestUnitRemoteManifestPreservesAllowedMetadata(t *testing.T) {
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

func TestUnitRemoteManifestAllowsPublicDottedNamesAndSkipsLegacyVersions(t *testing.T) {
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

func TestUnitRemoteManifestRejectsExecutableOrAmbiguousInput(t *testing.T) {
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

func TestUnitRemoteManifestAcceptsSafeLuaSyntaxWithoutExecutingIt(t *testing.T) {
	data := []byte(`-- the upstream writer may use return and semicolon separators
return { ["commands"] = {}; ['modules'] = {}; ["repository"] = {
  ['safe.name'] = { ['1.0-1'] = {
    { arch = 'rockspec', license = "O'Reilly", downloads = 12, stable = true },
    { arch = "src", tags = {"one", "two"} },
  } },
} }`)
	m, err := parseRemoteManifest(data)
	if err != nil || !m.has("safe.name-1.0-1.rockspec") ||
		!m.has("safe.name-1.0-1.src.rock") {
		t.Fatalf("safe upstream syntax: %v %v", m.files, err)
	}
	projected, paths, err := m.filtered(func(path string) bool {
		return path == "safe.name-1.0-1.rockspec"
	})
	if err != nil || len(paths) != 1 || !bytes.Contains(projected, []byte(`["downloads"]=12`)) ||
		!bytes.Contains(projected, []byte(`["stable"]=true`)) {
		t.Fatalf("safe metadata lost: %v %v %s", err, paths, projected)
	}
	filtered, err := parseRemoteManifest(projected)
	if err != nil || !filtered.has(paths[0]) || filtered.has("safe.name-1.0-1.src.rock") {
		t.Fatalf("projected manifest changed identity: %v %v", filtered.files, err)
	}
}

func TestUnitRemoteManifestEscapedMetadataRoundTrips(t *testing.T) {
	data := []byte(`commands = {} modules = {} repository = {
  ["quoted"] = { ["1.0-1"] = {
    { arch = "rockspec", note = "a \"quote\" and \\ path", apostrophe = 'it\'s safe' },
  } },
}`)
	m, err := parseRemoteManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	projected, paths, err := m.filtered(func(string) bool { return true })
	if err != nil || len(paths) != 1 {
		t.Fatalf("safe escaped metadata: %v %v", err, paths)
	}
	if reparsed, err := parseRemoteManifest(projected); err != nil || !reparsed.has(paths[0]) {
		t.Fatalf("escaped metadata failed to round-trip: %v %s", err, projected)
	}
}

func TestUnitRemoteManifestRejectsInvalidLuaAndIdentityVariants(t *testing.T) {
	base := `commands = {} modules = {} repository = { ["safe"] = { ["1.0-1"] = {{ arch = "src" }} } }`
	for name, data := range map[string]string{
		"empty":             "",
		"duplicate root":    base + ` repository = {}`,
		"missing commands":  `modules = {} repository = {}`,
		"root code":         base + ` os.execute('bad')`,
		"duplicate package": `commands = {} modules = {} repository = { ["safe"] = {}, ["safe"] = {} }`,
		"duplicate version": `commands = {} modules = {} repository = { ["safe"] = { ["1.0-1"] = {}, ["1.0-1"] = {} } }`,
		"named record":      `commands = {} modules = {} repository = { ["safe"] = { ["1.0-1"] = { first = { arch = "src" } } } }`,
		"missing arch":      `commands = {} modules = {} repository = { ["safe"] = { ["1.0-1"] = {{ dependencies = {} }} } }`,
		"duplicate arch":    `commands = {} modules = {} repository = { ["safe"] = { ["1.0-1"] = {{ arch = "src", arch = "src" }} } }`,
		"invalid number":    strings.Replace(base, `arch = "src"`, `arch = "src", downloads = 1..2`, 1),
		"byte escape":       strings.Replace(base, `["safe"]`, `["s\\123afe"]`, 1),
		"line break string": strings.Replace(base, `["safe"]`, `["s\\nafe"]`, 1),
		"unfinished quote":  strings.Replace(base, `["safe"]`, `["safe]`, 1),
		"unknown escape":    strings.Replace(base, `["safe"]`, `["s\\xafe"]`, 1),
		"invalid utf8":      base + string([]byte{0xff}),
		"deep metadata": strings.Replace(base, `arch = "src"`,
			`arch = "src", extra = `+strings.Repeat("{", 26)+`true`+strings.Repeat("}", 26), 1),
		"non-table package": `commands = {} modules = {} repository = { ["safe"] = "bad" }`,
		"non-table version": `commands = {} modules = {} repository = { ["safe"] = { ["1.0-1"] = "bad" } }`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRemoteManifest([]byte(data)); err == nil {
				t.Fatal("accepted unsafe manifest")
			}
		})
	}
}

func TestUnitValidateRemoteManifestRejectsCancellationAndLengthMismatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		data string
		size int64
	}{
		"cancelled": {ctx: ctx, data: fixtureManifest, size: int64(len(fixtureManifest))},
		"empty":     {ctx: t.Context(), data: "", size: 0},
		"truncated": {ctx: t.Context(), data: fixtureManifest, size: int64(len(fixtureManifest) + 1)},
		"oversized": {ctx: t.Context(), data: fixtureManifest, size: remoteManifestLimit + 1},
		"unsafe":    {ctx: t.Context(), data: `repository = os.execute('bad')`, size: 30},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRemoteManifest(tc.ctx, bytes.NewReader([]byte(tc.data)), tc.size,
				source.MetadataResponse{}); err == nil {
				t.Fatal("admitted invalid metadata")
			}
		})
	}
}
