package terraform_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

var terraformMatrixPart = matrixPart{
	tune: tuneRegistry,
	seed: func(t *testing.T, f *matrixFixture) { t.Helper(); seedRegistryModule(t, f.db, f.items.Blobs) },
	rows: func() []matrixRow {
		allow := func(matrixPrincipal) int { return 0 }
		discovery := at(tfDiscovery, "", allow, 200)
		discovery.open = true
		head := at(tfDiscoveryHead, "", allow, 200)
		head.open = true
		signed := row(tfSigned, tfScopedBase+"1.0.0.zip?sig=bad", "", allow, 403)
		signed.open = true
		signedHead := row(tfSignedHead, tfScopedBase+"1.0.0.zip?sig=bad", "", allow, 403)
		signedHead.open = true
		read := holds("tf-registry-local", "ns/name/aws/1.0.0.zip", types.ActionRead)
		return []matrixRow{
			discovery,
			head,
			signed,
			signedHead,
			row(
				tfVersions,
				tfModuleBase+"versions",
				"",
				read,
				200,
			),
			row(tfVersionsHead, tfModuleBase+"versions", "", read, 200),
			row(
				tfDownload,
				tfModuleBase+"1.0.0/download",
				"",
				read,
				204,
			),
			row(tfDownloadHead, tfModuleBase+"1.0.0/download", "", read, 204),
			row(
				tfScopedVersions,
				tfScopedBase+"versions",
				"",
				read,
				200,
			),
			row(tfScopedVersionsHead, tfScopedBase+"versions", "", read, 200),
			row(
				tfScopedDownload,
				tfScopedBase+"1.0.0/download",
				"",
				read,
				204,
			),
			row(tfScopedDownloadHead, tfScopedBase+"1.0.0/download", "", read, 204),
		}
	},
}
