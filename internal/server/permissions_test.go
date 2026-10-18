package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
)

func adminJSON() map[string]string {
	h := basic("admin", "pw")
	h["Content-Type"] = "application/json"
	return h
}

func permDecode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
}

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d: %s", what, rec.Code, want, rec.Body)
	}
}

// TestPermissionAcrossFormats writes a permission in each API and reads it
// back in the other two: one stored record, three views (decision 0006).
func TestPermissionAcrossFormats(t *testing.T) {
	h := statusFixture(t, false)
	admin := adminJSON()

	// v2 create, read as v1 and Access.
	rec := do(
		t,
		h,
		http.MethodPost,
		permV2+"/cross",
		admin,
		`{"repo":{"include-patterns":["org/**"],`+
			`"exclude-patterns":["org/secret/**"],"repositories":["perm-local"],`+
			`"actions":{"users":{"user":["read","write"]},"groups":{"readers":["read"]}}},`+
			`"build":{"repositories":["artifactory-build-info"],"actions":{"users":{"user":["read"]}}}}`,
	)
	mustStatus(t, rec, 201, "v2 create")
	var v1 struct {
		Name, IncludesPattern, ExcludesPattern string
		Repositories                           []string
		Principals                             struct{ Users, Groups map[string][]string }
	}
	rec = do(t, h, http.MethodGet, permV1+"/cross", basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "v1 read")
	permDecode(t, rec, &v1)
	if v1.IncludesPattern != "org/**" || v1.ExcludesPattern != "org/secret/**" ||
		!slices.Equal(v1.Repositories, []string{"perm-local"}) ||
		!slices.Equal(v1.Principals.Users["user"], []string{"r", "w"}) ||
		!slices.Equal(v1.Principals.Groups["readers"], []string{"r"}) {
		t.Fatalf("v1 view %+v", v1)
	}
	var acc struct {
		Name      string
		Resources map[string]struct {
			Actions struct{ Users, Groups map[string][]string }
			Targets map[string]struct {
				IncludePatterns []string `json:"include_patterns"`
				ExcludePatterns []string `json:"exclude_patterns"`
			}
		}
	}
	rec = do(t, h, http.MethodGet, permAcc+"/cross", basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "access read")
	permDecode(t, rec, &acc)
	art := acc.Resources["artifact"]
	if !slices.Equal(art.Actions.Users["user"], []string{"READ", "WRITE"}) ||
		!slices.Equal(art.Targets["perm-local"].ExcludePatterns, []string{"org/secret/**"}) ||
		len(acc.Resources["build"].Targets) != 1 {
		t.Fatalf("access view %+v", acc)
	}

	// A v1 PUT replaces the repository section and keeps build.
	rec = do(t, h, http.MethodPut, permV1+"/cross", admin,
		`{"name":"cross","repositories":["perm-local"],"principals":{"users":{"user":["r"]}}}`)
	mustStatus(t, rec, 200, "v1 replace")
	if !strings.Contains(rec.Body.String(), "updated") {
		t.Fatalf("v1 replace message %s", rec.Body)
	}
	var v2 struct {
		Repo, Build *struct {
			Actions struct{ Users map[string][]string }
		}
	}
	rec = do(t, h, http.MethodGet, permV2+"/cross", basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "v2 read")
	permDecode(t, rec, &v2)
	if v2.Build == nil || !slices.Equal(v2.Repo.Actions.Users["user"], []string{"read"}) {
		t.Fatalf("after a v1 PUT: %s", rec.Body)
	}

	// Access create with a stored-only section, which reads back as written
	// and survives a v2 replace.
	rec = do(t, h, http.MethodPost, permAcc, admin, `{"name":"plat","resources":{`+
		`"artifact":{"actions":{"users":{"user":["READ"]}},"targets":{"perm-local":{"include_patterns":["**"]}}},`+
		`"destination":{"actions":{"users":{"user":["READ"]}},"targets":{"edge-1":{}}}}}`)
	mustStatus(t, rec, 201, "access create")
	if !strings.Contains(rec.Body.String(), `"destination"`) {
		t.Fatalf("create response dropped the destination section: %s", rec.Body)
	}
	rec = do(t, h, http.MethodPut, permV2+"/plat", admin,
		`{"repo":{"repositories":["perm-local"],"actions":{"users":{"user":["read"]}}}}`)
	mustStatus(t, rec, 200, "v2 replace")
	rec = do(t, h, http.MethodGet, permAcc+"/plat", basic("admin", "pw"), "")
	if !strings.Contains(rec.Body.String(), `"destination"`) {
		t.Fatalf("a v2 replace dropped a section v2 cannot carry: %s", rec.Body)
	}
}

// TestSeededPermissionsInEveryFormat reads the seeded Anything and Any
// Remote through each API. Since 7.84.3 neither names anonymous (#383).
func TestSeededPermissionsInEveryFormat(t *testing.T) {
	h := statusFixture(t, false)
	for _, name := range []string{"Anything", "Any%20Remote"} {
		for _, path := range []string{permV1, permV2, permAcc} {
			rec := do(t, h, http.MethodGet, path+"/"+name, basic("admin", "pw"), "")
			mustStatus(t, rec, 200, path+"/"+name)
			if strings.Contains(rec.Body.String(), auth.AnonymousUser) {
				t.Errorf("%s/%s grants anonymous: %s", path, name, rec.Body)
			}
		}
	}
	rec := do(t, h, http.MethodGet, permV1+"/Any%20Remote", basic("admin", "pw"), "")
	if !strings.Contains(rec.Body.String(), `"repositories":["ANY REMOTE"]`) ||
		strings.Contains(rec.Body.String(), "principals") {
		t.Fatalf("Any Remote in v1: %s", rec.Body)
	}
	rec = do(t, h, http.MethodGet, permV1+"/Anything", basic("admin", "pw"), "")
	if !strings.Contains(rec.Body.String(), `"ANY"`) ||
		!strings.Contains(rec.Body.String(), `"readers":["r"]`) {
		t.Fatalf("Anything in v1: %s", rec.Body)
	}
}

func TestLossyPermission(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(
		t,
		h,
		http.MethodPost,
		permAcc,
		adminJSON(),
		`{"name":"lossy","resources":{"artifact":{`+
			`"actions":{"users":{"user":["READ"]}},"targets":{`+
			`"perm-local":{"include_patterns":["a/**"]},"libs-local":{"include_patterns":["b/**"]}}}}}`,
	)
	mustStatus(t, rec, 201, "create")
	rec = do(t, h, http.MethodGet, permV1+"/lossy", basic("admin", "pw"), "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "/access/api/v2/permissions") {
		t.Fatalf("v1 of a lossy permission: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, permV2+"/lossy", basic("admin", "pw"), "")
	mustStatus(t, rec, 400, "v2 of a lossy permission")
	// The name and URI listings still show it, so it is not invisible; the
	// by-principal views carry full v2 documents and leave it out.
	for _, path := range []string{permV1, permV2} {
		rec = do(t, h, http.MethodGet, path, basic("admin", "pw"), "")
		mustStatus(t, rec, 200, path)
		if !strings.Contains(rec.Body.String(), `"name":"lossy"`) ||
			!strings.Contains(rec.Body.String(), path+`/lossy"`) {
			t.Fatalf("%s leaves out a lossy permission: %s", path, rec.Body)
		}
	}
	rec = do(t, h, http.MethodGet, permV2+"/users/user", basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "by user")
	if strings.Contains(rec.Body.String(), "lossy") {
		t.Fatalf("a lossy permission in a v2 by-user view: %s", rec.Body)
	}
}

// TestExpandNarrows checks that an expanded listing shows only the grants
// to the principals asked about (getpermissions.json).
func TestExpandNarrows(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(
		t,
		h,
		http.MethodPost,
		permAcc,
		adminJSON(),
		`{"name":"shared","resources":{"artifact":{`+
			`"actions":{"users":{"user":["READ"],"admin":["MANAGE"]},"groups":{"readers":["READ"]}},`+
			`"targets":{"perm-local":{"include_patterns":["**"]}}}}}`,
	)
	mustStatus(t, rec, 201, "create")
	var page struct {
		Permissions []struct {
			Name      string
			Resources map[string]struct {
				Actions struct{ Users, Groups map[string][]string }
			}
		}
	}
	rec = do(t, h, http.MethodGet, permAcc+"?user=user&expand=true&include_inherited_groups=false",
		basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "expand")
	permDecode(t, rec, &page)
	found := false
	for _, p := range page.Permissions {
		a := p.Resources["artifact"].Actions
		if _, ok := a.Users["admin"]; ok || len(a.Groups) != 0 {
			t.Errorf("%s shows grants to others: %+v", p.Name, a)
		}
		if p.Name == "shared" {
			found = true
			if !slices.Equal(a.Users["user"], []string{"READ"}) {
				t.Errorf("shared narrowed to %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("shared missing from %+v", page)
	}
}

func TestAccessListing(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	var page struct {
		Permissions []struct{ Name, URI string }
		Cursor      string
	}
	var names []string
	cursor := ""
	for range 20 {
		rec := do(t, h, http.MethodGet, permAcc+"?limit=2"+cursor, admin, "")
		mustStatus(t, rec, 200, "page")
		page.Cursor = ""
		permDecode(t, rec, &page)
		for _, p := range page.Permissions {
			names = append(names, p.Name)
		}
		if page.Cursor == "" {
			break
		}
		// The documented cursor is the page's last name, in the clear.
		if last := page.Permissions[len(page.Permissions)-1].Name; page.Cursor != last {
			t.Fatalf("cursor %q after %q", page.Cursor, last)
		}
		cursor = "&cursor=" + url.QueryEscape(page.Cursor)
	}
	if !slices.IsSorted(names) || !slices.Contains(names, "Anything") ||
		!slices.Contains(names, "Any Remote") || !slices.Contains(names, "st-managed") ||
		len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Fatalf("paged names %v", names)
	}
	// Any name is a cursor; the page starts after it.
	rec := do(t, h, http.MethodGet, permAcc+"?limit=1&cursor=Any", admin, "")
	mustStatus(t, rec, 200, "cursor between names")
	permDecode(t, rec, &page)
	if len(page.Permissions) != 1 || page.Permissions[0].Name != "Any Remote" ||
		page.Cursor != "Any Remote" {
		t.Fatalf("page after Any: %+v", page)
	}
	// expand with a user filter: full documents, grants through groups too
	// unless include_inherited_groups is false.
	rec = do(t, h, http.MethodGet, permAcc+"?user=user&expand=true", admin, "")
	mustStatus(t, rec, 200, "expand")
	if !strings.Contains(rec.Body.String(), `"resources"`) {
		t.Fatalf("expanded entries %s", rec.Body)
	}
	rec = do(
		t,
		h,
		http.MethodGet,
		permAcc+"?group=readers&include_inherited_groups=false",
		admin,
		"",
	)
	mustStatus(t, rec, 200, "group filter")
	if !strings.Contains(rec.Body.String(), "Anything") ||
		strings.Contains(rec.Body.String(), "st-managed") {
		t.Fatalf("readers filter %s", rec.Body)
	}
	rec = do(t, h, http.MethodGet, permAcc+"?group=nobody", admin, "")
	mustStatus(t, rec, 404, "unknown group")
}

func TestManageHolderListing(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodGet, permV2, basic("user", "pw"), "")
	mustStatus(t, rec, 200, "list")
	body := rec.Body.String()
	if !strings.Contains(body, "st-managed") || strings.Contains(body, "Anything") {
		t.Fatalf("a Manage holder's listing: %s", body)
	}
	// The by-principal views show it the same permissions.
	rec = do(t, h, http.MethodGet, permV2+"/users/user", basic("user", "pw"), "")
	mustStatus(t, rec, 200, "by user")
	if body = rec.Body.String(); !strings.Contains(body, `"name":"st-managed"`) {
		t.Fatalf("a Manage holder's by-user view: %s", body)
	}
	rec = do(t, h, http.MethodGet, permV2+"/groups/readers", basic("user", "pw"), "")
	mustStatus(t, rec, 200, "by group")
	if body = rec.Body.String(); strings.Contains(body, "Anything") {
		t.Fatalf("a by-group view shows what the caller does not manage: %s", body)
	}
}

// TestManageHolderGrants follows a Manage holder's v2 replaces: it grants
// and revokes only actions it holds in the section, never MANAGE, and an
// administrator still may (exclude-manage, decision 0006).
func TestManageHolderGrants(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodPost, permAcc, adminJSON(), `{"name":"mixed","resources":{`+
		`"artifact":{"actions":{"users":{"user":["READ","WRITE","MANAGE"]}},`+
		`"targets":{"perm-local":{"include_patterns":["**"]}}},`+
		`"build":{"actions":{"users":{"user":["READ"]}},`+
		`"targets":{"artifactory-build-info":{"include_patterns":["**"]}}}}}`)
	mustStatus(t, rec, 201, "create")
	holder := map[string]string{"Content-Type": "application/json"}
	for k, v := range basic("user", "pw") {
		holder[k] = v
	}
	body := func(repo, build string) string {
		return `{"repo":{"repositories":["perm-local"],"actions":{` + repo + `}},` +
			`"build":{"repositories":["artifactory-build-info"],"actions":{` + build + `}}}`
	}
	const (
		keep  = `"users":{"user":["read","write","manage"]}`
		build = `"users":{"user":["read"]}`
	)
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"grant held actions", body(keep+`,"groups":{"readers":["read","write"]}`, build), 200},
		{"revoke a held action", body(keep+`,"groups":{"readers":["read"]}`, build), 200},
		{"grant an action it lacks", body(keep+`,"groups":{"readers":["delete"]}`, build), 403},
		{"grant MANAGE", body(keep+`,"groups":{"readers":["read","manage"]}`, build), 403},
		{"revoke its own MANAGE", body(`"users":{"user":["read","write"]}`, build), 403},
		{
			"grant in a section it does not manage",
			body(keep, build+`,"groups":{"readers":["read"]}`), 403,
		},
	} {
		rec := do(t, h, http.MethodPut, permV2+"/mixed", holder, c.body)
		mustStatus(t, rec, c.want, c.name)
		if c.want == 403 && !strings.Contains(rec.Body.String(), "only actions it holds") {
			t.Errorf("%s: message %s", c.name, rec.Body)
		}
	}
	var acc struct {
		Resources map[string]struct {
			Actions struct{ Users, Groups map[string][]string }
		}
	}
	rec = do(t, h, http.MethodGet, permAcc+"/mixed", basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "read back")
	permDecode(t, rec, &acc)
	art := acc.Resources["artifact"].Actions
	if !slices.Equal(art.Groups["readers"], []string{"READ"}) ||
		!slices.Equal(art.Users["user"], []string{"READ", "WRITE", "MANAGE"}) ||
		len(acc.Resources["build"].Actions.Groups) != 0 {
		t.Fatalf("after the holder's edits: %+v", acc.Resources)
	}
	// An administrator grants MANAGE through the same replace.
	rec = do(t, h, http.MethodPut, permV2+"/mixed", adminJSON(),
		body(keep+`,"groups":{"readers":["read","manage"]}`, build))
	mustStatus(t, rec, 200, "an administrator grants MANAGE")
}

func TestAccessSections(t *testing.T) {
	h := statusFixture(t, false)
	admin := adminJSON()
	rec := do(t, h, http.MethodPatch, permAcc+"/st-managed/artifact", admin,
		`{"actions":{"groups":{"readers":["READ"]}}}`)
	mustStatus(t, rec, 200, "patch")
	var sec struct {
		Actions struct{ Users, Groups map[string][]string }
	}
	permDecode(t, rec, &sec)
	if len(sec.Actions.Users["user"]) == 0 || len(sec.Actions.Groups["readers"]) != 1 {
		t.Fatalf("merge lost or missed a principal: %s", rec.Body)
	}
	rec = do(t, h, http.MethodPut, permAcc+"/st-managed/build", admin,
		`{"actions":{"users":{"user":["READ"]}},"targets":{"artifactory-build-info":{}}}`)
	mustStatus(t, rec, 200, "put creates a section")
	rec = do(t, h, http.MethodGet, permAcc+"/st-managed/build", basic("admin", "pw"), "")
	mustStatus(t, rec, 200, "read the new section")
	rec = do(t, h, http.MethodPut, permAcc+"/st-managed/build", admin,
		`{"targets":{"artifactory-build-info":{},"other":{}}}`)
	mustStatus(t, rec, 400, "two build targets")
	rec = do(t, h, http.MethodPatch, permAcc+"/st-managed/artifact", basic("admin", "pw"), "")
	mustStatus(t, rec, 400, "patch without a body")
	rec = do(t, h, http.MethodPut, permAcc+"/st-managed/artifact", basic("admin", "pw"), "")
	mustStatus(t, rec, 400, "put without a body")
}

// TestTerraformFlows follows the calls the artifactory_permission_target
// and platform_permission resources make.
func TestTerraformFlows(t *testing.T) {
	h := statusFixture(t, false)
	admin := adminJSON()
	steps := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodHead, permV2 + "/tf-pt", "", 404},
		{
			http.MethodPost,
			permV2 + "/tf-pt",
			`{"name":"tf-pt","repo":{"repositories":["perm-local"],"actions":{"users":{"user":["read"]}}}}`,
			201,
		},
		{http.MethodHead, permV2 + "/tf-pt", "", 200},
		{http.MethodGet, permV2 + "/tf-pt", "", 200},
		{
			http.MethodPut,
			permV2 + "/tf-pt",
			`{"name":"tf-pt","repo":{"repositories":["perm-local"],"actions":{"users":{"user":["read","annotate"]}}}}`,
			200,
		},
		{http.MethodDelete, permV2 + "/tf-pt", "", 200},
		{http.MethodHead, permV2 + "/tf-pt", "", 404},

		{
			http.MethodPost,
			permAcc,
			`{"name":"tf-plat","resources":{"artifact":{"actions":{"users":{"user":["READ"]}},"targets":{"perm-local":{"include_patterns":["**"]}}}}}`,
			201,
		},
		{http.MethodGet, permAcc + "/tf-plat", "", 200},
		{
			http.MethodPut,
			permAcc + "/tf-plat/artifact",
			`{"actions":{"users":{"user":["READ","WRITE"]}},"targets":{"perm-local":{"include_patterns":["**"]}}}`,
			200,
		},
		{http.MethodDelete, permAcc + "/tf-plat/artifact", "", 204},
		{http.MethodDelete, permAcc + "/tf-plat", "", 204},
		{http.MethodGet, permAcc + "/tf-plat", "", 404},
	}
	for _, s := range steps {
		rec := do(t, h, s.method, s.path, admin, s.body)
		mustStatus(t, rec, s.want, s.method+" "+s.path)
	}
}

func TestPermissionBodies(t *testing.T) {
	h := statusFixture(t, false)
	vendor := func(mt string) map[string]string {
		hd := basic("admin", "pw")
		hd["Content-Type"] = mt
		return hd
	}
	rec := do(t, h, http.MethodPut, permV1+"/vendor-v1",
		vendor("application/vnd.org.jfrog.artifactory.security.PermissionTarget+json"),
		`{"repositories":["perm-local"]}`)
	mustStatus(t, rec, 200, "v1 vendor body")
	rec = do(t, h, http.MethodPost, permV2+"/vendor-v2",
		vendor("application/vnd.org.jfrog.artifactory.security.PermissionTargetV2+json"),
		`{"repo":{"repositories":["perm-local"]}}`)
	mustStatus(t, rec, 201, "v2 vendor body")
	rec = do(t, h, http.MethodPut, permV2+"/vendor-v2",
		vendor("application/vnd.org.jfrog.artifactory.security.PermissionTargetV2+json"),
		`{"repo":{"repositories":["perm-local"]}}`)
	mustStatus(t, rec, 200, "v2 vendor replace")
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, permV1 + "/nobody"},
		{http.MethodPost, permV2 + "/nobody"},
		{http.MethodPut, permV2 + "/nobody"},
		{http.MethodPost, permAcc},
	} {
		rec := do(t, h, c.method, c.path, basic("admin", "pw"), "")
		mustStatus(t, rec, 400, c.method+" "+c.path+" without a body")
	}
	rec = do(
		t,
		h,
		http.MethodPut,
		permV1+"/named",
		adminJSON(),
		`{"name":"other","repositories":["perm-local"]}`,
	)
	mustStatus(t, rec, 400, "v1 name mismatch")
	rec = do(t, h, http.MethodPut, permV1+"/badletter", adminJSON(),
		`{"repositories":["perm-local"],"principals":{"users":{"user":["q"]}}}`)
	mustStatus(t, rec, 400, "unknown v1 letter")
	rec = do(t, h, http.MethodPost, permV2+"/badaction", adminJSON(),
		`{"repo":{"repositories":["perm-local"],"actions":{"users":{"user":["fly"]}}}}`)
	mustStatus(t, rec, 400, "unknown v2 action")
	rec = do(t, h, http.MethodPost, permAcc, adminJSON(), `{"name":`)
	mustStatus(t, rec, 400, "malformed Access body")
}

// TestPermissionTokenScopes checks that a token's scope, not its user,
// decides: system:permissions:r lists and reads every permission whole but
// writes none, and may not read one section, which is admin only (#382); a
// group-scoped token loses the MANAGE its user holds directly.
func TestPermissionTokenScopes(t *testing.T) {
	h := statusFixture(t, false, allowBasicTokens)
	token := func(scope string) map[string]string {
		created := mintAdmin(t, h, `{"username":"user","scope":"`+scope+`"}`)
		s, _ := created["access_token"].(string)
		return bearerAuth(s)
	}
	reader := token("system:permissions:r")
	mustStatus(t, do(t, h, http.MethodGet, permAcc, reader, ""), 200, "scoped list")
	mustStatus(t, do(t, h, http.MethodGet, permAcc+"/Anything", reader, ""), 200, "scoped read")
	mustStatus(
		t,
		do(t, h, http.MethodGet, permAcc+"/Anything/artifact", reader, ""),
		403,
		"scoped section",
	)
	mustStatus(
		t,
		do(t, h, http.MethodGet, permAcc+"/missing", reader, ""),
		404,
		"scoped read of a missing name",
	)
	mustStatus(
		t,
		do(t, h, http.MethodDelete, permAcc+"/st-managed", reader, ""),
		403,
		"scoped delete",
	)

	mustStatus(
		t,
		do(t, h, http.MethodGet, permV2+"/st-managed", basic("user", "pw"), ""),
		200,
		"manager read",
	)
	groups := token("applied-permissions/groups:readers")
	mustStatus(
		t,
		do(t, h, http.MethodGet, permV2+"/st-managed", groups, ""),
		403,
		"group-scoped read",
	)
}
