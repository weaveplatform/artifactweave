package itemkit_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// The package download service names a file inside the quotes as it is,
// spaces and sub-delimiters included, with no filename* part, as
// 7.161.26's JerseyArtifactoryResponse writes it. A quote or a backslash,
// which 7.x's PathValidator refuses in a path, is escaped so the name
// cannot end the quoted string early. A name with a character outside
// printable ASCII, a control character included, is named by filename*
// alone, every byte but RFC 3986's unreserved characters percent-encoded,
// as RequestResponseHelper.setHeaderForNonAscii names it. The item route
// keeps blob.Disposition's form with filename* (#1505).
func TestUnitPackageDispositionQuotesTheNameAsItIs(t *testing.T) {
	for name, want := range map[string]string{
		"rake-12.3.1.gem":   `attachment; filename="rake-12.3.1.gem"`,
		"release one.box":   `attachment; filename="release one.box"`,
		"pkg+1.0;x=(1).zip": `attachment; filename="pkg+1.0;x=(1).zip"`,
		"~tilde_ok-1.0.bin": `attachment; filename="~tilde_ok-1.0.bin"`,
		`odd"name.bin`:      `attachment; filename="odd\"name.bin"`,
		`back\slash.bin`:    `attachment; filename="back\\slash.bin"`,
		"naïve-1.0.whl":     "attachment; filename*=UTF-8''na%C3%AFve-1.0.whl",
		"日本 a+b&c~d.zip":    "attachment; filename*=UTF-8''%E6%97%A5%E6%9C%AC%20a%2Bb%26c~d.zip",
		"tab\tname.bin":     "attachment; filename*=UTF-8''tab%09name.bin",
		"del\x7fname.bin":   "attachment; filename*=UTF-8''del%7Fname.bin",
	} {
		if got := itemkit.PackageDisposition(name); got != want {
			t.Errorf("PackageDisposition(%q) = %q, want %q", name, got, want)
		}
	}
	if got, want := blob.Disposition("release one.box"),
		`attachment; filename="release one.box"; filename*=UTF-8''release%20one.box`; got != want {
		t.Errorf("blob.Disposition = %q, want %q", got, want)
	}
}
