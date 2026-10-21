package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
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
		return append(tfbeAPIMatrixRows(), []matrixRow{
			row(tfbeGet, "/artifactory/tfbe-local/"+latest, "",
				holds("tfbe-local", latest, types.ActionRead), 200),
			row(tfbePut, "/artifactory/tfbe-local/workspaces/{p}/state.latest.json", tfbeState(3),
				holds("tfbe-local", "workspaces/x/state.latest.json", types.ActionWrite), 400),
		}...)
	},
}

func tfbeAPIMatrixRows() []matrixRow {
	read := holds("tfbe-local", "workspaces/app", types.ActionRead)
	write := holds("tfbe-local", "workspaces/app/state.latest.json", types.ActionWrite)
	del := holds("tfbe-local", "workspaces/app", types.ActionDelete)
	base := tfbeAPI + "/organizations/tfbe-local"
	ws := tfbeAPI + "/workspaces/tfbe-local__app"
	state := tfbeAPI + "/state-versions/" + tfbeVersion
	ping := at("GET "+tfbeAPI+"/ping", "", noCredentials, 204)
	ping.open = true
	upload := row(
		"PUT "+tfbeAPI+"/state-versions/{id}/content",
		state+"/content?token=bad",
		"",
		noCredentials,
		403,
	)
	upload.open = true
	seed := func(t *testing.T, f *matrixFixture, name string) {
		t.Helper()
		c, err := f.db.Repos().Get(t.Context(), "tfbe-local")
		if err != nil {
			t.Fatal(err)
		}
		svc := terraformstate.Service{Items: f.items, Keys: tfbeKeys(t)}
		if _, err := svc.CreateWorkspace(t.Context(), c, name, "", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	mutate := func(method, suffix string, action rule, status int) matrixRow {
		return row(
			method+" "+tfbeAPI+"/workspaces/{id}"+suffix,
			tfbeAPI+"/workspaces/tfbe-local__{p}"+suffix,
			"",
			action,
			status,
		).seeding(seed)
	}
	orgDelete := row(
		"DELETE "+tfbeAPI+"/organizations/{repo}/workspaces/{name}",
		base+"/workspaces/{p}",
		"",
		del,
		204,
	).seeding(seed)
	orgRead := func(p matrixPrincipal) int {
		// Organization discovery accepts Read anywhere in this repository.
		for _, g := range p.grants {
			if (g.repo == "*" || g.repo == "tfbe-local") && g.actions&types.ActionRead != 0 {
				return 0
			}
		}
		return refuse(p)
	}
	return []matrixRow{
		ping,
		upload,
		at("GET "+tfbeAPI+"/account/details", "", signedIn, 200),
		row("GET "+tfbeAPI+"/organizations/{repo}", base, "", orgRead, 200),
		row(
			"GET "+tfbeAPI+"/organizations/{repo}/entitlement-set",
			base+"/entitlement-set",
			"",
			orgRead,
			200,
		),
		row(
			"GET "+tfbeAPI+"/organizations/{repo}/workspaces",
			base+"/workspaces",
			"",
			orgRead,
			200,
		),
		row(
			"GET "+tfbeAPI+"/organizations/{repo}/workspaces/{name}",
			base+"/workspaces/app",
			"",
			read,
			200,
		),
		row("GET "+tfbeAPI+"/workspaces/{id}", ws, "", read, 200),
		row(
			"GET "+tfbeAPI+"/workspaces/{id}/current-state-version",
			ws+"/current-state-version",
			"",
			read,
			200,
		),
		row("GET "+tfbeAPI+"/state-versions/{id}", state, "", read, 200),
		row("GET "+tfbeAPI+"/state-versions/{id}/download", state+"/download", "", read, 200),
		row(
			"GET "+tfbeAPI+"/state-versions",
			tfbeAPI+"/state-versions?filter[organization][name]=tfbe-local&filter[workspace][name]=app",
			"",
			read,
			200,
		),
		row(
			"POST "+tfbeAPI+"/organizations/{repo}/workspaces",
			base+"/workspaces",
			`{"data":{"type":"workspaces","attributes":{"name":"{p}"}}}`,
			write,
			201,
		),
		row(
			"POST "+tfbeAPI+"/workspaces/{id}/state-versions",
			ws+"/state-versions",
			`{"data":{"type":"state-versions","attributes":{"md5":"00000000000000000000000000000000","serial":3}}}`,
			write,
			201,
		),
		mutate("POST", "/actions/lock", read, 200),
		mutate("POST", "/actions/unlock", read, 409),
		mutate("POST", "/actions/force-unlock", del, 409),
		mutate("DELETE", "", del, 204),
		orgDelete,
	}
}
