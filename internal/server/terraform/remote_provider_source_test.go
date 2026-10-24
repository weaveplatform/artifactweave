package terraform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitRemoteProviderMetadataSchemas(t *testing.T) {
	q := providerOp{name: "hashicorp/null"}
	for _, raw := range []string{
		`null`, `{}`, `{"versions":null}`, `{"versions":[{"version":"bad"}]}`,
		`{"versions":[{"version":"1.0.0"},{"version":"1.0.0"}]}`,
		`{"versions":[{"version":"1.0.0","protocols":["five"]}]}`,
		`{"versions":[{"version":"1.0.0","protocols":["5.0","5.0"]}]}`,
		`{"versions":[{"version":"1.0.0","platforms":[{"os":"../linux","arch":"amd64"}]}]}`,
		`{"versions":[{"version":"1.0.0","platforms":[{"os":"linux","arch":"amd64"},{"os":"linux","arch":"amd64"}]}]}`,
	} {
		if _, err := parseRemoteProviderVersions(strings.NewReader(raw), q); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if _, err := parseRemoteProviderVersions(strings.NewReader(`{"versions":[]}`), q); err != nil {
		t.Fatal(err)
	}
	// The public registry still lists older releases with a major-only "4"
	// protocol alongside newer releases with "5.0". Both are valid registry
	// metadata, even when the client selects only the pinned newer release.
	for _, raw := range []string{
		`{"versions":[{"version":"1.0.0","protocols":["4"],"platforms":[{"os":"linux","arch":"amd64"}]},{"version":"3.3.2","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]}]}`,
		`{"versions":[{"version":"2.1.2","protocols":["4.0","5.0"]}]}`,
	} {
		if _, err := parseRemoteProviderVersions(strings.NewReader(raw), q); err != nil {
			t.Fatal("rejected valid public registry protocol versions", err)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"providers.v1":false}`, `{"providers.v1":"ftp://host/path/"}`, `{"providers.v1":"/api?token=secret"}`, `{"providers.v1":"/api"}`} {
		if _, err := parseProviderDiscovery(
			strings.NewReader(raw),
			"https://registry.example/",
		); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, protocols := range [][]string{nil, {"5."}, {".0"}, {"five.0"}, {"5.0.1"}, make([]string, 65), {strings.Repeat("1", 33) + ".0"}} {
		if validProviderProtocols(protocols, true) {
			t.Fatal("accepted", protocols)
		}
	}
}

func TestUnitRemoteProviderPackageSchema(t *testing.T) {
	f := newProviderFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	for _, field := range []string{"platform", "protocols", "filename", "hash", "keys", "private-key", "url", "host"} {
		t.Run(field, func(t *testing.T) {
			raw, _ := json.Marshal(f.metadata)
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "platform":
				doc["os"] = "windows"
			case "protocols":
				doc["protocols"] = []string{}
			case "filename":
				doc["filename"] = "../provider.zip"
			case "hash":
				doc["shasum"] = strings.Repeat("0", 62)
			case "keys":
				doc["signing_keys"] = map[string]any{}
			case "private-key":
				doc["signing_keys"] = map[string]any{
					"gpg_public_keys": []map[string]string{
						{
							"key_id":      f.key.KeyID(),
							"ascii_armor": "-----BEGIN PGP PRIVATE KEY BLOCK-----\ninvalid",
						},
					},
				}
			case "url":
				doc["download_url"] = "http://user:secret@upstream.example/provider.zip"
			case "host":
				doc["download_url"] = "https://unapproved.example/provider.zip"
			}
			data, _ := json.Marshal(doc)
			if _, err := parseRemoteProviderPackage(
				bytes.NewReader(data),
				f.up.URL,
				c,
				f.q,
			); err == nil {
				t.Fatal("accepted", field)
			}
		})
	}
	if _, err := parseRemoteProviderPackage(
		strings.NewReader(`null`),
		f.up.URL,
		c,
		f.q,
	); err == nil {
		t.Fatal("null package")
	}
}

func TestUnitRemoteMirrorSchemasAndStrongestHash(t *testing.T) {
	q := providerOp{
		name:           "hashicorp/null",
		version:        "1.0.0",
		host:           "registry.terraform.io",
		upstreamMirror: true,
	}
	c := repo.Config{
		TerraformRegistryURL:  "https://registry.example/api/",
		TerraformProvidersURL: "https://storage.example/",
	}
	for _, raw := range []string{`null`, `{}`, `{"versions":null}`, `{"versions":{"bad":{}}}`, `{"versions":{"1.0.0":null}}`, `{"versions":{"1.0.0":[]}}`} {
		if _, err := parseMirrorVersions(strings.NewReader(raw), q); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"archives":null}`, `{"archives":{"../amd64":{"url":"https://storage.example/a.zip"}}}`, `{"archives":{"linux_amd64":{"url":"ftp://storage.example/a.zip"}}}`, `{"archives":{"linux_amd64":{"url":"https://hostile.example/a.zip"}}}`} {
		if _, err := parseMirrorArchives(
			strings.NewReader(raw),
			"https://registry.example/api/",
			c,
			q,
		); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, hashes := range [][]string{{"h1:bad"}, {"zh:bad"}, {"invalid"}, {"x:unknown"}, {":hash"}, {"h1:"}} {
		data, _ := json.Marshal(
			map[string]any{
				"archives": map[string]remoteMirrorArchive{
					"linux_amd64": {URL: "https://storage.example/a.zip", Hashes: hashes},
				},
			},
		)
		if _, err := parseMirrorArchives(
			bytes.NewReader(data),
			"https://registry.example/api/",
			c,
			q,
		); err == nil {
			t.Fatal("accepted", hashes)
		}
	}
	archive := archiveZip(t, "provider")
	h1, ok, err := tf.H1(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || !ok {
		t.Fatal(err)
	}
	other := archiveZip(t, "different")
	wrong, ok, err := tf.H1(bytes.NewReader(other), int64(len(other)))
	if err != nil || !ok {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	for _, check := range []struct {
		hashes []string
		good   bool
	}{
		{nil, true},
		{[]string{wrong, h1}, true},
		{[]string{wrong, "zh:" + hex.EncodeToString(digest[:])}, false},
		{[]string{"zh:" + strings.Repeat("0", 64)}, false},
	} {
		err = verifyMirrorArchive(
			t.Context(),
			bytes.NewReader(archive),
			int64(len(archive)),
			check.hashes,
		)
		if (err == nil) != check.good {
			t.Fatal(check, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = verifyMirrorArchive(
		ctx,
		bytes.NewReader(archive),
		int64(len(archive)),
		[]string{h1},
	); err == nil {
		t.Fatal("cancelled h1 verification")
	}
	if err = verifyMirrorArchive(
		ctx,
		bytes.NewReader(archive),
		int64(len(archive)),
		[]string{"zh:" + strings.Repeat("0", 64)},
	); err == nil {
		t.Fatal("cancelled ZIP verification")
	}
}
