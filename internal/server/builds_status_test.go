package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// testStarted is a started timestamp in the layout the JFrog CLI writes.
const testStarted = "2026-10-15T09:30:00.000+0000"

// allowBuilds lets a fixture publish builds without a caller.
type allowBuilds struct{}

func (allowBuilds) Build(string, string, bool, types.Actions) bool { return true }
func (allowBuilds) Item(repo.Config, string, types.Actions) bool   { return true }

// buildDoc is a build-info document of one run without modules.
func buildDoc(name, number, started string) string {
	return fmt.Sprintf(`{"name":%q,"number":%q,"started":%q}`, name, number, started)
}

// seedBuild publishes run number 1 of name, started at testStarted.
func seedBuild(t *testing.T, db store.Store, blobs blob.Store, name string) {
	t.Helper()
	svc := build.Service{Store: db, Blobs: blobs}
	if _, err := svc.Publish(
		context.Background(), "", []byte(buildDoc(name, "1", testStarted)), "admin", allowBuilds{},
	); err != nil {
		t.Fatal(err)
	}
}

// buildStatusRows covers the Builds API (#165). The fixture published run
// 1 of st-app, st-ren, st-del and st-del2 (seedStatusBuilds); the rows that
// change a build each use their own.
func buildStatusRows() []statusCase {
	const (
		all       = "GET /artifactory/api/build"
		publish   = "PUT /artifactory/api/build"
		runs      = "GET /artifactory/api/build/{buildName}"
		info      = "GET /artifactory/api/build/{buildName}/{buildNumber}"
		appendOp  = "POST /artifactory/api/build/append/{buildName}/{buildNumber}"
		rename    = "POST /artifactory/api/build/rename/{buildName}"
		remove    = "DELETE /artifactory/api/build/{buildName}"
		removeAll = "POST /artifactory/api/build/delete"
		artifacts = "GET /artifactory/api/builds/buildArtifacts/{buildName}/{buildNumber}/{buildRepo}"
		b         = "/artifactory/api/build"
		arts      = "/artifactory/api/builds/buildArtifacts"
	)
	doc := buildDoc("st-new", "1", testStarted)
	return []statusCase{
		{all, b, "admin", "", 200},
		{all, b, "user", "", 200},
		{all, b + "?project=default", "anonymous", "", 200},
		{all, b, "", "", 401},

		{publish, b, "admin", doc, 200},
		// The same identity again replaces the run.
		{publish, b, "admin", doc, 200},
		{publish, b, "admin", `{"number":"1","started":"` + testStarted + `"}`, 400},
		{publish, b, "admin", buildDoc("st-new", "1", "yesterday"), 400},
		{publish, b, "admin", `[]`, 400},
		{publish, b + "?project=nope", "admin", doc, 404},
		{publish, b, "user", doc, 403},
		{publish, b, "anonymous", doc, 401},
		{publish, b, "", doc, 401},

		{runs, b + "/st-app", "admin", "", 200},
		{runs, b + "/absent", "admin", "", 404},
		{runs, b + "/st-app", "user", "", 403},
		{runs, b + "/st-app", "anonymous", "", 401},
		{runs, b + "/st-app", "", "", 401},

		{info, b + "/st-app/1", "admin", "", 200},
		{info, b + "/st-app/1?started=2026-10-15T09:30:00.000%2B0000&diff=0", "admin", "", 200},
		{info, b + "/st-app/1?started=soon", "admin", "", 400},
		{info, b + "/st-app/2", "admin", "", 404},
		{info, b + "/st-app/1", "user", "", 403},
		{info, b + "/st-app/1", "anonymous", "", 401},
		{info, b + "/st-app/1", "", "", 401},

		{appendOp, b + "/append/st-app/1", "admin", `[{"id":"more"}]`, 204},
		{appendOp, b + "/append/st-app/1?buildRepo=libs-local", "admin", `[]`, 400},
		{appendOp, b + "/append/st-app/1?started=soon", "admin", `[]`, 400},
		{appendOp, b + "/append/st-app/1?project=nope", "admin", `[]`, 404},
		{appendOp, b + "/append/st-app/2", "admin", `[]`, 404},
		{appendOp, b + "/append/st-app/1", "user", `[]`, 403},
		{appendOp, b + "/append/st-app/1", "anonymous", `[]`, 401},
		{appendOp, b + "/append/st-app/1", "", `[]`, 401},

		{rename, b + "/rename/st-ren?to=st-app", "admin", "", 409},
		{rename, b + "/rename/st-ren?to=", "admin", "", 400},
		{rename, b + "/rename/absent?to=st-x", "admin", "", 404},
		{rename, b + "/rename/st-ren?to=st-x", "user", "", 403},
		{rename, b + "/rename/st-ren?to=st-x", "anonymous", "", 401},
		{rename, b + "/rename/st-ren?to=st-x", "", "", 401},
		{rename, b + "/rename/st-ren?to=st-renamed", "admin", "", 200},

		{remove, b + "/st-del", "admin", "", 400},
		{remove, b + "/absent?buildNumbers=1", "admin", "", 404},
		{remove, b + "/st-del?buildNumbers=1", "user", "", 403},
		{remove, b + "/st-del?buildNumbers=1", "anonymous", "", 401},
		{remove, b + "/st-del?buildNumbers=1", "", "", 401},
		{remove, b + "/st-del?buildNumbers=1&artifacts=0&deleteAll=0", "admin", "", 200},

		{removeAll, b + "/delete", "admin", `{"buildName":"absent","deleteAll":true}`, 404},
		{removeAll, b + "/delete", "admin", `{"buildName":"st-del2"}`, 400},
		{removeAll, b + "/delete", "user", `{"buildName":"st-del2","deleteAll":true}`, 403},
		{removeAll, b + "/delete", "anonymous", `{"buildName":"st-del2","deleteAll":true}`, 401},
		{removeAll, b + "/delete", "", `{"buildName":"st-del2","deleteAll":true}`, 401},
		{removeAll, b + "/delete", "admin", `{"buildName":"st-del2","deleteAll":true}`, 200},

		{artifacts, arts + "/st-app/1/artifactory-build-info", "admin", "", 200},
		{artifacts, arts + "/st-app/1/libs-local", "admin", "", 404},
		{artifacts, arts + "/st-app/2/artifactory-build-info", "admin", "", 404},
		{artifacts, arts + "/st-app/1/artifactory-build-info", "user", "", 403},
		{artifacts, arts + "/st-app/1/artifactory-build-info", "anonymous", "", 401},
		{artifacts, arts + "/st-app/1/artifactory-build-info", "", "", 401},
	}
}

// seedStatusBuilds publishes the runs buildStatusRows act on.
func seedStatusBuilds(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	for _, name := range []string{"st-app", "st-ren", "st-del", "st-del2"} {
		seedBuild(t, db, blobs, name)
	}
}
