package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// tfbeMatrixPart is the Terraform backend's share of the route × principal
// matrix, which #558 extends with the API's routes. A caller who may read a
// state downloads it as stored; one who may write is refused all the same,
// as the item routes write nothing into the repository.
var tfbeMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		t.Helper()
		seedTfbe(t, f.db, f.items.Blobs, tfbeKeys(t), "tfbe-local")
	},
	rows: func() []matrixRow {
		const latest = "workspaces/app/state.latest.json"
		return []matrixRow{
			row(tfbeGet, "/artifactory/tfbe-local/"+latest, "",
				holds("tfbe-local", latest, types.ActionRead), 200),
			row(tfbePut, "/artifactory/tfbe-local/workspaces/{p}/state.latest.json", tfbeState(3),
				holds("tfbe-local", "workspaces/x/state.latest.json", types.ActionWrite), 400),
		}
	},
}
