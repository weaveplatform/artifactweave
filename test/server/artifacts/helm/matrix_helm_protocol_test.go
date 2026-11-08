package helm_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

var helmProtocolMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		f.deploy(t, "helm-local", "work/chart.tgz", helmChart("web", "1.0.0"))
		seedHelmIndex(t, f.items)
	},
	rows: func() []matrixRow {
		const root = "/artifactory/api/helm/helm-local/"
		read := holds("helm-local", "work/chart.tgz", types.ActionRead)
		return []matrixRow{
			row(
				helmIndex,
				root+"index.yaml",
				"",
				read,
				200,
			),
			row(helmIndexHead, root+"index.yaml", "", read, 200),
			row(
				helmDownload,
				root+"work/chart.tgz",
				"",
				read,
				200,
			),
			row(helmDownloadHead, root+"work/chart.tgz", "", read, 200),
		}
	},
}
