package ecosystem

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
)

// Classic Helm repositories have the Helm format; Helm OCI ones keep the
// image format (TestUnitImagesHaveTheirFormat).
func TestUnitHelmHasItsFormat(t *testing.T) {
	if h, ok := For(repo.Helm).(helm.Handler); !ok {
		t.Fatalf("%#v", h)
	}
}
