package accesshttp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

func TestUnitProjectWireDefaultsAndPartialUpdates(t *testing.T) {
	p := projectFromCreate(accessgen.ProjectCreateRequest{ProjectKey: "alpha", DisplayName: "Alpha"})
	if p.StorageQuotaBytes != -1 || !p.AdminPrivileges.ManageMembers || !p.AdminPrivileges.CreateLocalRepository || !p.AdminPrivileges.DeleteRemoteRepository {
		t.Fatalf("omitted create settings: %+v", p)
	}
	p.Description, p.SoftLimit, p.QuotaEmail = "description", true, true
	p.AdminPrivileges.DeleteLocalRepository = false
	if got := projectFromUpdate(p, accessgen.ProjectUpdateRequest{}); got != p {
		t.Fatalf("empty update changed stored settings: got %+v want %+v", got, p)
	}
	got := projectFromUpdate(p, accessgen.ProjectUpdateRequest{AdminPrivileges: &projectPrivileges{ManageMembers: httpkit.PtrTo(false)}})
	if got.AdminPrivileges.ManageMembers || got.AdminPrivileges.DeleteLocalRepository || !got.AdminPrivileges.CreateRemoteRepository {
		t.Fatalf("members-only update changed repository grants: %+v", got.AdminPrivileges)
	}
	got = projectFromUpdate(p, accessgen.ProjectUpdateRequest{Description: httpkit.PtrTo(""), SoftLimit: httpkit.PtrTo(false)})
	if got.Description != "" || got.SoftLimit || !got.QuotaEmail || got.DisplayName != p.DisplayName {
		t.Fatalf("explicit false/empty values were lost: %+v", got)
	}
	w := projectWire(got)
	if w.ProjectKey == nil || *w.ProjectKey != p.Key || w.Description == nil || *w.Description != "" ||
		w.SoftLimit == nil || *w.SoftLimit || w.AdminPrivileges.DeleteLocalRepository == nil || *w.AdminPrivileges.DeleteLocalRepository {
		t.Fatalf("explicit response projection: %+v", w)
	}
}

func TestUnitProjectWireLegacyAndGranularRepositoryGrants(t *testing.T) {
	b := accessgen.ProjectCreateRequest{ProjectKey: "alpha", DisplayName: "Alpha", AdminPrivileges: &projectPrivileges{
		ManageResources: httpkit.PtrTo(false), ManageRemoteRepository: httpkit.PtrTo(false),
	}}
	p := projectFromCreate(b)
	if p.AdminPrivileges.CreateLocalRepository || p.AdminPrivileges.DeleteVirtualRepository || p.AdminPrivileges.CreateRemoteRepository {
		t.Fatalf("legacy denials were not resolved: %+v", p.AdminPrivileges)
	}
	b.AdminPrivileges.DeleteLocalRepository = httpkit.PtrTo(false)
	p = projectFromCreate(b)
	if !p.AdminPrivileges.CreateLocalRepository || !p.AdminPrivileges.DeleteRemoteRepository || p.AdminPrivileges.DeleteLocalRepository {
		t.Fatalf("granular create used legacy mapping instead of independent defaults: %+v", p.AdminPrivileges)
	}
	got := projectFromUpdate(p, accessgen.ProjectUpdateRequest{AdminPrivileges: &projectPrivileges{
		CreateRemoteRepository: httpkit.PtrTo(false),
	}})
	if got.AdminPrivileges.CreateRemoteRepository || !got.AdminPrivileges.DeleteRemoteRepository || got.AdminPrivileges.DeleteLocalRepository {
		t.Fatalf("granular update reset omitted flags: %+v", got.AdminPrivileges)
	}
	got = projectFromUpdate(got, accessgen.ProjectUpdateRequest{AdminPrivileges: &projectPrivileges{ManageResources: httpkit.PtrTo(false)}})
	if got.AdminPrivileges.CreateLocalRepository || got.AdminPrivileges.DeleteVirtualRepository || !got.AdminPrivileges.DeleteRemoteRepository {
		t.Fatalf("legacy local/virtual update changed unrelated grants: %+v", got.AdminPrivileges)
	}
	if quota := projectWire(model.Project{}).StorageQuotaBytes; quota == nil || *quota != -1 {
		t.Fatalf("unlimited storage response: %v", quota)
	}
}

func TestUnitProjectRoleWireKeepsInternalAdminProtection(t *testing.T) {
	r, ok := model.BuiltinGlobalRole("Project Admin")
	if !ok {
		t.Fatal("missing protected project admin role")
	}
	w, err := projectRoleWire(r)
	if err != nil {
		t.Fatal(err)
	}
	if *w.Type != accessgen.ProjectRoleType(model.RolePredefined) || r.Type != model.RoleAdmin || r.Editable() {
		t.Fatalf("public projection changed internal protection: wire=%+v role=%+v", w, r)
	}
	(*w.Actions)[0] = "changed by response reader"
	if r.Actions[0] == (*w.Actions)[0] {
		t.Fatal("response actions alias the stored role")
	}
	if _, err := projectRoleWire(model.Role{Type: "unknown"}); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("unknown stored type fabricated into a role: %v", err)
	}
}

func TestUnitProjectRoleAndMemberWireDoNotExposeStorageFields(t *testing.T) {
	r, err := projectRoleWire(model.Role{ProjectKey: "private-project", Name: "custom", Type: model.RoleCustom})
	if err != nil {
		t.Fatal(err)
	}
	m := model.ProjectMember{ProjectKey: "private-project", Type: model.PrincipalUser, Name: "Alice"}
	for _, value := range []any{r, projectUserWire(m), projectGroupWire(m)} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "private-project") || strings.Contains(string(body), "ProjectKey") || strings.Contains(string(body), "null") {
			t.Fatalf("wire exposed storage fields or null role lists: %s", body)
		}
	}
}
