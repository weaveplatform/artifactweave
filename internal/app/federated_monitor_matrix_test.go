package app

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
)

func federationMonitorMatrix() []matrixRow {
	const base = "/artifactory/api/federation/"
	result := []matrixRow{}
	for _, suffix := range []string{"state", "inconsistentRepos", "status/mirrorsLag", "status/unavailableMirrors", "status/stateList", "status/stateSummary", "rtfs", "migration/status"} {
		result = append(result, at("GET "+base+suffix, "", administrators, 200))
	}
	create := func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		cfg := repo.Config{Key: "fstatus-" + p, Class: repo.Federated, PackageType: repo.Generic}
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&cfg)); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{base, "/artifactory/api/v2/federation/"} {
		result = append(result, row("GET "+prefix+"status/repo/{repoKey}", prefix+"status/repo/fstatus-{p}", "", administrators, 200).seeding(create))
		result = append(result, row("GET "+prefix+"status/repo/{repoKey}/{federatedKey}", prefix+"status/repo/fstatus-{p}/mirrors", "", administrators, 200).seeding(create))
	}
	for _, refusal := range federationhttp.RTFSRefusals() {
		path := strings.NewReplacer("{repoKey}", "missing", "{fileName}", "missing.json", "{state}", "RUNNING").Replace(refusal.Path)
		r := row(refusal.Method+" "+refusal.Path, path, "not JSON", noCredentials, refusal.Status)
		r.open = true
		result = append(result, r)
	}
	return result
}

func federationMonitorStatusRows() []statusCase {
	const base = "/artifactory/api/federation/"
	result := []statusCase{}
	for _, suffix := range []string{"state", "inconsistentRepos", "status/mirrorsLag", "status/unavailableMirrors", "status/stateList", "status/stateSummary", "rtfs", "migration/status"} {
		route := "GET " + base + suffix
		result = append(
			result,
			statusCase{route, base + suffix, "admin", "", 200},
			statusCase{route, base + suffix, "user", "", 403},
			statusCase{route, base + suffix, "", "", 401},
		)
	}
	for _, prefix := range []string{base, "/artifactory/api/v2/federation/"} {
		route := "GET " + prefix + "status/repo/{repoKey}"
		result = append(
			result,
			statusCase{route, prefix + "status/repo/libs-local", "admin", "", 200},
			statusCase{route, prefix + "status/repo/missing", "admin", "", 404},
			statusCase{route, prefix + "status/repo/missing", "user", "", 403},
			statusCase{route, prefix + "status/repo/missing", "", "", 401},
		)
		route += "/{federatedKey}"
		result = append(
			result,
			statusCase{route, prefix + "status/repo/libs-local/mirrors", "admin", "", 200},
			statusCase{route, prefix + "status/repo/libs-local/binaries", "admin", "", 200},
			statusCase{route, prefix + "status/repo/libs-local/invalid", "admin", "", 400},
			statusCase{route, prefix + "status/repo/missing/invalid", "admin", "", 404},
			statusCase{route, prefix + "status/repo/missing/invalid", "user", "", 403},
			statusCase{route, prefix + "status/repo/missing/invalid", "", "", 401},
		)
	}
	for _, refusal := range federationhttp.RTFSRefusals() {
		path := strings.NewReplacer("{repoKey}", "missing", "{fileName}", "missing.json", "{state}", "RUNNING").Replace(refusal.Path)
		for _, user := range []string{"admin", "user", "", "admin:wrong"} {
			result = append(result, statusCase{refusal.Method + " " + refusal.Path, path, user, "", refusal.Status})
		}
	}
	return result
}
