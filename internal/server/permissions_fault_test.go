package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// permFault fails one PermissionStore method after letting skip calls of it
// through, so a handler's second read can fail after its first succeeded.
type permFault struct {
	op   string
	skip int
}

func (f *permFault) hit(op string) error {
	if f.op != op {
		return nil
	}
	if f.skip > 0 {
		f.skip--
		return nil
	}
	return errBoom
}

type permFaultStore struct {
	*inmem.Store
	f *permFault
}

func (s permFaultStore) Permissions() store.PermissionStore {
	return permFaultPerms{s.Store.Permissions(), s.f}
}

type permFaultPerms struct {
	store.PermissionStore
	f *permFault
}

func (p permFaultPerms) Create(ctx context.Context, perm types.Permission) error {
	if err := p.f.hit("create"); err != nil {
		return err
	}
	return p.PermissionStore.Create(ctx, perm) //nolint:wrapcheck // test double
}

func (p permFaultPerms) Get(ctx context.Context, name string) (types.Permission, error) {
	if err := p.f.hit("get"); err != nil {
		return types.Permission{}, err
	}
	return p.PermissionStore.Get(ctx, name) //nolint:wrapcheck // test double
}

func (p permFaultPerms) Update(ctx context.Context, perm types.Permission) error {
	if err := p.f.hit("update"); err != nil {
		return err
	}
	return p.PermissionStore.Update(ctx, perm) //nolint:wrapcheck // test double
}

func (p permFaultPerms) Delete(ctx context.Context, name string) error {
	if err := p.f.hit("delete"); err != nil {
		return err
	}
	return p.PermissionStore.Delete(ctx, name) //nolint:wrapcheck // test double
}

func (p permFaultPerms) List(
	ctx context.Context,
	pg store.Page,
) ([]types.Permission, string, error) {
	if err := p.f.hit("list"); err != nil {
		return nil, "", err
	}
	return p.PermissionStore.List(ctx, pg) //nolint:wrapcheck // test double
}

func (p permFaultPerms) ForPrincipal(
	ctx context.Context,
	user string,
	groups []string,
) ([]types.Permission, error) {
	if err := p.f.hit("forprincipal"); err != nil {
		return nil, err
	}
	return p.PermissionStore.ForPrincipal(ctx, user, groups) //nolint:wrapcheck // test double
}

// newPermFaultWorld serves a store whose permission index fails on demand.
// Anonymous access is on, so an anonymous request reaches the handlers'
// own 401. "mgr" holds MANAGE on p; empty has no sections; split has
// targets with different patterns, so it has no v1 or v2 view.
func newPermFaultWorld(t *testing.T) (http.Handler, *permFault) {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	if err := db.Settings().Put(ctx, types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	if err := db.Users().Create(ctx, types.User{Name: "mgr", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"perm-local", "perm-other"} {
		if err := db.Repos().
			Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Local})); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []types.Permission{
		{Name: "p", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"mgr": types.ActionRead | types.ActionManage}},
			Targets: map[string]types.Target{"perm-local": {}},
		}}},
		{Name: "empty"},
		{Name: "split", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Targets: map[string]types.Target{
				"perm-local": {Include: []string{"a/**"}},
				"perm-other": {Include: []string{"b/**"}},
			},
		}}},
	} {
		if err := db.Permissions().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	f := &permFault{}
	return New(Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: permFaultStore{Store: db, f: f}, Auth: a,
	}), f
}

func permRequest(
	t *testing.T,
	h http.Handler,
	method, path, as, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	hdr := map[string]string{}
	if as != "" {
		hdr = basic(as, "pw")
	}
	if body != "" {
		hdr["Content-Type"] = "application/json"
	}
	return do(t, h, method, path, hdr, body)
}

// TestPermissionStoreFailures checks that a failing store answers 500 from
// every read and write path, and that nothing internal leaks.
func TestPermissionStoreFailures(t *testing.T) {
	h, f := newPermFaultWorld(t)
	v1Body := `{"repositories":["perm-local"]}`
	v2Body := `{"repo":{"repositories":["perm-local"],"actions":{"users":{"mgr":["read","manage"]}}}}`
	section := `{"targets":{"perm-local":{}}}`
	for _, c := range []struct {
		op           string
		skip         int
		method, path string
		as, body     string
	}{
		// The middleware evaluates the caller's permissions first.
		{"forprincipal", 0, http.MethodGet, permV2, "mgr", ""},
		{"forprincipal", 1, http.MethodGet, permV2 + "/users/mgr", "admin", ""},
		{"list", 0, http.MethodGet, permV1, "admin", ""},
		{"list", 0, http.MethodGet, permV2, "admin", ""},
		{"list", 0, http.MethodGet, permAcc, "admin", ""},
		{"get", 0, http.MethodGet, permV1 + "/p", "admin", ""},
		{"get", 0, http.MethodGet, permV2 + "/p", "admin", ""},
		{"get", 0, http.MethodHead, permV2 + "/p", "admin", ""},
		{"get", 0, http.MethodPut, permV2 + "/p", "admin", v2Body},
		{"get", 0, http.MethodGet, permAcc + "/p", "admin", ""},
		{"get", 0, http.MethodGet, permAcc + "/p/artifact", "admin", ""},
		// The create succeeds; reading the stored document back fails.
		{
			"get", 1, http.MethodPost, permAcc, "admin",
			`{"name":"made","resources":{"artifact":{"targets":{"perm-local":{}}}}}`,
		},
		{"update", 0, http.MethodPut, permV1 + "/p", "admin", v1Body},
		{"update", 0, http.MethodPut, permV2 + "/p", "mgr", v2Body},
		{"update", 0, http.MethodPatch, permAcc + "/p/artifact", "admin", section},
		{"update", 0, http.MethodPut, permAcc + "/p/artifact", "admin", section},
		{"delete", 0, http.MethodDelete, permV1 + "/p", "admin", ""},
		{"delete", 0, http.MethodDelete, permV2 + "/p", "admin", ""},
		{"delete", 0, http.MethodDelete, permAcc + "/p", "admin", ""},
	} {
		*f = permFault{op: c.op, skip: c.skip}
		rec := permRequest(t, h, c.method, c.path, c.as, c.body)
		*f = permFault{}
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s with %s failing: %d %s", c.method, c.path, c.op, rec.Code, rec.Body)
		}
		if c.method != http.MethodHead && rec.Body.String() != "" &&
			!strings.Contains(rec.Body.String(), "internal error") {
			t.Errorf("%s %s: body %s", c.method, c.path, rec.Body)
		}
	}
}

// TestPermissionHandlerEdges covers the answers the status table does not
// reach: the handlers' own 401 for an anonymous caller, 501 without a store,
// and the edges of each family.
func TestPermissionHandlerEdges(t *testing.T) {
	h, _ := newPermFaultWorld(t)
	for _, path := range []string{
		permV1, permV2, permV2 + "/p", permAcc, permAcc + "/p", permAcc + "/p/artifact",
	} {
		if rec := permRequest(
			t,
			h,
			http.MethodGet,
			path,
			"",
			"",
		); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s: %d", path, rec.Code)
		}
	}
	for _, method := range []string{http.MethodHead, http.MethodPut, http.MethodDelete} {
		if rec := permRequest(
			t,
			h,
			method,
			permV2+"/p",
			"",
			`{}`,
		); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s v2: %d", method, rec.Code)
		}
	}
	for _, c := range []struct {
		method, path, as, body string
		want                   int
	}{
		// An unknown principal holds no permissions.
		{http.MethodGet, permV2 + "/users/nobody", "admin", "", 200},
		{http.MethodGet, permV2 + "/groups/nobody", "admin", "", 200},
		// Targets with different patterns have no v2 view.
		{http.MethodGet, permV2 + "/split", "admin", "", 400},
		// Access reads need MANAGE on every section, and empty has none.
		{http.MethodGet, permAcc + "/empty", "mgr", "", 403},
		{http.MethodPost, permAcc, "mgr", `{"name":"x"}`, 403},
		{http.MethodPatch, permAcc + "/p/artifact", "mgr", `{}`, 403},
		{http.MethodPut, permAcc + "/p/artifact", "mgr", `{}`, 403},
		{http.MethodGet, permAcc + "/%20p/artifact", "admin", "", 400},
		{http.MethodPatch, permAcc + "/%20p/artifact", "admin", `{}`, 400},
		{http.MethodPut, permAcc + "/%20p/artifact", "admin", `{}`, 400},
		// Unknown action names and letters are the caller's to fix.
		{http.MethodPut, permAcc + "/p/artifact", "admin", `{"actions":{"users":{"mgr":["BOGUS"]}}}`, 400},
		{http.MethodPut, permV1 + "/p", "admin", `{"principals":{"users":{"mgr":["z"]}}}`, 400},
		// The generated type drops destination; the raw body is decoded too.
		{
			http.MethodPost, permAcc, "admin",
			`{"name":"d","resources":{"destination":{"actions":"READ"}}}`, 400,
		},
	} {
		rec := permRequest(t, h, c.method, c.path, c.as, c.body)
		if rec.Code != c.want {
			t.Errorf(
				"%s %s as %q: %d %s, want %d",
				c.method,
				c.path,
				c.as,
				rec.Code,
				rec.Body,
				c.want,
			)
		}
	}

	db := inmem.New()
	cheapSeed(t, db)
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	bare := New(Config{BaseURL: "https://reg.example", CompatVersion: "7.155.0", Auth: a})
	if rec := permRequest(
		t,
		bare,
		http.MethodGet,
		permV1,
		"admin",
		"",
	); rec.Code != http.StatusNotImplemented {
		t.Errorf("no store: %d", rec.Code)
	}
}

// TestReadAccessWithoutRawBody decodes the generated body when no raw body
// was kept, as for a request that bypassed the middleware.
func TestReadAccessWithoutRawBody(t *testing.T) {
	name := "direct"
	w, err := readAccess(
		context.Background(),
		&accessgen.CreatePermissionJSONRequestBody{Name: name},
	)
	if err != nil || w.Name != name {
		t.Fatalf("readAccess = %+v, %v", w, err)
	}
	if got := after(errBoom, errBoom); got != "boom" {
		t.Errorf("after with a bare sentinel = %q", got)
	}
}

// TestPermissionCallerWithoutMiddleware evaluates permissions itself for a
// principal the middleware did not authorise, and answers 500 when that
// evaluation fails.
func TestPermissionCallerWithoutMiddleware(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	f := &permFault{op: "forprincipal"}
	cfg := Config{Store: permFaultStore{Store: db, f: f}}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true})
	_, err := permissionCaller(ctx, cfg)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Status != http.StatusInternalServerError {
		t.Fatalf("failing evaluation: %v", err)
	}
	*f = permFault{}
	c, err := permissionCaller(ctx, cfg)
	if err != nil || !c.dec.Admin() {
		t.Fatalf("caller = %+v, %v", c, err)
	}
}
