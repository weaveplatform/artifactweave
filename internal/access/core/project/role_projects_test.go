package project_test

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/access/core/roles"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitProjectRolePagesBindCursorAndRejectOnlyAffectedCollisionPage(t *testing.T) {
	svc, db := world(t)
	ctx := t.Context()
	const name = "global\x1frole"
	must(t, (roles.Service{Store: db}).CreateRole(ctx, model.Role{Name: name, Type: model.RoleCustomGlobal}))
	for _, key := range []string{"alpha", "beta"} {
		must(t, db.Projects().PutMember(ctx, model.ProjectMember{ProjectKey: key, Type: model.PrincipalUser, Name: "alice", Roles: []string{name}}))
	}
	must(t, db.Projects().PutRole(ctx, model.Role{ProjectKey: "beta", Name: name, Type: model.RoleCustom}))
	first, next, err := svc.ProjectsForRole(ctx, name, false, kernel.Page{Limit: 1})
	must(t, err)
	if len(first) != 1 || first[0].Key != "alpha" || next == "" {
		t.Fatal("a later collision invalidated the valid first page")
	}
	rows, cursor, err := svc.ProjectsForRole(ctx, name, false, kernel.Page{Limit: 1, After: next})
	if !errors.Is(err, project.ErrAmbiguousRole) || rows != nil || cursor != "" {
		t.Fatalf("ambiguous page returned partial state: %+v, %q, %v", rows, cursor, err)
	}
	must(t, db.Projects().DeleteRole(ctx, "beta", name))
	rows, cursor, err = svc.ProjectsForRole(ctx, name, false, kernel.Page{Limit: 1, After: next})
	must(t, err)
	if len(rows) != 1 || rows[0].Key != "beta" || cursor != "" {
		t.Fatal("valid second page did not finish the cursor walk")
	}
	rows, cursor, err = svc.ProjectsForRole(ctx, name, false, kernel.Page{})
	must(t, err)
	if len(rows) != 2 || cursor != "" {
		t.Fatal("default page size did not include both matches")
	}
	encode := func(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
	for _, after := range []string{
		"!", kernel.Cursor("wrong-parts"),
		kernel.Cursor(encode(name), "false", "!", encode("alpha")),
		kernel.Cursor(encode(name), "false", encode("alpha"), "!"),
		kernel.Cursor(encode(name), "false", encode("alpha"), ""),
		kernel.Cursor(encode(name), "false", encode("\xff"), encode("alpha")),
		kernel.Cursor(encode(name), "false", encode("alpha"), encode("\xff")),
		kernel.Cursor(encode(name), "false", encode("alpha\x00"), encode("alpha")),
		kernel.Cursor(encode(name), "false", encode("alpha"), encode("alpha\x00")),
	} {
		if _, _, err := svc.ProjectsForRole(ctx, name, false, kernel.Page{Limit: 1, After: after}); !errors.Is(err, project.ErrInvalid) {
			t.Fatalf("malformed cursor was accepted: %v", err)
		}
	}
	if _, _, err := svc.ProjectsForRole(ctx, name, true, kernel.Page{Limit: 1, After: next}); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("cursor changed sort direction: %v", err)
	}
	if _, _, err := svc.ProjectsForRole(ctx, "Viewer", false, kernel.Page{Limit: 1, After: next}); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("cursor changed role: %v", err)
	}
	if _, _, err := svc.ProjectsForRole(ctx, name, false, kernel.Page{Limit: -1}); !errors.Is(err, project.ErrInvalid) {
		t.Fatalf("negative limit: %v", err)
	}
	if _, _, err := svc.ProjectsForRole(ctx, "missing", false, kernel.Page{}); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("missing global role: %v", err)
	}
	for _, fail := range []string{"global role", "local role", "role project page"} {
		broken := project.Service{Store: failingStore{Store: db, fail: fail}}
		rows, next, err := broken.ProjectsForRole(ctx, name, false, kernel.Page{Limit: 1})
		if !errors.Is(err, errStoreFailure) || rows != nil || next != "" {
			t.Fatalf("%s returned partial results: %+v %q %v", fail, rows, next, err)
		}
	}
}
