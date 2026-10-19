package server

import (
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	permV1  = "/artifactory/api/security/permissions"
	permV2  = "/artifactory/api/v2/security/permissions"
	permAcc = "/access/api/v2/permissions"
)

// seedPermissions adds the permissions the status rows change, one per
// state-changing row, so the rows run in any order against one store.
// "user" holds READ and MANAGE on the st-managed* permissions. Every permission the
// rows create or seed targets perm-local, a repository no other row reads:
// a grant on a shared repository would change other rows' answers.
func seedPermissions(t *testing.T, db store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "perm-local", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	libs := map[string]types.Target{"perm-local": {Include: []string{"**"}}}
	for _, name := range []string{
		"st-del-v1", "st-del-v2", "st-del-access", "st-res-put", "st-res-patch", "st-res-del",
	} {
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {Targets: libs},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"st-managed", "st-managed-put", "st-managed-del", "st-managed-grant", "st-managed-other",
	} {
		if err := db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"user": types.ActionRead | types.ActionManage,
				}},
				Targets: libs,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func permissionStatusRows() []statusCase {
	long := strings.Repeat("n", 65)
	v1Body := `{"repositories":["perm-local"],"principals":{"users":{"user":["r"]}}}`
	v2Body := `{"repo":{"repositories":["perm-local"],"actions":{"users":{"user":["read","manage"]}}}}`
	accBody := func(name string) string {
		return `{"name":"` + name + `","resources":{"artifact":{"actions":{"users":{"user":["READ"]}},` +
			`"targets":{"perm-local":{"include_patterns":["**"]}}}}}`
	}
	section := `{"actions":{"users":{"user":["READ"]}},"targets":{"perm-local":{}}}`
	// managed is a v2 body for an st-managed* permission that keeps "user"'s
	// READ and MANAGE and adds grants.
	managed := func(more string) string {
		return `{"repo":{"repositories":["perm-local"],"actions":{` +
			`"users":{"user":["read","manage"]},` + more + `}}}`
	}
	// grantManage is an Access section that grants MANAGE on st-managed. The
	// v1 and Access writes need an administrator, so a Manage holder cannot
	// grant through them either.
	grantManage := `{"actions":{"groups":{"readers":["READ","MANAGE"]}},"targets":{"perm-local":{}}}`
	return []statusCase{
		// v1
		{"GET " + permV1, permV1, "admin", "", 200},
		{"GET " + permV1, permV1, "user", "", 403},
		{"GET " + permV1, permV1, "", "", 401},
		{"GET " + permV1 + "/{permissionTargetName}", permV1 + "/Anything", "admin", "", 200},
		{"GET " + permV1 + "/{permissionTargetName}", permV1 + "/missing", "admin", "", 404},
		{"GET " + permV1 + "/{permissionTargetName}", permV1 + "/Anything", "user", "", 403},
		{"GET " + permV1 + "/{permissionTargetName}", permV1 + "/Anything", "", "", 401},
		{"PUT " + permV1 + "/{permissionTargetName}", permV1 + "/st-v1-put", "admin", v1Body, 200},
		{
			"PUT " + permV1 + "/{permissionTargetName}", permV1 + "/st-v1-bad", "admin",
			`{"repositories":["nope"]}`, 400,
		},
		{"PUT " + permV1 + "/{permissionTargetName}", permV1 + "/st-v1-x", "user", v1Body, 403},
		{
			"PUT " + permV1 + "/{permissionTargetName}", permV1 + "/st-managed", "user",
			`{"repositories":["perm-local"],` +
				`"principals":{"users":{"user":["r","m"]},"groups":{"readers":["m"]}}}`,
			403,
		},
		{"PUT " + permV1 + "/{permissionTargetName}", permV1 + "/st-v1-x", "", v1Body, 401},
		{"DELETE " + permV1 + "/{permissionTargetName}", permV1 + "/st-del-v1", "admin", "", 200},
		{"DELETE " + permV1 + "/{permissionTargetName}", permV1 + "/missing", "admin", "", 404},
		{"DELETE " + permV1 + "/{permissionTargetName}", permV1 + "/Anything", "user", "", 403},
		{"DELETE " + permV1 + "/{permissionTargetName}", permV1 + "/Anything", "", "", 401},

		// v2
		{"GET " + permV2, permV2, "admin", "", 200},
		{"GET " + permV2, permV2, "user", "", 200},
		{"GET " + permV2, permV2, "", "", 401},
		{"GET " + permV2 + "/groups/{groupName}", permV2 + "/groups/readers", "admin", "", 200},
		// A non-admin sees the permissions it manages that grant the principal.
		{"GET " + permV2 + "/groups/{groupName}", permV2 + "/groups/readers", "user", "", 200},
		{"GET " + permV2 + "/groups/{groupName}", permV2 + "/groups/readers", "", "", 401},
		{"GET " + permV2 + "/users/{userName}", permV2 + "/users/anonymous", "admin", "", 200},
		{"GET " + permV2 + "/users/{userName}", permV2 + "/users/anonymous", "user", "", 200},
		{"GET " + permV2 + "/users/{userName}", permV2 + "/users/anonymous", "", "", 401},
		{"GET " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "admin", "", 200},
		{"GET " + permV2 + "/{permissionTargetName}", permV2 + "/missing", "admin", "", 404},
		{"GET " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed", "user", "", 200},
		{"GET " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "user", "", 403},
		{"GET " + permV2 + "/{permissionTargetName}", permV2 + "/missing", "user", "", 403},
		{"GET " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "", "", 401},
		{"HEAD " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "admin", "", 200},
		{"HEAD " + permV2 + "/{permissionTargetName}", permV2 + "/missing", "admin", "", 404},
		// A missing name answers a non-admin as one it may not see.
		{"HEAD " + permV2 + "/{permissionTargetName}", permV2 + "/missing", "user", "", 403},
		{"HEAD " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "user", "", 403},
		{"HEAD " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "", "", 401},
		{
			"POST " + permV2 + "/{permissionTargetName}",
			permV2 + "/st-v2-post",
			"admin",
			v2Body,
			201,
		},
		{"POST " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "admin", v2Body, 409},
		{
			"POST " + permV2 + "/{permissionTargetName}", permV2 + "/st-v2-bad", "admin",
			`{"repo":{"repositories":["virt"]}}`, 400,
		},
		{"POST " + permV2 + "/{permissionTargetName}", permV2 + "/st-v2-x", "user", v2Body, 403},
		{"POST " + permV2 + "/{permissionTargetName}", permV2 + "/st-v2-x", "", v2Body, 401},
		{"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-v2-put", "admin", v2Body, 200},
		{
			"PUT " + permV2 + "/{permissionTargetName}",
			permV2 + "/st-managed-put",
			"user",
			v2Body,
			200,
		},
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-v2-bad", "admin",
			`{"name":"other","repo":{"repositories":["perm-local"]}}`, 400,
		},
		{"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "user", v2Body, 403},
		// A Manage holder may change actions only, not targets or patterns.
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed",
			"user", `{"repo":{"repositories":["perm-local"],"include-patterns":["a/**"],` +
				`"actions":{"users":{"user":["read","manage"]}}}}`, 403,
		},
		// A Manage holder grants or revokes only actions it holds, and never
		// Manage (exclude-manage, decision 0006).
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed-grant",
			"user", managed(`"groups":{"readers":["read"]}`), 200,
		},
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed",
			"user", managed(`"groups":{"readers":["read","write"]}`), 403,
		},
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed",
			"user", managed(`"groups":{"readers":["read","manage"]}`), 403,
		},
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed", "user",
			`{"repo":{"repositories":["perm-local"],"actions":{"users":{"user":["read"]}}}}`, 403,
		},
		// It may not change its own entry, even within the actions it holds,
		// but may change another user's.
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed", "user",
			`{"repo":{"repositories":["perm-local"],"actions":{"users":{"user":["manage"]}}}}`, 403,
		},
		{
			"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-managed-other",
			"user", `{"repo":{"repositories":["perm-local"],"actions":{"users":{` +
				`"user":["read","manage"],"anonymous":["read"]}}}}`, 200,
		},
		{"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/st-v2-new", "user", v2Body, 403},
		{"PUT " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "", v2Body, 401},
		{"DELETE " + permV2 + "/{permissionTargetName}", permV2 + "/st-del-v2", "admin", "", 200},
		{"DELETE " + permV2 + "/{permissionTargetName}", permV2 + "/missing", "admin", "", 404},
		{
			"DELETE " + permV2 + "/{permissionTargetName}",
			permV2 + "/st-managed-del",
			"user",
			"",
			403,
		},
		{"DELETE " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "user", "", 403},
		{"DELETE " + permV2 + "/{permissionTargetName}", permV2 + "/Anything", "", "", 401},

		// Access v2
		{"GET " + permAcc, permAcc, "admin", "", 200},
		{"GET " + permAcc, permAcc + "?user=user&expand=true", "admin", "", 200},
		{"GET " + permAcc, permAcc + "?limit=0", "admin", "", 400},
		{"GET " + permAcc, permAcc + "?expand=true", "admin", "", 400},
		{"GET " + permAcc, permAcc + "?user=nobody", "admin", "", 404},
		{"GET " + permAcc, permAcc + "?group=nobody", "admin", "", 404},
		{"GET " + permAcc, permAcc + "?cursor=Anything&limit=1", "admin", "", 200},
		{"GET " + permAcc, permAcc, "user", "", 403},
		{"GET " + permAcc, permAcc, "", "", 401},
		{"POST " + permAcc, permAcc, "admin", accBody("st-ac-post"), 201},
		{"POST " + permAcc, permAcc, "admin", accBody("Anything"), 409},
		{
			"POST " + permAcc,
			permAcc,
			"admin",
			`{"name":"st-ac-bad","resources":{"artifact":{"actions":{"users":{"user":["FLY"]}}}}}`,
			400,
		},
		{"POST " + permAcc, permAcc, "user", accBody("st-ac-x"), 403},
		{"POST " + permAcc, permAcc, "", accBody("st-ac-x"), 401},
		{"GET " + permAcc + "/{permissionName}", permAcc + "/Anything", "admin", "", 200},
		{"GET " + permAcc + "/{permissionName}", permAcc + "/missing", "admin", "", 404},
		{"GET " + permAcc + "/{permissionName}", permAcc + "/missing", "user", "", 403},
		{"GET " + permAcc + "/{permissionName}", permAcc + "/" + long, "admin", "", 400},
		// Access details need an administrator or system:permissions:r, not MANAGE.
		{"GET " + permAcc + "/{permissionName}", permAcc + "/st-managed", "user", "", 403},
		{"GET " + permAcc + "/{permissionName}", permAcc + "/Anything", "user", "", 403},
		{"GET " + permAcc + "/{permissionName}", permAcc + "/Anything", "", "", 401},
		{"DELETE " + permAcc + "/{permissionName}", permAcc + "/st-del-access", "admin", "", 204},
		{"DELETE " + permAcc + "/{permissionName}", permAcc + "/missing", "admin", "", 404},
		{"DELETE " + permAcc + "/{permissionName}", permAcc + "/" + long, "admin", "", 400},
		{"DELETE " + permAcc + "/{permissionName}", permAcc + "/Anything", "user", "", 403},
		{"DELETE " + permAcc + "/{permissionName}", permAcc + "/Anything", "", "", 401},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/Anything/artifact",
			"admin",
			"",
			200,
		},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/Anything/build",
			"admin",
			"",
			404,
		},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/missing/artifact",
			"admin",
			"",
			404,
		},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/Anything/widget",
			"admin",
			"",
			400,
		},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/Anything/artifact",
			"user",
			"",
			403,
		},
		// One section is admin only (#382): MANAGE on the permission does
		// not open it, and a missing name is 403 too.
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-managed/artifact",
			"user",
			"",
			403,
		},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/missing/artifact",
			"user",
			"",
			403,
		},
		{
			"GET " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/Anything/artifact",
			"",
			"",
			401,
		},
		{
			"PUT " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-put/artifact",
			"admin",
			section,
			200,
		},
		{
			"PUT " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/missing/artifact",
			"admin",
			section,
			404,
		},
		{
			"PUT " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-put/artifact",
			"admin",
			`{"targets":{"nope":{}}}`,
			400,
		},
		{
			"PUT " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-put/artifact",
			"user",
			section,
			403,
		},
		{
			"PUT " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-managed/artifact",
			"user",
			grantManage,
			403,
		},
		{
			"PUT " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-put/artifact",
			"",
			section,
			401,
		},
		{
			"PATCH " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-patch/artifact",
			"admin",
			section,
			200,
		},
		{
			"PATCH " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-patch/build",
			"admin",
			section,
			404,
		},
		{
			"PATCH " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-patch/artifact",
			"admin",
			`{"actions":{"users":{"user":["FLY"]}}}`,
			400,
		},
		{
			"PATCH " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-patch/artifact",
			"user",
			section,
			403,
		},
		{
			"PATCH " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-managed/artifact",
			"user",
			grantManage,
			403,
		},
		{
			"PATCH " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-patch/artifact",
			"",
			section,
			401,
		},
		{
			"DELETE " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-del/artifact",
			"admin",
			"",
			204,
		},
		{
			"DELETE " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-del/build",
			"admin",
			"",
			404,
		},
		{
			"DELETE " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-del/widget",
			"admin",
			"",
			400,
		},
		{
			"DELETE " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-del/artifact",
			"user",
			"",
			403,
		},
		{
			"DELETE " + permAcc + "/{permissionName}/{resourceType}",
			permAcc + "/st-res-del/artifact",
			"",
			"",
			401,
		},
	}
}
