package terraformhttp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestUnitModuleChecksumFilePortableRecords(t *testing.T) {
	t.Parallel()
	for _, algorithm := range []struct {
		name   string
		length int
	}{{"md5", 32}, {"sha1", 40}, {"sha256", 64}, {"sha512", 128}} {
		t.Run(algorithm.name, func(t *testing.T) {
			t.Parallel()
			digest := strings.Repeat("a", algorithm.length)
			for _, tc := range []struct{ name, body string }{
				{"gnu-text", digest + "  module.zip\n"},
				{"gnu-binary", digest + " *module.zip\r\n"},
				{"gnu-portable-marker", digest + " ?module.zip"},
				{"bsd", strings.ToUpper(algorithm.name) + " (module.zip) = " + digest + "\n"},
				{"standalone", digest},
				{"blank-lines", "\n \t\r\n" + digest + "  module.zip\n\n"},
				{"select-among-other-files", strings.Repeat("b", algorithm.length) + "  other.zip\n" + digest + "  module.zip\n"},
				{"relative-posix", digest + "  ../archives/module.zip\n"},
				{"absolute-posix", digest + "  /archives/module.zip\n"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := parseModuleChecksumFile(
						[]byte(tc.body),
						"https://up.example/archives/module.zip?private=archive",
						"https://up.example/checksums/SHA256SUMS?private=file",
					)
					want, e := moduleChecksum(algorithm.name + ":" + digest)
					if err != nil || e != nil || got != want {
						t.Fatal("portable checksum record was not selected", got, err)
					}
				})
			}
		})
	}
	for _, name := range []string{"module with spaces.zip", "module (example).zip", "módulo.zip", "module%literal.zip", "module#literal.zip"} {
		t.Run(name, func(t *testing.T) {
			archive := "https://up.example/archives/" + name
			if strings.Contains(name, "#") {
				archive = "https://up.example/archives/module%23literal.zip"
			}
			if strings.Contains(name, "%") {
				archive = "https://up.example/archives/module%25literal.zip"
			}
			if _, err := parseModuleChecksumFile([]byte(strings.Repeat("a", 64)+"  "+name), archive, "https://up.example/SUMS"); err != nil {
				t.Fatal("decoded archive basename lost", err)
			}
		})
	}
}

func TestUnitModuleChecksumFileRejectsMalformedAmbiguousAndMissingRecords(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"blank", "\r\n \t\n"},
		{"missing", digest + "  other.zip"},
		{"duplicate-selected", digest + "  module.zip\n" + digest + " *module.zip\n"},
		{"conflicting-selected", digest + "  module.zip\n" + strings.Repeat("b", 64) + "  module.zip\n"},
		{"standalone-with-record", digest + "\n" + digest + "  other.zip\n"},
		{"two-standalone", digest + "\n" + digest},
		{"malformed-other", "malformed other line\n" + digest + "  module.zip\n"},
		{"wrong-bsd-algorithm", "SHA1 (module.zip) = " + digest},
		{"unsupported-bsd-algorithm", "SHA384 (module.zip) = " + strings.Repeat("a", 96)},
		{"invalid-hex", strings.Repeat("z", 64) + "  module.zip"},
		{"missing-filename", digest + "  "},
		{"bad-marker", digest + " !module.zip"},
		{"missing-digest", "  module.zip"},
		{"single-space", digest + " module.zip"},
		{"embedded-cr", digest + "  module\r.zip"},
		{"control-character", digest + "  module\x00.zip"},
		{"backslash", digest + "  folder\\module.zip"},
		{"oversized-line", digest + "  " + strings.Repeat("x", 8193)},
		{"unsupported-standalone-prefix", "SHA256:" + digest},
		{"unsupported-gnu-prefix", "SHA256:" + digest + "  module.zip"},
		{"malformed-bsd", "SHA256 (module.zip = " + digest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseModuleChecksumFile([]byte(tc.body), "https://up.example/archives/module.zip", "https://up.example/SUMS"); err == nil {
				t.Fatal("invalid checksum file admitted")
			}
		})
	}
	if _, err := parseModuleChecksumFile([]byte(digest+"  /archives/module.zip"), "https://up.example/archives/module.zip", "https://other.example/SUMS"); err == nil {
		t.Fatal("absolute record borrowed a different origin")
	}
}

func TestUnitModuleChecksumFileSourceGrammarAndPrivateControl(t *testing.T) {
	t.Parallel()
	c := repo.Config{URL: "https://up.example"}
	for _, raw := range []string{"file:/tmp/SUMS", "ssh://up.example/SUMS", "../SUMS", "https://user:pass@up.example/SUMS", "https://up.example/SUMS#", "http://up.example/SUMS", "https://other.example/SUMS", "https://up.example/SUMS?checksum=file:next", "https://up.example/SUMS?archive=zip", "https://up.example/SUMS?terraform-get=1"} {
		if _, err := moduleChecksumURL(raw, "https://up.example/module.zip", c); err == nil {
			t.Fatal("unqualified checksum source admitted")
		}
	}
	got, err := parseModuleSource("/module.zip?checksum=file%3Ahttps%3A%2F%2Fup.example%2FSUMS%3Ftoken%3Dprivate", "https://up.example/download", c)
	if err != nil || got.ChecksumURL != "https://up.example/SUMS?token=private" || got.Target != "https://up.example/module.zip" {
		t.Fatal("checksum URL/control was not separated", got, err)
	}
	body := strings.Repeat("a", 64) + "  module.zip"
	control, _ := json.Marshal(source.ModuleChecksumResponse{Content: []byte(body)})
	checksums, err := parseModuleChecksumControl(strings.NewReader(string(control)), got.Target, got.ChecksumURL)
	if err != nil || checksums.SHA256 != strings.Repeat("a", 64) {
		t.Fatal("private checksum control failed", err)
	}
	for _, raw := range []string{`{}`, `null`, `{"content":"/w=="}`, string(control) + " {}", fmt.Sprintf(`{"content":%q,"unknown":true}`, "")} {
		if _, err := parseModuleChecksumControl(strings.NewReader(raw), got.Target, got.ChecksumURL); err == nil {
			t.Fatal("malformed private checksum control admitted")
		}
	}
}
