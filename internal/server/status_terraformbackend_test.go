package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// tfbeRepos is the Terraform backend's fixture repository, tfbe-local: the
// package type is local only (decision 0041).
var tfbeRepos = []repo.Config{
	{Key: "tfbe-local", Class: repo.Local, PackageType: repo.TerraformBackend},
}

// Routes the Terraform backend's storage answers on (#557): the item and
// storage routes, which read its items as stored and write nothing into it
// but deletes and properties other than the lock's.
const (
	tfbePut   = "PUT /artifactory/{repoKey}/{filePath}"
	tfbeGet   = "GET /artifactory/{repoKey}/{filePath}"
	tfbeDel   = "DELETE /artifactory/{repoKey}/{filePath}"
	tfbeProps = "PUT /artifactory/api/storage/{repoKey}/{itemPath}"
)

// tfbeStatusPart is the Terraform backend's share of the status table,
// which #558 extends with the API's routes.
var tfbeStatusPart = statusPart{
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		seedTfbe(t, db, blobs, tfbeKeys(t), "tfbe-local")
	},
	rows: func() []statusCase {
		const (
			ws      = "/artifactory/tfbe-local/workspaces/app/"
			storage = "/artifactory/api/storage/tfbe-local/workspaces/app"
		)
		return []statusCase{
			{tfbeGet, ws + "state.latest.json", "admin", "", 200},
			{tfbeGet, ws + "workspace.json", "admin", "", 200},
			{tfbeGet, ws + "state.latest.json", "user", "", 403},
			{tfbeGet, ws + "state.latest.json", "", "", 401},
			// Nothing is written through the item routes.
			{tfbePut, ws + "state.latest.json", "admin", tfbeState(3), 400},
			{tfbePut, ws + "state.latest.json", "admin", "checksum:" + sha1Hex("fixture"), 400},
			{tfbePut, "/artifactory/tfbe-local/workspaces/new/workspace.json", "admin", "{}", 400},
			{tfbePut, "/artifactory/tfbe-local/workspaces/new/", "admin", "", 400},
			{tfbePut, ws + "state.latest.json", "user", tfbeState(3), 403},
			{tfbePut, ws + "state.latest.json", "", tfbeState(3), 401},
			// The lock's properties are the API's; others may be set.
			{tfbeProps, storage + "?properties=locked.by=mallory&recursive=0", "admin", "", 400},
			{tfbeProps, storage + "?properties=team=infra&recursive=0", "admin", "", 204},
			{tfbeDel, ws + "state.latest.json", "user", "", 403},
			{tfbeDel, ws + "state.latest.json", "admin", "", 204},
		}
	},
}
