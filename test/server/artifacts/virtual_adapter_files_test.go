package artifacts_test

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	gemfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func adapterFileRows() []adapterConformance {
	return []adapterConformance{
		{repo.Gems, adapterGems},
		{repo.LuaRocks, adapterLuaRocks},
		{repo.Hex, adapterHex},
		{repo.Swift, adapterSwift},
	}
}

func adapterFixture(t *testing.T, family, name string) []byte {
	t.Helper()
	return repopath.Read(t, "internal/artifacts/ecosystem/"+family+"/testdata/"+name)
}

// fileOwners compares bytes from three independently known holders. Local and
// federated swap effective position through nesting; removing both leaves a
// repeated remote behind the same root/self cycle. The direct controls ensure
// a malformed upstream fixture cannot masquerade as a passed-over member.
func (g *adapterGraph) fileOwners(path string, route func(string) string, bodies map[string][]byte) {
	g.t.Helper()
	for _, key := range []string{"local", "federated"} {
		g.store(key, path, bodies[key])
		g.want(route(key), bodies[key])
	}
	g.t.Log("native file: direct remote control")
	g.want(route("origin"), bodies["origin"])
	g.t.Log("native file: local wins before configured-first remote")
	g.want(route("outer"), bodies["local"])
	g.graph([]string{"origin", "inner", "local", "origin", "outer"},
		[]string{"federated", "outer", "local", "inner", "federated"})
	g.t.Log("native file: nested federated wins before local")
	g.want(route("outer"), bodies["federated"])
	g.graph([]string{"origin", "inner", "origin", "outer"}, []string{"outer", "inner"})
	g.t.Log("native file: repeated remote wins after hosted members are removed")
	g.want(route("outer"), bodies["origin"])
}

func adapterGems(g *adapterGraph) {
	const p = "gems/rake-12.3.1.gem"
	fixture := adapterFixture(g.t, "gems", "rake-12.3.1.gem")
	// Additional tar padding changes the served representation, not the valid
	// gem's identity or its internal checksums.
	bodies := map[string][]byte{"local": fixture, "federated": append(bytes.Clone(fixture), make([]byte, 512)...), "origin": append(bytes.Clone(fixture), make([]byte, 1024)...)}
	pkg, err := gemfmt.Read(g.t.Context(), bytes.NewReader(bodies["origin"]), int64(len(bodies["origin"])), p)
	if err != nil {
		g.t.Fatal(err)
	}
	info := []byte("---\n12.3.1 |checksum:" + pkg.SHA256 + "\n")
	g.upstream("versions", []byte("---\nrake 12.3.1 "+gemfmt.InfoMD5(info)+"\n"))
	g.upstream("info/rake", info)
	g.upstream(p, bodies["origin"])
	g.fileOwners(p, func(key string) string { return "/artifactory/api/gems/" + key + "/" + p }, bodies)
}

func adapterLuaRocks(g *adapterGraph) {
	const p = "demo-1.0-1.rockspec"
	bodies := map[string][]byte{}
	for _, key := range []string{"local", "federated", "origin"} {
		bodies[key] = []byte(
			fmt.Sprintf("package = 'demo'\nversion = '1.0-1'\nsource = { url = 'https://example.invalid/demo.tar.gz' }\nbuild = { type = 'builtin', modules = {} }\n-- %s\n", key),
		)
	}
	g.upstream("manifest", []byte("commands = {}\nmodules = {}\nrepository = { demo = { ['1.0-1'] = { {arch = 'rockspec'} } } }\n"))
	g.upstream(p, bodies["origin"])
	g.fileOwners(p, func(key string) string { return "/artifactory/api/luarocks/" + key + "/" + p }, bodies)
}

func adapterHex(g *adapterGraph) {
	const p = "tarballs/decimal-2.0.0.tar"
	fixture := adapterFixture(g.t, "hex", "decimal-2.0.0.tar")
	bodies := map[string][]byte{"local": fixture, "federated": append(bytes.Clone(fixture), make([]byte, 512)...), "origin": append(bytes.Clone(fixture), make([]byte, 1024)...)}
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(bodies["origin"]))
	if err != nil {
		g.t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		g.t.Fatal(err)
	}
	registry, err := hexfmt.EncodePackage(hexfmt.RegistryPackage{Name: "decimal", Repository: "hexpm", Releases: []hexfmt.Package{pkg}}, key)
	if err != nil {
		g.t.Fatal(err)
	}
	c, err := g.w.Runtime.Store.Repos().Get(g.t.Context(), "origin")
	if err != nil {
		g.t.Fatal(err)
	}
	c.HexPublicKey = hexfmt.PublicKeyPEM(key)
	g.save(c, "POST")
	g.upstream("packages/decimal", registry)
	g.upstream(p, bodies["origin"])
	g.fileOwners(p, func(key string) string { return "/artifactory/api/hex/" + key + "/" + p }, bodies)
}

func adapterZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func adapterSwift(g *adapterGraph) {
	const p = "acme/demo/demo-1.0.0.zip"
	bodies := map[string][]byte{}
	for _, key := range []string{"local", "federated", "origin"} {
		bodies[key] = adapterZip(
			g.t,
			map[string]string{"Package.swift": "// swift-tools-version: 6.0\nimport PackageDescription\nlet package = Package(name: \"demo\")\n", "owner.txt": key},
		)
	}
	g.upstream(
		"acme/demo/1.0.0",
		[]byte(
			fmt.Sprintf(`{"id":"acme.demo","version":"1.0.0","resources":[{"name":"source-archive","type":"application/zip","checksum":"%x"}]}`, sha256.Sum256(bodies["origin"])),
		),
	)
	g.upstream("acme/demo/1.0.0.zip", bodies["origin"])
	g.fileOwners(p, func(key string) string { return "/artifactory/api/swift/" + key + "/acme/demo/1.0.0.zip" }, bodies)
}

// Keep protocol ownership assertions readable when the surrounding JSON also
// contains rewritten virtual URLs or independently merged releases.
func (g *adapterGraph) contains(route string, wanted ...string) {
	g.t.Helper()
	body := string(g.get(route))
	for _, value := range wanted {
		if !strings.Contains(body, value) {
			g.t.Fatalf("GET %s = %s; want %q", route, body, value)
		}
	}
}
