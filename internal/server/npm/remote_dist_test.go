package npm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestRemoteURLPolicy(t *testing.T) {
	yes := true
	c := repo.Config{
		URL:                          "https://registry.example/base",
		ExternalDependenciesEnabled:  &yes,
		ExternalDependenciesPatterns: []string{"https://cdn.example/packages/**"},
	}
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"pkg.tgz", true},
		{"/base/pkg.tgz", true},
		{"https://registry.example/base/pkg.tgz?signature=opaque", true},
		{"https://cdn.example/packages/pkg.tgz", true},
		{"https://cdn.example/other/pkg.tgz", false},
		{"http://cdn.example/packages/pkg.tgz", false},
		{"https://evil.example/pkg.tgz", false},
		{"https://user:secret@registry.example/base/pkg", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"https://registry.example/base/pkg#fragment", false},
		{"https://registry.example/base/pkg\\evil", false},
		{"http://[", false},
		{"../../secret", false},
		{"https://registry.example/base%2f../secret", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			_, err := remoteTarballURL(c, tc.url)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed %v err %v", tc.allowed, err)
			}
		})
	}
	c.ExternalDependenciesEnabled = nil
	if _, err := remoteTarballURL(c, "https://cdn.example/packages/pkg.tgz"); err == nil {
		t.Fatal("disabled external allowed")
	}
}

func TestRemoteDistValidationPreservesSignatures(t *testing.T) {
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
	for _, dist := range []string{`{}`, `{"tarball":false}`, `{"tarball":"pkg","integrity":false}`, `{"tarball":"pkg","shasum":false}`, `{"tarball":"https://bad.example/pkg"}`} {
		good["dist"] = json.RawMessage(dist)
		if _, err = remoteDist(c, "pkg", "1.0.0", good); err == nil {
			t.Fatal(dist)
		}
	}
}

func TestRemoteIntegritySyntax(t *testing.T) {
	for _, raw := range []string{"broken", "sha512-bad", "sha512-" + base64.StdEncoding.EncodeToString([]byte("short")), "unknown-" + base64.StdEncoding.EncodeToString([]byte("value")), "sha512-" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64)) + " sha512-" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 64))} {
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

func TestRemoteMetadataValidation(t *testing.T) {
	for _, input := range []string{"[]", "null", "invalid"} {
		if err := validateRemoteJSON(
			context.Background(),
			strings.NewReader(input),
			int64(len(input)),
		); err == nil {
			t.Fatal(input)
		}
	}
	if err := validateRemoteJSON(
		context.Background(),
		strings.NewReader("{}"),
		remoteMetadataLimit+1,
	); err == nil {
		t.Fatal("oversized metadata")
	}
	if err := validateRemoteJSON(context.Background(), strings.NewReader("{}"), 2); err != nil {
		t.Fatal(err)
	}
}
