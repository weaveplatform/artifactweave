package model_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
)

func TestUnitProjectRoleVocabularySeparatesGlobalAndProjectActions(t *testing.T) {
	for _, action := range model.GlobalRoleActions {
		want := action != "READ_POLICIES_SECURITY"
		if got := model.IsProjectRoleAction(action); got != want {
			t.Errorf("global action %q valid in a project = %v, want %v", action, got, want)
		}
	}
	for _, action := range []string{
		"MANAGE_INTEGRATIONS_PIPELINE", "MANAGE_SOURCES_PIPELINE", "MANAGE_POOLS_PIPELINE",
		"TRIGGER_SECURITY", "ISSUES_SECURITY", "LICENCES_SECURITY", "MANAGE_MEMBERS", "MANAGE_RESOURCES",
	} {
		if !model.IsProjectRoleAction(action) || model.IsGlobalRoleAction(action) {
			t.Errorf("project-only action %q must be accepted only by the project vocabulary", action)
		}
	}
	for _, action := range []string{"", "read_repository", "READ_REPOSITORY ", "LICENSES_SECURITY", "UNKNOWN"} {
		if model.IsProjectRoleAction(action) {
			t.Errorf("invalid project action %q was accepted", action)
		}
	}
	seen := make(map[string]bool, len(model.ProjectRoleActions))
	for _, action := range model.ProjectRoleActions {
		if seen[action] {
			t.Errorf("duplicate project action %q", action)
		}
		seen[action] = true
	}
	if len(seen) != 32 {
		t.Errorf("project action vocabulary has %d distinct entries, want 32", len(seen))
	}
}
