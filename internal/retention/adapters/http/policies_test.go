package retentionhttp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
)

type fullPolicy struct{}

func (fullPolicy) Any() bool          { return true }
func (fullPolicy) Allows(string) bool { return true }
func (fullPolicy) Name() string       { return "admin" }

func TestUnitPackagePolicyHTTPStatusesAndWireRoundTrip(t *testing.T) {
	db := inmem.New()
	s := PackagePoliciesAPI{Policies: &cleanup.Service{
		Store: db, Schedules: db.Schedules(), Repositories: db.Repos(), Guard: db.Projects().Guard,
		Authorize: func(ctx context.Context, _ kernel.ScopeActions) (cleanup.Policy, error) {
			return fullPolicy{}, db.Projects().Guard(ctx)
		},
	}}
	ctx := t.Context()
	const raw = `{"key":"cleanup-wire","description":"old packages","durationInMinutes":30,"skipTrashcan":true,"searchCriteria":{"packageTypes":["npm"],"repos":["**"],"includedPackages":["@scope/*"],"excludedRepos":["safe-*"],"excludedPackages":["@scope/keep"],"includeAllProjects":true,"includedProjects":["default"],"includeAllStages":false,"includedStages":["DEV"],"createdBeforeInDays":10,"createdBeforeInMonths":2,"lastDownloadedBeforeInDays":20,"lastDownloadedBeforeInMonths":3,"keepLastNVersions":5,"includedProperties":{"clean":["yes"]},"excludedProperties":{"hold":["yes"]},"includedPackageVersions":["1.*"],"excludedPackageVersions":["1.0"]}}`
	var body artgen.PackageCleanupPolicyRequest
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreatePackageCleanupPolicy(ctx, artgen.CreatePackageCleanupPolicyRequestObject{PolicyKey: body.Key, Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := created.VisitCreatePackageCleanupPolicyResponse(w); err != nil {
		t.Fatal(err)
	}
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"enabled":false`) || strings.Contains(w.Body.String(), "Revision") ||
		strings.Contains(w.Body.String(), "package_types") {
		t.Fatalf("create did not return disabled camelCase wire policy: %d %s", w.Code, w.Body.String())
	}
	var returned artgen.PackageCleanupPolicyRequest
	if err := json.Unmarshal(w.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Cleanup().Policy(ctx, body.Key)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := json.Marshal(stored.Criteria)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(storage), `"created_before_in_days":10`) || !strings.Contains(string(storage), `"include_all_stages":false`) {
		t.Fatalf("storage mapping lost public criteria: %s", storage)
	}
	if policyInput(returned).Criteria.CreatedBeforeInMonths != 2 || policyInput(returned).Criteria.ExcludedProperties["hold"][0] != "yes" {
		t.Fatal("wire round-trip lost legacy/properties criteria")
	}
	get, err := s.GetPackageCleanupPolicy(ctx, artgen.GetPackageCleanupPolicyRequestObject{PolicyKey: body.Key})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	if err := get.VisitGetPackageCleanupPolicyResponse(w); err != nil || w.Code != 200 {
		t.Fatalf("get=%d %v", w.Code, err)
	}
	list, err := s.GetAllPackageCleanupPolicies(ctx, artgen.GetAllPackageCleanupPoliciesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.(artgen.GetAllPackageCleanupPolicies200JSONResponse)) != 1 {
		t.Fatal("list did not return the global policy")
	}
	enabled, err := s.EnableDisablePackageCleanupPolicy(
		ctx,
		artgen.EnableDisablePackageCleanupPolicyRequestObject{PolicyKey: body.Key, Body: &artgen.CleanupPolicyEnablement{Enabled: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	if err := enabled.VisitEnableDisablePackageCleanupPolicyResponse(w); err != nil || w.Code != 200 {
		t.Fatalf("enable=%d %v", w.Code, err)
	}
	body.Enabled = httpkit.PtrTo(false)
	updated, err := s.UpdatePackageCleanupPolicy(ctx, artgen.UpdatePackageCleanupPolicyRequestObject{PolicyKey: body.Key, Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	if !httpkit.Deref(updated.(artgen.UpdatePackageCleanupPolicy200JSONResponse).Enabled) {
		t.Fatal("update disabled policy through ignored enabled field")
	}
	disabled, err := s.DisableAllCleanupPolicies(ctx, artgen.DisableAllCleanupPoliciesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	if err := disabled.VisitDisableAllCleanupPoliciesResponse(w); err != nil || w.Code != 200 {
		t.Fatalf("disable all=%d %v", w.Code, err)
	}
	deleted, err := s.DeletePackageCleanupPolicy(ctx, artgen.DeletePackageCleanupPolicyRequestObject{PolicyKey: body.Key})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	if err := deleted.VisitDeletePackageCleanupPolicyResponse(w); err != nil || w.Code != 200 || !strings.Contains(w.Body.String(), "deleted") {
		t.Fatalf("delete must return 200 confirmation: %d %s %v", w.Code, w.Body.String(), err)
	}
}

func TestUnitPolicyWireProjectionDoesNotAliasStoredCriteria(t *testing.T) {
	p := model.CleanupPolicy{
		Criteria: model.CleanupCriteria{PackageTypes: []string{"npm"}, IncludedProperties: map[string][]string{"keep": {"yes"}}, ExcludedPackages: []string{"keep"}},
	}
	w := policyWire(p)
	w.SearchCriteria.PackageTypes[0] = "docker"
	(*w.SearchCriteria.IncludedProperties)["keep"][0] = "no"
	(*w.SearchCriteria.ExcludedPackages)[0] = "lost"
	if p.Criteria.PackageTypes[0] != "npm" || p.Criteria.IncludedProperties["keep"][0] != "yes" || p.Criteria.ExcludedPackages[0] != "keep" {
		t.Fatal("response aliases stored selection criteria")
	}
}
