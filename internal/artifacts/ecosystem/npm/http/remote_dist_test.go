package npmhttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestUnitRemoteURLPolicy: a packument's dist.tarball resolves against the
// remote's URL to wherever it points, in the registry, elsewhere on its host
// or on another host, as 7.x follows a tarball's location (RTFACT-23931,
// 7.24.1; 7.161.26's RemoteRepoBase.mayReplaceConfiguredOrigin), whatever
// the remote's externalDependencies fields hold, which 7.x keeps for npm
// virtual repositories alone (#1574). The safety refusals stay.
func TestUnitRemoteURLPolicy(t *testing.T) {
	yes := true
	for _, c := range []repo.Config{
		{URL: "https://registry.example/base"},
		{
			URL:                          "https://registry.example/base",
			ExternalDependenciesEnabled:  &yes,
			ExternalDependenciesPatterns: []string{"https://cdn.example/packages/**"},
		},
	} {
		for _, tc := range []struct {
			url, want string
		}{
			{"pkg.tgz", "https://registry.example/base/pkg.tgz"},
			{"/base/pkg.tgz", "https://registry.example/base/pkg.tgz"},
			{
				"https://registry.example/base/pkg.tgz?signature=opaque",
				"https://registry.example/base/pkg.tgz?signature=opaque",
			},
			{"/files/pkg.tgz", "https://registry.example/files/pkg.tgz"},
			{"../../secret", "https://registry.example/secret"},
			{"https://registry.example/files/pkg.tgz", "https://registry.example/files/pkg.tgz"},
			{"https://cdn.example/packages/pkg.tgz", "https://cdn.example/packages/pkg.tgz"},
			{"https://cdn.example/other/pkg.tgz", "https://cdn.example/other/pkg.tgz"},
			{"https://evil.example/pkg.tgz", "https://evil.example/pkg.tgz"},
			{"http://cdn.example/packages/pkg.tgz", ""},
			{"https://user:secret@registry.example/base/pkg", ""},
			{"file:///etc/passwd", ""},
			{"javascript:alert(1)", ""},
			{"https://registry.example/base/pkg#fragment", ""},
			{"https://registry.example/base/pkg\\evil", ""},
			{"http://[", ""},
			{"https://registry.example/base%2f../secret", ""},
			{"https://cdn.example/a/%2e%2e/secret", ""},
		} {
			t.Run(tc.url, func(t *testing.T) {
				got, err := remoteTarballURL(c, tc.url)
				if got != tc.want || (err == nil) != (tc.want != "") {
					t.Fatalf("resolved %q, %v; want %q", got, err, tc.want)
				}
			})
		}
	}
	// An HTTP registry may name an HTTPS tarball, never the reverse.
	got, err := remoteTarballURL(
		repo.Config{URL: "http://registry.example"},
		"https://cdn.example/pkg.tgz",
	)
	if err != nil || got != "https://cdn.example/pkg.tgz" {
		t.Fatalf("upgrade: %q, %v", got, err)
	}
}

func TestUnitRemoteDistValidationPreservesSignatures(t *testing.T) {
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64))
	good := remoteDocument{
		"name":    jsonString("pkg"),
		"version": jsonString("1.0.0"),
		"dist": encode(
			map[string]any{
				"tarball":    "pkg.tgz",
				"integrity":  integrity,
				"signatures": []map[string]string{{"sig": "kept"}},
			},
		),
	}
	c := repo.Config{URL: "https://registry.example"}
	d, err := remoteDist(c, "pkg", "1.0.0", good)
	if err != nil || len(d.checksums.SHA512) != 128 ||
		!strings.Contains(string(d.fields["signatures"]), "kept") {
		t.Fatalf("dist %+v %v", d, err)
	}
	for _, field := range []string{"name", "version", "dist"} {
		changed := remoteDocument{}
		for key, value := range good {
			changed[key] = value
		}
		changed[field] = json.RawMessage(`null`)
		if _, err = remoteDist(c, "pkg", "1.0.0", changed); err == nil {
			t.Fatal(field)
		}
	}
	if _, err = remoteDist(c, "pkg", "bad", good); err == nil {
		t.Fatal("invalid version")
	}
	for _, dist := range []string{`{}`, `{"tarball":false}`, `{"tarball":"pkg","integrity":false}`, `{"tarball":"pkg","shasum":false}`, `{"tarball":"https://u:p@bad.example/pkg"}`} {
		good["dist"] = json.RawMessage(dist)
		if _, err = remoteDist(c, "pkg", "1.0.0", good); err == nil {
			t.Fatal(dist)
		}
	}
}

func TestUnitRemoteIntegritySyntax(t *testing.T) {
	for _, raw := range []string{"broken", "sha512-bad", "sha512-" + base64.StdEncoding.EncodeToString([]byte("short")), "unknown-" + base64.StdEncoding.EncodeToString([]byte("value"))} {
		if _, err := distChecksums(raw, ""); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := distChecksums("", "bad"); err == nil {
		t.Fatal("invalid SHA1")
	}
	for _, alg := range []struct {
		name string
		size int
	}{{"sha512", 64}, {"sha256", 32}, {"sha1", 20}} {
		if _, err := distChecksums(
			alg.name+"-"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, alg.size)),
			"",
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := distChecksums("", strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
}

func TestUnitRemoteMetadataValidation(t *testing.T) {
	for _, input := range []string{"[]", "null", "invalid", "{} {}", "{}junk"} {
		if _, err := decodeRemoteDocument(strings.NewReader(input)); err == nil {
			t.Fatal(input)
		}
	}
	if _, err := decodeRemoteDocument(strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
	for _, validate := range []func(context.Context, io.ReadSeeker, int64) error{validatePackageMetadata(repo.Config{}, "pkg"), validateSearchMetadata} {
		if err := validate(
			context.Background(),
			strings.NewReader("{}"),
			remoteMetadataLimit+1,
		); err == nil {
			t.Fatal("oversized metadata")
		}
	}
}
