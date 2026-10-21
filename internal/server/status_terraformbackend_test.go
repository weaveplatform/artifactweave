package server

import (
	"strings"
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
	handwritten: tfbeAPIRoutes,
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		seedTfbe(t, db, blobs, tfbeKeys(t), "tfbe-local")
		seedTfbe(t, db, blobs, tfbeKeys(t), "tfbe-local", "api")
	},
	rows: func() []statusCase {
		const (
			ws      = "/artifactory/tfbe-local/workspaces/app/"
			storage = "/artifactory/api/storage/tfbe-local/workspaces/app"
		)
		return append(tfbeAPIStatusRows(), []statusCase{
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
		}...)
	},
}

const (
	tfbeAPI     = "/artifactory/api/terraform/remote/v2"
	tfbeVersion = "tfbe-local__app__1792576800001"
)

var tfbeAPIRoutes = map[string]bool{
	"GET " + tfbeAPI + "/ping":                                      true,
	"GET " + tfbeAPI + "/account/details":                           true,
	"GET " + tfbeAPI + "/organizations/{repo}":                      true,
	"GET " + tfbeAPI + "/organizations/{repo}/entitlement-set":      true,
	"GET " + tfbeAPI + "/organizations/{repo}/workspaces":           true,
	"POST " + tfbeAPI + "/organizations/{repo}/workspaces":          true,
	"GET " + tfbeAPI + "/organizations/{repo}/workspaces/{name}":    true,
	"DELETE " + tfbeAPI + "/organizations/{repo}/workspaces/{name}": true,
	"GET " + tfbeAPI + "/workspaces/{id}":                           true,
	"DELETE " + tfbeAPI + "/workspaces/{id}":                        true,
	"POST " + tfbeAPI + "/workspaces/{id}/actions/lock":             true,
	"POST " + tfbeAPI + "/workspaces/{id}/actions/unlock":           true,
	"POST " + tfbeAPI + "/workspaces/{id}/actions/force-unlock":     true,
	"GET " + tfbeAPI + "/workspaces/{id}/current-state-version":     true,
	"POST " + tfbeAPI + "/workspaces/{id}/state-versions":           true,
	"GET " + tfbeAPI + "/state-versions":                            true,
	"GET " + tfbeAPI + "/state-versions/{id}":                       true,
	"GET " + tfbeAPI + "/state-versions/{id}/download":              true,
	"PUT " + tfbeAPI + "/state-versions/{id}/content":               true,
}

func tfbeAPIStatusRows() []statusCase {
	base := tfbeAPI + "/organizations/tfbe-local"
	ws := tfbeAPI + "/workspaces/tfbe-local__api"
	state := tfbeAPI + "/state-versions/tfbe-local__api__1792576800001"
	rows := []statusCase{
		{"GET " + tfbeAPI + "/ping", tfbeAPI + "/ping", "", "", 204},
		{"GET " + tfbeAPI + "/ping", tfbeAPI + "/ping", "expired", "", 204},
		{
			"GET " + tfbeAPI + "/account/details",
			tfbeAPI + "/account/details",
			"admin-token",
			"",
			200,
		},
		{"GET " + tfbeAPI + "/account/details", tfbeAPI + "/account/details", "expired", "", 401},
		{"GET " + tfbeAPI + "/account/details", tfbeAPI + "/account/details", "forged", "", 401},
		{"GET " + tfbeAPI + "/account/details", tfbeAPI + "/account/details", "", "", 401},
		{"GET " + tfbeAPI + "/organizations/{repo}", base, "admin", "", 200},
		{
			"GET " + tfbeAPI + "/organizations/{repo}/entitlement-set",
			base + "/entitlement-set",
			"admin",
			"",
			200,
		},
		{
			"GET " + tfbeAPI + "/organizations/{repo}/workspaces",
			base + "/workspaces",
			"admin",
			"",
			200,
		},
		{
			"GET " + tfbeAPI + "/organizations/{repo}/workspaces/{name}",
			base + "/workspaces/api",
			"admin",
			"",
			200,
		},
		{"GET " + tfbeAPI + "/workspaces/{id}", ws, "admin", "", 200},
		{
			"GET " + tfbeAPI + "/workspaces/{id}/current-state-version",
			ws + "/current-state-version",
			"admin",
			"",
			200,
		},
		{"GET " + tfbeAPI + "/state-versions/{id}", state, "admin", "", 200},
		{"GET " + tfbeAPI + "/state-versions/{id}/download", state + "/download", "admin", "", 200},
		{
			"GET " + tfbeAPI + "/state-versions",
			tfbeAPI + "/state-versions?filter[organization][name]=tfbe-local&filter[workspace][name]=api",
			"admin",
			"",
			200,
		},
		{
			"POST " + tfbeAPI + "/organizations/{repo}/workspaces",
			base + "/workspaces",
			"admin",
			`{"data":{"type":"workspaces","attributes":{"name":"new"}}}`,
			201,
		},
		{
			"POST " + tfbeAPI + "/workspaces/{id}/state-versions",
			ws + "/state-versions",
			"admin",
			`{"data":{"type":"state-versions","attributes":{"md5":"00000000000000000000000000000000","serial":3}}}`,
			201,
		},
		{
			"POST " + tfbeAPI + "/workspaces/{id}/actions/lock",
			ws + "/actions/lock",
			"admin",
			"{}",
			200,
		},
		{
			"POST " + tfbeAPI + "/workspaces/{id}/actions/unlock",
			ws + "/actions/unlock",
			"admin",
			"{}",
			200,
		},
		{
			"POST " + tfbeAPI + "/workspaces/{id}/actions/force-unlock",
			ws + "/actions/force-unlock",
			"admin",
			"{}",
			409,
		},
		{"DELETE " + tfbeAPI + "/workspaces/{id}", ws, "admin", "", 204},
		{
			"DELETE " + tfbeAPI + "/organizations/{repo}/workspaces/{name}",
			base + "/workspaces/new",
			"admin",
			"",
			204,
		},
		{
			"PUT " + tfbeAPI + "/state-versions/{id}/content",
			state + "/content?token=bad",
			"",
			"{}",
			403,
		},
	}
	var authRows []statusCase
	for _, r := range rows {
		if !strings.HasPrefix(r.route, "GET ") || strings.HasSuffix(r.route, "/ping") ||
			strings.HasSuffix(r.route, "/account/details") {
			continue
		}
		r.as = "user"
		r.want = 403
		authRows = append(authRows, r)
		r.as = ""
		r.want = 401
		authRows = append(authRows, r)
	}
	return append(authRows, rows...)
}
