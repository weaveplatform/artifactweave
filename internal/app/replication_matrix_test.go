package app

import (
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
)

// Current own/foreign project administrators and scoped JWT/reference tokens
// have their additional matrix in test/server/federation.
func replicationMatrix() []matrixRow {
	const base = "/artifactory/api/replications"
	const member = base + "/{repoKey}"
	const mine = base + "/rep-{p}"
	const body = `{"url":"https://target.example/repository"}`
	seed := func(t *testing.T, f *matrixFixture, principal string) {
		t.Helper()
		key := "rep-" + principal
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic})); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Replications().Put(t.Context(), model.Replication{
			RepoKey: key, Key: key + "-target", Revision: "private-matrix-revision",
			URL: "https://target.example/repository", Enabled: true, SyncProperties: true, SocketTimeout: 15 * time.Second,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return []matrixRow{
		row("GET "+member, mine, "", administrators, 200).seeding(seed),
		row("PUT "+member, mine, body, administrators, 201).seeding(seed),
		row("POST "+member, mine, `{"enabled":false}`, administrators, 200).seeding(seed),
		row("DELETE "+member, mine, "", administrators, 200).seeding(seed),
		at("GET /artifactory/api/system/replications", "", administrators, 200),
		at("POST /artifactory/api/system/replications/block", "", administrators, 200),
		at("POST /artifactory/api/system/replications/unblock", "", administrators, 200),
	}
}
