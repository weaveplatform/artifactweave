package aieditorextensions

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func zipTestFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for name, content := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestUnitExtensionAssetValidatorsRejectSwappedOrMalformedContent(t *testing.T) {
	t.Parallel()
	manifest := []byte(
		`{"publisher":"ms-vscode","name":"hexeditor","version":"1.11.1","engines":{"vscode":"^1.89.0"}}`,
	)
	if err := validateManifest(
		bytes.NewReader(manifest),
		int64(len(manifest)),
		"ms-vscode",
		"hexeditor",
		"1.11.1",
	); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body []byte
		size int64
	}{
		{"wrong-publisher", bytes.Replace(manifest, []byte("ms-vscode"), []byte("other"), 1), -1},
		{"wrong-version", bytes.Replace(manifest, []byte("1.11.1"), []byte("1.11.2"), 1), -1},
		{"missing-engine", bytes.Replace(manifest, []byte("^1.89.0"), []byte(""), 1), -1},
		{"wrong-size", manifest, int64(len(manifest) + 1)},
		{"too-large", manifest, maxManifest + 1},
		{"invalid-json", []byte(`{`), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.size < 0 {
				tc.size = int64(len(tc.body))
			}
			if err := validateManifest(
				bytes.NewReader(tc.body),
				tc.size,
				"ms-vscode",
				"hexeditor",
				"1.11.1",
			); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	valid := zipTestFiles(
		t,
		map[string][]byte{"extension/package.json": manifest, "extension/README.md": []byte("x")},
	)
	if err := validatePackage(
		bytes.NewReader(valid),
		int64(len(valid)),
		"ms-vscode",
		"hexeditor",
		"1.11.1",
	); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		archive []byte
	}{
		{"wrong-identity", zipTestFiles(t, map[string][]byte{"extension/package.json": bytes.Replace(manifest, []byte("hexeditor"), []byte("other"), 1)})},
		{"missing-manifest", zipTestFiles(t, map[string][]byte{"extension/README.md": []byte("x")})},
		{"path-traversal", zipTestFiles(t, map[string][]byte{"../extension/package.json": manifest})},
		{"invalid-zip", []byte("not a zip")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePackage(
				bytes.NewReader(tc.archive),
				int64(len(tc.archive)),
				"ms-vscode",
				"hexeditor",
				"1.11.1",
			); err == nil {
				t.Fatal("unsafe VSIX accepted")
			}
		})
	}
	if err := validatePackage(
		bytes.NewReader(valid),
		maxPackage+1,
		"ms-vscode",
		"hexeditor",
		"1.11.1",
	); err == nil {
		t.Fatal("oversized VSIX accepted")
	}
	sum := sha256.Sum256(valid)
	statement, _ := json.Marshal(
		map[string]any{
			"package": map[string]any{
				"size":    len(valid),
				"digests": map[string]string{"sha256": base64.StdEncoding.EncodeToString(sum[:])},
			},
		},
	)
	sig := zipTestFiles(
		t,
		map[string][]byte{".signature.manifest": statement, ".signature.p7s": []byte("signature")},
	)
	if err := validateSignature(
		bytes.NewReader(sig),
		int64(len(sig)),
		strings.Repeat("0", 64),
	); err == nil {
		t.Fatal("signature accepted for wrong VSIX")
	}
	if err := validateSignature(
		bytes.NewReader(sig),
		int64(len(sig)),
		strings.ToUpper(hex.EncodeToString(sum[:])),
	); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "invalid"} {
		if err := validateSignature(bytes.NewReader(sig), int64(len(sig)), bad); err == nil {
			t.Fatal("invalid digest admitted")
		}
	}
	if err := validateSignature(
		bytes.NewReader([]byte("invalid")),
		7,
		hex.EncodeToString(sum[:]),
	); err == nil {
		t.Fatal("invalid signature ZIP admitted")
	}
}

func TestUnitZIPPathGrammar(t *testing.T) {
	t.Parallel()
	for _, good := range []string{"extension/package.json", "extension/assets/icon.svg", "extension/"} {
		if !safeZIPName(good) {
			t.Errorf("rejected %q", good)
		}
	}
	for _, bad := range []string{"", "/absolute", "../traversal", "extension//double", "extension/../package.json", "extension\\package.json", "./extension/package.json"} {
		if safeZIPName(bad) {
			t.Errorf("admitted %q", bad)
		}
	}
}

type seekerWithoutReaderAt struct{ inner *bytes.Reader }

func (s seekerWithoutReaderAt) Read(p []byte) (int, error) { return s.inner.Read(p) }
func (s seekerWithoutReaderAt) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}

func TestUnitZIPValidationForStreamingAndMalformedSignature(t *testing.T) {
	t.Parallel()
	manifest := []byte(
		`{"publisher":"ms-vscode","name":"hexeditor","version":"1.11.1","engines":{"vscode":"^1.89.0"}}`,
	)
	vsix := zipTestFiles(t, map[string][]byte{"extension/package.json": manifest})
	stream := seekerWithoutReaderAt{bytes.NewReader(vsix)}
	if err := validatePackage(
		stream,
		int64(len(vsix)),
		"ms-vscode",
		"hexeditor",
		"1.11.1",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := readZIP(
		seekerWithoutReaderAt{bytes.NewReader(vsix)},
		int64(len(vsix)+1),
		maxPackage,
	); err == nil {
		t.Fatal("short stream accepted")
	}
	if _, err := readZIP(
		seekerWithoutReaderAt{bytes.NewReader(vsix)},
		64<<20+1,
		maxPackage,
	); err == nil {
		t.Fatal("unbounded stream accepted")
	}
	if _, err := readZIP(seekerWithoutReaderAt{bytes.NewReader(nil)}, 0, maxPackage); err == nil {
		t.Fatal("empty ZIP accepted")
	}
	sha := sha256.Sum256(vsix)
	for _, tc := range []struct {
		name  string
		files map[string][]byte
	}{
		{"missing-signature", map[string][]byte{".signature.manifest": []byte(`{}`)}},
		{"invalid-json", map[string][]byte{".signature.manifest": []byte(`{`), ".signature.p7s": []byte("signature")}},
		{"empty-proof", map[string][]byte{".signature.manifest": []byte(`{}`), ".signature.p7s": nil}},
		{"wrong-digest", map[string][]byte{".signature.manifest": []byte(`{"package":{"size":123,"digests":{"sha256":"AAAA"}}}`), ".signature.p7s": []byte("signature")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := zipTestFiles(t, tc.files)
			if err := validateSignature(
				bytes.NewReader(b),
				int64(len(b)),
				hex.EncodeToString(sha[:]),
			); err == nil {
				t.Fatal("invalid signature accepted")
			}
		})
	}
}

func TestUnitAssetTypeAndOriginGrammar(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Microsoft.VisualStudio.Code.Manifest", "A9.B"} {
		if !safeAssetType(name) {
			t.Errorf("rejected %q", name)
		}
	}
	for _, name := range []string{"", ".hidden", "bad.", "bad/type", "bad%2Ftype", strings.Repeat("a", 129)} {
		if safeAssetType(name) {
			t.Errorf("admitted %q", name)
		}
	}
	good := "https://ms-vscode.gallerycdn.vsassets.io/extensions/ms-vscode/hexeditor/1.11.1/123/Microsoft.VisualStudio.Code.Manifest"
	if !safeAssetSource(
		marketplaceGallery,
		good,
		"ms-vscode",
		"hexeditor",
		"1.11.1",
		manifestAsset,
	) {
		t.Fatal("official CDN URL rejected")
	}
	for _, raw := range []string{"https://evil.example/extensions/ms-vscode/hexeditor/1.11.1/123/Microsoft.VisualStudio.Code.Manifest", "https://ms-vscode.gallerycdn.vsassets.io/extensions/other/hexeditor/1.11.1/123/Microsoft.VisualStudio.Code.Manifest", good + "?token=x", strings.Replace(good, "https:", "http:", 1), strings.Replace(good, "Manifest", "VSIXPackage", 1)} {
		if safeAssetSource(
			marketplaceGallery,
			raw,
			"ms-vscode",
			"hexeditor",
			"1.11.1",
			manifestAsset,
		) {
			t.Errorf("admitted %q", raw)
		}
	}
}
