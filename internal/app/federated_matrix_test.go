package app

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// federatedMatrix covers the federated repository operations (FED-01):
// administrators only, as 7.161.26's FederatedRepoResource and
// FederatedReplaceUrlResource allow.
func federatedMatrix() []matrixRow {
	const base = "/artifactory/api/federation"
	const urls = `{"oldBaseUrl":"https://old.example","newBaseUrl":"https://new.example"}`
	create := func(prefix string, class repo.Class) seeder {
		return func(t *testing.T, f *matrixFixture, p string) {
			t.Helper()
			c := repo.Config{Key: prefix + p, Class: class, PackageType: repo.Generic}
			if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return []matrixRow{
		row("POST "+base+"/configSync/{repoKey}", base+"/configSync/fsync-{p}", "", administrators, 200).
			seeding(create("fsync-", repo.Federated)),
		row("POST "+base+"/migrate/{repoKey}", base+"/migrate/fmig-{p}", "", administrators, 200).
			seeding(create("fmig-", repo.Local)),
		at("POST "+base+"/convertToLocal", `["fcvt-{p}"]`, administrators, 200).
			seeding(create("fcvt-", repo.Federated)),
		at("POST "+base+"/replaceUrl", urls, administrators, 200),
		at("POST "+base+"/updateMemberUrl", urls, administrators, 200),
	}
}
