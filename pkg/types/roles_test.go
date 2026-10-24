package types

import (
	"slices"
	"testing"
)

func TestUnitGlobalRoleVocabulary(t *testing.T) {
	if len(GlobalRoleActions) != 25 || !IsGlobalRoleAction("READ_POLICIES_SECURITY") ||
		IsGlobalRoleAction("MANAGE_MEMBERS") {
		t.Fatalf("the global vocabulary has the 25 global actions only")
	}
}

func TestUnitBuiltinGlobalRoles(t *testing.T) {
	all := BuiltinGlobalRoles()
	if len(all) != 6 || all[0].Name != "Project Admin" || all[0].Type != RoleAdmin {
		t.Fatalf("built-ins %v", all)
	}
	for _, r := range all {
		if r.Type != RoleAdmin && r.Type != RolePredefined || r.Editable() {
			t.Errorf("%s is %s and editable=%v", r.Name, r.Type, r.Editable())
		}
		for _, a := range r.Actions {
			if !IsGlobalRoleAction(a) {
				t.Errorf("%s grants %s, outside the vocabulary", r.Name, a)
			}
		}
		for _, e := range r.Environments {
			if !IsBuiltinEnvironment(e) {
				t.Errorf("%s applies to %s, not a built-in environment", r.Name, e)
			}
		}
	}
	// Callers get copies.
	all[1].Actions[0] = "CHANGED"
	if r, ok := BuiltinGlobalRole(all[1].Name); !ok || r.Actions[0] == "CHANGED" {
		t.Fatalf("BuiltinGlobalRoles shares its lists: %v", r.Actions)
	}
	if _, ok := BuiltinGlobalRole("ci"); ok {
		t.Fatal("ci is not built in")
	}
}

func TestUnitEnvironmentNames(t *testing.T) {
	if !IsBuiltinEnvironment("dev") || !IsBuiltinEnvironment("PROD") || IsBuiltinEnvironment("QA") {
		t.Fatal("built-in environments compare without regard to case")
	}
	for name, ok := range map[string]bool{
		"QA": true, "stage_2-b": true, "": false, "no spaces": false, "é": false,
		"12345678901234567890123456789012": true, "123456789012345678901234567890123": false,
	} {
		if ValidEnvironmentName(name) != ok {
			t.Errorf("ValidEnvironmentName(%q) != %v", name, ok)
		}
	}
}

func TestUnitRoleActions(t *testing.T) {
	actions := []string{
		"READ_REPOSITORY",
		"DELETE_OVERWRITE_REPOSITORY",
		"DEPLOY_BUILD",
		"TRIGGER_PIPELINE",
	}
	if got := RoleActions(actions, ResourceArtifact); got != ActionRead|ActionDelete {
		t.Errorf("artifact actions %v", got)
	}
	if got := RoleActions(actions, ResourceBuild); got != ActionWrite {
		t.Errorf("build actions %v", got)
	}
	admin, _ := BuiltinGlobalRole("Project Admin")
	want := ActionRead | ActionAnnotate | ActionWrite | ActionDelete | ActionScan
	if got := RoleActions(admin.Actions, ResourceArtifact); got != want {
		t.Errorf("Project Admin on artifacts %v, want %v", got, want)
	}
	if got := RoleActions(slices.Clone(admin.Actions), ResourceReleaseBundle); got != 0 {
		t.Errorf("Release Bundle actions grant nothing here: %v", got)
	}
}

// Developer follows JFrog's one published example: Read and Write in DEV and
// PROD, so artifacts are read, annotated and deployed but not deleted.
func TestUnitDeveloperDefault(t *testing.T) {
	dev, ok := BuiltinGlobalRole("Developer")
	if !ok || !slices.Equal(dev.Environments, []string{EnvironmentDev, EnvironmentProd}) {
		t.Fatalf("Developer %+v", dev)
	}
	if got := RoleActions(
		dev.Actions,
		ResourceArtifact,
	); got != ActionRead|ActionAnnotate|ActionWrite {
		t.Errorf("Developer on artifacts %v", got)
	}
	if got := RoleActions(
		dev.Actions,
		ResourceBuild,
	); got != ActionRead|ActionAnnotate|ActionWrite {
		t.Errorf("Developer on builds %v", got)
	}
}
