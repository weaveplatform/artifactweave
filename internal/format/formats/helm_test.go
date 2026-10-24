package formats

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Classic Helm repositories have the Helm format; Helm OCI ones keep the
// image format (TestUnitImagesHaveTheirFormat).
func TestUnitHelmHasItsFormat(t *testing.T) {
	if h, ok := For(repo.Helm).(helm.Handler); !ok {
		t.Fatalf("%#v", h)
	}
}
