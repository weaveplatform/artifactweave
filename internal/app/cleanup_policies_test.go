package app

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

func cleanupPolicyMatrix() []matrixRow {
	const base = "/artifactory/api/cleanup/packages/policies"
	const one = base + "/{policyKey}"
	const mine = base + "/cleanup-{p}"
	const criteria = `"searchCriteria":{"packageTypes":["npm"],"repos":["**"],"includedPackages":["**"]}`
	seed := func(t *testing.T, f *matrixFixture, principal string) {
		t.Helper()
		if err := f.db.Cleanup().
			CreatePolicy(t.Context(), model.CleanupPolicy{Key: "cleanup-" + principal, ItemType: model.CleanupPackage, Criteria: model.CleanupCriteria{PackageTypes: []string{"npm"}, Repos: []string{"**"}, IncludedPackages: []string{"**"}}}); err != nil {
			t.Fatal(err)
		}
	}
	return []matrixRow{
		at("GET "+base, "", administrators, 200),
		row("POST "+one, base+"/cleanup-new-{p}", `{"key":"cleanup-new-{p}",`+criteria+`}`, administrators, 201),
		row("GET "+one, mine, "", administrators, 200).seeding(seed),
		row("PUT "+one, mine, `{"key":"cleanup-{p}",`+criteria+`}`, administrators, 200).seeding(seed),
		row("DELETE "+one, mine, "", administrators, 200).seeding(seed),
		row("POST "+one+"/enablement", mine+"/enablement", `{"enabled":true}`, administrators, 200).seeding(seed),
		at("PUT /artifactory/api/cleanup/policies/forceDisableAll", "", administrators, 200).seeding(seed),
		row("PUT "+one+"/run", base+"/missing-{p}/run", "", administrators, 404),
		row("PUT /artifactory/api/cleanup/policies/runs/{runId}", "/artifactory/api/cleanup/policies/runs/999999", `{"action":"stop"}`, administrators, 404),
		at("PUT /artifactory/api/cleanup/policies/runs/forceStopAll", "", administrators, 202),
	}
}

func cleanupPolicyStatusRows() []statusCase {
	const base = "/artifactory/api/cleanup/packages/policies"
	const one = base + "/{policyKey}"
	const path = base + "/cleanup-status"
	const body = `{"key":"cleanup-status","searchCriteria":{"packageTypes":["npm"],"repos":["**"],"includedPackages":["**"]}}`
	return []statusCase{
		{"POST " + one, path, "admin", body, 201},
		{"POST " + one, path, "admin", body, 409},
		{"POST " + one, path, "admin", `{"key":"cleanup-status","enabled":true,"searchCriteria":{"packageTypes":["npm"],"repos":["**"],"includedPackages":["**"]}}`, 400},
		{"GET " + one, path, "admin", "", 200},
		{"GET " + base, base, "admin", "", 200},
		{"GET " + one, path, "user", "", 403},
		{"GET " + one, path, "", "", 401},
		{"GET " + one, base + "/missing-policy", "admin", "", 404},
		{"PUT " + one, path, "admin", body, 200},
		{"PUT " + one + "/run", path + "/run", "admin", "", 400},
		{"PUT " + one + "/run", base + "/missing-policy/run", "admin", "", 404},
		{"PUT " + one + "/run", path + "/run", "user", "", 403},
		{"PUT " + one + "/run", path + "/run", "", "", 401},
		{"PUT /artifactory/api/cleanup/policies/runs/{runId}", "/artifactory/api/cleanup/policies/runs/999999", "admin", `{"action":"stop"}`, 404},
		{"PUT /artifactory/api/cleanup/policies/runs/{runId}", "/artifactory/api/cleanup/policies/runs/0", "admin", `{"action":"stop"}`, 400},
		{"PUT /artifactory/api/cleanup/policies/runs/{runId}", "/artifactory/api/cleanup/policies/runs/999999", "user", `{"action":"stop"}`, 403},
		{"PUT /artifactory/api/cleanup/policies/runs/{runId}", "/artifactory/api/cleanup/policies/runs/999999", "", `{"action":"stop"}`, 401},
		{"PUT /artifactory/api/cleanup/policies/runs/forceStopAll", "/artifactory/api/cleanup/policies/runs/forceStopAll", "admin", "", 202},
		{"PUT /artifactory/api/cleanup/policies/runs/forceStopAll", "/artifactory/api/cleanup/policies/runs/forceStopAll", "user", "", 403},
		{"PUT /artifactory/api/cleanup/policies/runs/forceStopAll", "/artifactory/api/cleanup/policies/runs/forceStopAll", "", "", 401},
		{"POST " + one + "/enablement", path + "/enablement", "admin", `{"enabled":true}`, 200},
		{"PUT /artifactory/api/cleanup/policies/forceDisableAll", "/artifactory/api/cleanup/policies/forceDisableAll", "admin", "", 200},
		{"DELETE " + one, path, "admin", "", 200},
		{"DELETE " + one, path, "admin", "", 404},
	}
}
