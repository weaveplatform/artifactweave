package itemkit_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// The package download service names a file inside the quotes as it is,
// spaces and characters outside ASCII included, with no filename* part,
// as 7.161.26's JerseyArtifactoryResponse writes it. A quote or a
// backslash, which 7.x's PathValidator refuses in a path, is escaped so
// the name cannot end the quoted string early. The item route keeps
// blob.Disposition's form with filename* (#1505).
func TestUnitPackageDispositionQuotesTheNameAsItIs(t *testing.T) {
	for name, want := range map[string]string{
		"rake-12.3.1.gem":   `attachment; filename="rake-12.3.1.gem"`,
		"release one.box":   `attachment; filename="release one.box"`,
		"naïve-1.0.whl":     `attachment; filename="naïve-1.0.whl"`,
		"pkg+1.0;x=(1).zip": `attachment; filename="pkg+1.0;x=(1).zip"`,
		`odd"name.bin`:      `attachment; filename="odd\"name.bin"`,
		`back\slash.bin`:    `attachment; filename="back\\slash.bin"`,
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
