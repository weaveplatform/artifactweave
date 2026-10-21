package terraformbackend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type fixture struct {
	t         *testing.T
	db        *inmem.Store
	items     *item.Service
	keys      *crypt.Keyring
	now       time.Time
	h         http.Handler
	principal func(auth.Principal) auth.Principal
	faults    []error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypt.New(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, keys: keys, now: time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)}
	f.items = &item.Service{
		Store:   db,
		Blobs:   blobs,
		Formats: formats.Settings{TerraformBackend: tfbe.Settings{Keys: keys}}.For,
	}
	for _, c := range []repo.Config{{Key: "states", Class: repo.Local, PackageType: repo.TerraformBackend}, {Key: "states__nested", Class: repo.Local, PackageType: repo.TerraformBackend}, {Key: "other", Class: repo.Local}} {
		if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []types.User{{Name: "admin", Admin: true}, {Name: "alice"}, {Name: "bob"}, {Name: "reader"}, {Name: "writer"}, {Name: "deleter"}, {Name: "scoped"}, {Name: "nobody"}} {
		if err := db.Users().Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	f.grant(
		"all",
		map[string]types.Actions{
			"alice":   types.ActionRead | types.ActionWrite | types.ActionDelete,
			"bob":     types.ActionRead | types.ActionWrite | types.ActionDelete,
			"reader":  types.ActionRead,
			"writer":  types.ActionWrite,
			"deleter": types.ActionDelete,
		},
		"**",
	)
	f.grant("scoped", map[string]types.Actions{"scoped": types.ActionRead}, "workspaces/visible/**")
	f.h = f.router(
		Options{
			Items: f.items,
			Keys:  keys,
			Now:   func() time.Time { return f.now },
			Fault: func(_ *http.Request, err error) { f.faults = append(f.faults, err) },
		},
	)
	return f
}

func (f *fixture) grant(name string, users map[string]types.Actions, path string) {
	f.t.Helper()
	err := f.db.Permissions().
		Create(f.t.Context(), types.Permission{Name: name, Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: users}, Targets: map[string]types.Target{"states": {Include: []string{path}}, "states__nested": {Include: []string{path}}}}}})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) router(o Options) http.Handler {
	r := chi.NewRouter()
	r.Use(baseurl.Middleware("https://registry.test"))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get("X-User")
			if name != "" {
				u, err := f.db.Users().Get(r.Context(), name)
				if err != nil {
					f.t.Fatal(err)
				}
				p := auth.Principal{
					Name:      name,
					Admin:     u.Admin,
					Groups:    u.Groups,
					Anonymous: name == "anonymous",
				}
				if f.principal != nil {
					p = f.principal(p)
				}
				p, err = auth.Authorize(
					r.Context(),
					authz.Sources{Permissions: f.db.Permissions(), Groups: f.db.Groups()},
					p,
				)
				if err != nil {
					f.t.Fatal(err)
				}
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	})
	Register(r, o)
	return r
}

func (f *fixture) do(method, path, user, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-User", user)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("got%d want%d: %s", w.Code, status, w.Body)
	}
	if status >= 400 {
		var body struct {
			Errors []struct{ Status, Title, Detail string }
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Errors) != 1 ||
			body.Errors[0].Status != fmt.Sprint(status) ||
			body.Errors[0].Title != http.StatusText(status) ||
			body.Errors[0].Detail == "" ||
			w.Header().Get("Content-Type") != mediaType {
			t.Fatalf("bad envelope: %s (%v)", w.Body, w.Header())
		}
	}
}

func (f *fixture) create(name string) {
	f.t.Helper()
	expect(
		f.t,
		f.do(
			"POST",
			Prefix+"/organizations/states/workspaces",
			"admin",
			`{"data":{"type":"workspaces","attributes":{"name":"`+name+`","terraform-version":"1.2.9"}}}`,
		),
		201,
	)
}

func stateBody(serial int) string {
	return fmt.Sprintf(
		`{"version":4,"terraform_version":"1.16.4","serial":%d,"lineage":"fixture","outputs":{},"resources":[]}`,
		serial,
	)
}

func stateCreate(body string, serial int, inline bool) string {
	attrs := map[string]any{
		"md5":                tfbe.MD5([]byte(body)),
		"serial":             serial,
		"force":              true,
		"lineage":            "ignored",
		"json-state":         "ignored",
		"json-state-outputs": "ignored",
	}
	if inline {
		attrs["state"] = base64.StdEncoding.EncodeToString([]byte(body))
	}
	b, _ := json.Marshal(
		map[string]any{"data": map[string]any{"type": "state-versions", "attributes": attrs}},
	)
	return string(b)
}

type response struct {
	Data struct {
		ID, Type      string
		Attributes    map[string]any
		Relationships map[string]any
	}
}

func readResponse(t *testing.T, w *httptest.ResponseRecorder) response {
	t.Helper()
	var out response
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) pending(user, body string, serial int) (string, string) {
	f.t.Helper()
	w := f.do(
		"POST",
		Prefix+"/workspaces/states__app/state-versions",
		user,
		stateCreate(body, serial, false),
	)
	expect(f.t, w, 201)
	d := readResponse(f.t, w)
	if d.Data.Attributes["status"] != "pending" {
		f.t.Fatal(d)
	}
	return d.Data.ID, d.Data.Attributes["hosted-state-upload-url"].(string)
}

// Replays the calls captured in research32, including the older inline
// upload and the credential-free upload used by current Terraform.
func TestRemoteBackendSequence(t *testing.T) {
	f := newFixture(t)
	expect(t, f.do("GET", Prefix+"/ping", "", ""), 204)
	expect(t, f.do("GET", Prefix+"/account/details", "alice", ""), 200)
	org := readResponse(t, f.do("GET", Prefix+"/organizations/states", "alice", ""))
	if org.Data.Type != "organizations" || org.Data.ID != "states" {
		t.Fatal(org)
	}
	ent := readResponse(t, f.do("GET", Prefix+"/organizations/states/entitlement-set", "alice", ""))
	if ent.Data.Attributes["operations"] != false || ent.Data.Attributes["state-storage"] != true {
		t.Fatal(ent)
	}
	f.create("app")
	ws := Prefix + "/workspaces/states__app"
	got := f.do("GET", Prefix+"/organizations/states/workspaces/app", "reader", "")
	expect(t, got, 200)
	d := readResponse(t, got)
	if d.Data.Attributes["execution-mode"] != "local" ||
		d.Data.Attributes["terraform-version"] != "1.2.9" ||
		d.Data.Relationships["organization"] == nil {
		t.Fatal(d)
	}
	expect(t, f.do("GET", ws+"/current-state-version", "alice", ""), 404)
	expect(t, f.do("POST", ws+"/actions/lock", "alice", `{"reason":"terraform"}`), 200)
	locked := f.do("POST", ws+"/actions/lock", "bob", "")
	expect(t, locked, 409)
	if !strings.Contains(locked.Body.String(), "locked by User alice") {
		t.Fatal(locked.Body)
	}
	expect(t, f.do("POST", ws+"/actions/unlock", "bob", ""), 409)
	expect(t, f.do("DELETE", Prefix+"/organizations/states/workspaces/app", "bob", ""), 409)
	expect(t, f.do("POST", ws+"/state-versions", "bob", stateCreate(stateBody(1), 1, false)), 409)
	id, upload := f.pending("alice", stateBody(1), 1)
	expect(t, f.do("GET", Prefix+"/state-versions/"+id, "alice", ""), 404)
	expect(t, f.do("PUT", upload, "", stateBody(1)), 200)
	expect(t, f.do("PUT", upload, "", stateBody(1)), 409)
	current := f.do("GET", ws+"/current-state-version", "reader", "")
	expect(t, current, 200)
	state := readResponse(t, current)
	if state.Data.ID != id || state.Data.Attributes["status"] != "finalized" ||
		state.Data.Attributes["serial"] != float64(1) {
		t.Fatal(state)
	}
	downloaded := f.do(
		"GET",
		state.Data.Attributes["hosted-state-download-url"].(string),
		"reader",
		"",
	)
	expect(t, downloaded, 200)
	if downloaded.Body.String() != stateBody(1) {
		t.Fatal(downloaded.Body)
	}
	expect(t, f.do("POST", ws+"/actions/unlock", "alice", ""), 200)
	expect(t, f.do("POST", ws+"/actions/unlock", "alice", ""), 409)
	// No pending version delays unlock, even when an upload never arrives.
	f.now = f.now.Add(time.Millisecond)
	_, _ = f.pending("alice", stateBody(2), 2)
	expect(t, f.do("POST", ws+"/actions/lock", "reader", ""), 200)
	expect(t, f.do("POST", ws+"/actions/force-unlock", "reader", ""), 403)
	expect(t, f.do("POST", ws+"/actions/force-unlock", "deleter", ""), 200)
	expect(t, f.do("POST", ws+"/actions/force-unlock", "deleter", ""), 409)
	// Older client state push writes directly, without Delete or a lock.
	inline := f.do("POST", ws+"/state-versions", "writer", stateCreate(stateBody(2), 2, true))
	expect(t, inline, 201)
	list := f.do(
		"GET",
		Prefix+"/state-versions?filter[organization][name]=states&filter[workspace][name]=app",
		"reader",
		"",
	)
	expect(t, list, 200)
	if !strings.Contains(list.Body.String(), id) ||
		!strings.Contains(list.Body.String(), `"total-count":2`) {
		t.Fatal(list.Body)
	}
	expect(t, f.do("GET", Prefix+"/state-versions/"+id, "reader", ""), 200)
	expect(t, f.do("POST", ws+"/actions/lock", "alice", ""), 200)
	expect(t, f.do("DELETE", Prefix+"/organizations/states/workspaces/app", "alice", ""), 204)
	expect(t, f.do("GET", ws, "alice", ""), 404)
}

func TestWorkspaceListsAndIdentifiers(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"hidden", "visible", "prefix-a", "prefix-b", "with.dot", "with__separator"} {
		f.create(name)
	}
	for _, tc := range []struct {
		query, user      string
		want             int
		contains, absent string
	}{
		{"", "scoped", 200, "visible", "hidden"},
		{"?search[name]=prefix&page[size]=1", "reader", 200, "prefix-a", "prefix-b"},
		{"?search[name]=prefix&page[size]=1&page[number]=2", "reader", 200, "prefix-b", "prefix-a"},
		{"?page[number]=9223372036854775807", "reader", 200, `"data":[]`, "hidden"},
		{"?page[size]=9999", "reader", 200, "hidden", ""},
		{"?page[size]=0", "reader", 400, "Invalid pagination", ""},
		{"?page[number]=x", "reader", 400, "Invalid pagination", ""},
	} {
		w := f.do("GET", Prefix+"/organizations/states/workspaces"+tc.query, tc.user, "")
		expect(t, w, tc.want)
		if !strings.Contains(w.Body.String(), tc.contains) ||
			tc.absent != "" && strings.Contains(w.Body.String(), tc.absent) {
			t.Fatal(w.Body)
		}
	}
	// First existing repository alone is insufficient: the workspace must
	// exist at that split, otherwise the later repository is selected.
	expect(
		t,
		f.do(
			"POST",
			Prefix+"/organizations/states__nested/workspaces",
			"admin",
			`{"data":{"type":"workspaces","attributes":{"name":"app"}}}`,
		),
		201,
	)
	w := f.do("GET", Prefix+"/workspaces/states__nested__app", "admin", "")
	expect(t, w, 200)
	if readResponse(t, w).Data.Attributes["terraform-version"] != "latest" {
		t.Fatal(w.Body)
	}
	expect(t, f.do("DELETE", Prefix+"/workspaces/states__nested__app", "admin", ""), 204)
}

func TestRefusals(t *testing.T) {
	f := newFixture(t)
	f.create("app")
	for _, tc := range []struct {
		method, path, user, body string
		status                   int
	}{
		{"GET", "/account/details", "", "", 401},
		{"GET", "/organizations/states", "nobody", "", 403},
		{"GET", "/organizations/other", "admin", "", 404},
		{"GET", "/organizations/missing", "admin", "", 404},
		{"GET", "/organizations/states/entitlement-set", "nobody", "", 403},
		{"GET", "/organizations/states/workspaces", "nobody", "", 403},
		{"GET", "/workspaces/states__app", "nobody", "", 403},
		{"GET", "/organizations/states/workspaces/missing", "admin", "", 404},
		{"GET", "/workspaces/malformed", "admin", "", 404},
		{"DELETE", "/workspaces/states__app", "reader", "", 403},
		{"POST", "/organizations/missing/workspaces", "admin", "{}", 404},
		{"POST", "/organizations/states/workspaces", "reader", `{"data":{"type":"workspaces","attributes":{"name":"new"}}}`, 403},
		{"POST", "/organizations/states/workspaces", "admin", "{", 400},
		{"POST", "/organizations/states/workspaces", "admin", "{}", 400},
		{"POST", "/organizations/states/workspaces", "admin", `{"data":{"type":"workspaces","attributes":{"name":"app"}}}`, 422},
		{"POST", "/organizations/states/workspaces", "admin", `{"data":{"type":"workspaces","attributes":{"name":"bad/name"}}}`, 422},
		{"POST", "/organizations/states/workspaces", "admin", `{} {}`, 400},
		{"POST", "/organizations/states/workspaces", "admin", `{} rubbish`, 400},
		{"GET", "/state-versions", "admin", "", 400},
		{"GET", "/state-versions?filter[organization][name]=states", "admin", "", 400},
		{"GET", "/state-versions?filter[organization][name]=states&filter[workspace][name]=app", "nobody", "", 403},
		{"GET", "/state-versions?filter[organization][name]=states&filter[workspace][name]=missing", "admin", "", 404},
		{"GET", "/state-versions/bad", "admin", "", 404},
		{"GET", "/state-versions/states__app__123/download", "admin", "", 404},
		{"POST", "/workspaces/states__app/state-versions", "reader", "{}", 403},
		{"POST", "/workspaces/states__app/state-versions", "admin", "{}", 400},
		{"POST", "/workspaces/states__app/state-versions", "admin", "{", 400},
		{"POST", "/workspaces/states__app/state-versions", "admin", `{"data":{"type":"state-versions","attributes":{}}}`, 422},
		{"POST", "/workspaces/states__app/state-versions", "admin", `{"data":{"type":"state-versions","attributes":{"serial":-1,"md5":"bad"}}}`, 422},
		{"PUT", "/state-versions/states__app__123/content", "", "{}", 403},
		{"GET", "/runs", "admin", "", 404},
		{"PATCH", "/workspaces/states__app", "admin", "{}", 404},
		{"POST", "/ping", "admin", "", 404},
	} {
		t.Run(
			tc.method+tc.path+tc.user+tc.body,
			func(t *testing.T) { expect(t, f.do(tc.method, Prefix+tc.path, tc.user, tc.body), tc.status) },
		)
	}
	for _, body := range []string{`{"serial":0}`, `{"serial":3}`} {
		doc := stateCreate(body, 1, true)
		expect(t, f.do("POST", Prefix+"/workspaces/states__app/state-versions", "admin", doc), 422)
	}
	doc := stateCreate("notstate", 0, true)
	expect(t, f.do("POST", Prefix+"/workspaces/states__app/state-versions", "admin", doc), 422)
	doc = strings.Replace(
		stateCreate(stateBody(1), 1, true),
		base64.StdEncoding.EncodeToString([]byte(stateBody(1))),
		"%%%",
		1,
	)
	expect(t, f.do("POST", Prefix+"/workspaces/states__app/state-versions", "admin", doc), 422)
	doc = strings.Replace(
		stateCreate(stateBody(1), 1, true),
		tfbe.MD5([]byte(stateBody(1))),
		strings.Repeat("0", 32),
		1,
	)
	expect(t, f.do("POST", Prefix+"/workspaces/states__app/state-versions", "admin", doc), 422)
}

func TestUploadCapability(t *testing.T) {
	for _, kind := range []string{"bad-signature", "expired", "future", "wrong-id", "lock", "revoked", "disabled", "deleted", "recreated", "wrong-md5", "wrong-serial", "scope"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.create("app")
			id, upload := f.pending("alice", stateBody(1), 1)
			status := 403
			body := stateBody(1)
			switch kind {
			case "bad-signature":
				upload += "x"
			case "expired":
				f.now = f.now.Add(uploadLifetime)
			case "future":
				f.now = f.now.Add(-time.Second)
			case "wrong-id":
				upload = strings.Replace(upload, id, "states__app__1", 1)
			case "lock":
				expect(
					t,
					f.do("POST", Prefix+"/workspaces/states__app/actions/lock", "bob", ""),
					200,
				)
				status = 409
			case "revoked":
				if err := f.db.Permissions().Delete(t.Context(), "all"); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				u, _ := f.db.Users().Get(t.Context(), "alice")
				u.Status = types.UserDisabled
				if err := f.db.Users().Update(t.Context(), u); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := f.db.Users().Delete(t.Context(), "alice"); err != nil {
					t.Fatal(err)
				}
			case "recreated":
				expect(t, f.do("DELETE", Prefix+"/workspaces/states__app", "admin", ""), 204)
				// The new record must invalidate the capability even at the same clock tick.
				f.create("app")
			case "wrong-md5":
				body = stateBody(2)
				status = 422
			case "wrong-serial":
				id, upload = f.pending("alice", stateBody(1), 2)
				status = 422
			case "scope":
				u, _ := url.Parse(upload)
				h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
				cl, _ := h.verify(u.Query().Get("token"), id)
				cl.Scope = new(string)
				if err := hWith(
					f,
				).authorizeUpload(httptest.NewRequest("PUT", upload, nil), mustRepo(t, f), "app", cl); err == nil {
					t.Fatal("empty token scope gained Deploy")
				}
				return
			}
			expect(t, f.do("PUT", upload, "", body), status)
			expect(
				t,
				f.do("GET", Prefix+"/workspaces/states__app/current-state-version", "admin", ""),
				404,
			)
		})
	}
}

func TestUploadCannotCrossWorkspaceRecreationDuringBodyRead(t *testing.T) {
	f := newFixture(t)
	f.create("app")
	_, upload := f.pending("alice", stateBody(1), 1)
	body := strings.NewReader(stateBody(1))
	var once sync.Once
	r := httptest.NewRequest("PUT", upload, readFunc(func(p []byte) (int, error) {
		once.Do(func() {
			// Run after the handler validates the capability but before the
			// storage transaction begins, without timing-dependent goroutines.
			expect(t, f.do("DELETE", Prefix+"/workspaces/states__app", "admin", ""), 204)
			f.create("app")
			f.now = f.now.Add(time.Millisecond)
			expect(t, f.do("POST", Prefix+"/workspaces/states__app/state-versions", "admin",
				stateCreate(stateBody(2), 2, true)), 201)
		})
		return body.Read(p)
	}))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	expect(t, w, 403)
	current := f.do("GET", Prefix+"/workspaces/states__app/current-state-version", "admin", "")
	expect(t, current, 200)
	if got := readResponse(t, current).Data.Attributes["serial"]; got != float64(2) {
		t.Fatalf("recreated workspace's state changed: %v", got)
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func hWith(f *fixture) handler {
	return handler{
		o:      Options{Items: f.items, Keys: f.keys, Now: func() time.Time { return f.now }},
		states: &terraformstate.Service{Items: f.items, Keys: f.keys},
	}
}

func mustRepo(t *testing.T, f *fixture) repo.Config {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), "states")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConcurrentLocksAndUploads(t *testing.T) {
	f := newFixture(t)
	f.create("app")
	run := func(method, path, body string, want map[int]int) {
		t.Helper()
		var group sync.WaitGroup
		codes := make(chan int, 8)
		for range 8 {
			group.Go(func() { codes <- f.do(method, path, "alice", body).Code })
		}
		group.Wait()
		close(codes)
		counts := map[int]int{}
		for code := range codes {
			counts[code]++
		}
		if fmt.Sprint(counts) != fmt.Sprint(want) {
			t.Fatalf("statuses %v want%v", counts, want)
		}
	}
	run("POST", Prefix+"/workspaces/states__app/actions/lock", "", map[int]int{200: 1, 409: 7})
	_, upload := f.pending("alice", stateBody(1), 1)
	run("PUT", upload, stateBody(1), map[int]int{200: 1, 409: 7})
}

func TestBodyLimitsAndDisconnect(t *testing.T) {
	f := newFixture(t)
	f.create("app")
	// This exceeds the old parser bug's 20,000,000-character boundary.
	big := `{"serial":1,"padding":"` + strings.Repeat("x", 21<<20) + `"}`
	_, upload := f.pending("alice", big, 1)
	expect(t, f.do("PUT", upload, "", big), 200)
	// Only JSON management bodies have the 64 MiB limit; ignored fields
	// cannot hide an oversized body after the required fields.
	body := `{"data":{"type":"workspaces","attributes":{"name":"large"}},"padding":"` + strings.Repeat(
		"x",
		bodyLimit,
	) + `"}`
	expect(t, f.do("POST", Prefix+"/organizations/states/workspaces", "admin", body), 413)
	f.now = f.now.Add(time.Millisecond)
	_, upload = f.pending("alice", stateBody(2), 2)
	req := httptest.NewRequest(
		"PUT",
		upload,
		io.MultiReader(strings.NewReader("{"), brokenReader{}),
	)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	expect(t, w, 500)
	if len(f.faults) != 1 {
		t.Fatal(f.faults)
	}
	current := readResponse(
		t,
		f.do("GET", Prefix+"/workspaces/states__app/current-state-version", "admin", ""),
	)
	if current.Data.Attributes["serial"] != float64(1) {
		t.Fatal(current)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestMissingDependenciesAndRouteBoundaries(t *testing.T) {
	f := newFixture(t)
	f.create("app")
	f.h = f.router(Options{Items: f.items})
	expect(
		t,
		f.do(
			"POST",
			Prefix+"/workspaces/states__app/state-versions",
			"alice",
			stateCreate(stateBody(1), 1, false),
		),
		501,
	)
	expect(t, f.do("PUT", Prefix+"/state-versions/states__app__1/content?token=a.b", "", "{}"), 403)
	f.h = f.router(Options{})
	expect(t, f.do("GET", Prefix+"/account/details", "alice", ""), 501)
	expect(t, f.do("GET", Prefix+"/ping", "", ""), 204)
	for _, path := range []string{Prefix, Prefix + "/ping", Prefix + "/state-versions/a/content", "/other", Prefix + "other"} {
		want := strings.HasPrefix(path, Prefix) && path != Prefix+"other"
		if IsPath(path) != want {
			t.Fatal(path)
		}
	}
	for _, tc := range []struct {
		method, path string
		skip         bool
	}{{"GET", Prefix + "/ping", true}, {"PUT", Prefix + "/state-versions/a/content", true}, {"GET", Prefix + "/state-versions/a/content", false}, {"PUT", Prefix + "/state-versions/a/content/extra", false}, {"PUT", Prefix + "/state-versions//content", false}, {"PUT", Prefix + "/account/details?token=x", false}} {
		if SkipsAuthentication(httptest.NewRequest(tc.method, tc.path, nil)) != tc.skip {
			t.Fatal(tc)
		}
	}
}

type fixedToken struct{ token types.Token }

func (v fixedToken) Verify(context.Context, string) (types.Token, error) { return v.token, nil }

func TestCapabilityPreservesTokenIdentityScopeAndExpiry(t *testing.T) {
	for _, kind := range []string{"scoped", "named-group", "transient", "synthetic", "transient-stored"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.create("app")
			name, scope, identity := "alice", "artifact:states/workspaces/app/state.latest.json:w", types.TokenIdentity(
				"",
			)
			if kind != "scoped" {
				scope = "applied-permissions/groups:backend"
				if err := f.db.Groups().
					Create(t.Context(), types.Group{Name: "backend", AutoJoin: true}); err != nil {
					t.Fatal(err)
				}
				if err := f.db.Permissions().
					Create(t.Context(), types.Permission{Name: "backend-group", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Targets: map[string]types.Target{"states": {}}, Actions: types.Grants{Groups: map[string]types.Actions{"backend": types.ActionWrite}}}}}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "transient" {
				name = "ghost"
				identity = types.IdentityTransient
				scope = "applied-permissions/user"
			}
			if kind == "transient-stored" {
				identity = types.IdentityTransient
			}
			if kind == "synthetic" {
				name = "ghost"
				identity = types.IdentitySynthetic
			}
			parsed, err := types.ParseScope(scope)
			if err != nil {
				t.Fatal(err)
			}
			a, err := auth.NewAuthenticator(
				f.db.Users(),
				f.db.Settings(),
				auth.Options{
					Groups: f.db.Groups(),
					Hash:   auth.HashParams{MemoryKiB: 64, Time: 1, Threads: 1},
					Tokens: fixedToken{
						types.Token{
							ID:        "original-token",
							Subject:   "jfac@fixture/users/" + name,
							Scope:     parsed,
							Identity:  identity,
							ExpiresAt: f.now.Add(30 * time.Second),
						},
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.Token(t.Context(), "token", "")
			if err != nil {
				t.Fatal(err)
			}
			f.principal = func(auth.Principal) auth.Principal { return p }
			id, upload := f.pending("alice", stateBody(1), 1)
			u, _ := url.Parse(upload)
			cl, valid := hWith(f).verify(u.Query().Get("token"), id)
			expectedIdentity := identity
			if kind == "transient-stored" {
				expectedIdentity = types.IdentityStored
			}
			if !valid || cl.Scope == nil || *cl.Scope != scope ||
				cl.Expires != f.now.Add(30*time.Second).Unix() ||
				cl.Identity != expectedIdentity {
				t.Fatalf("capability widened token: %+v", cl)
			}
			if kind == "transient-stored" {
				if err := f.db.Users().Delete(t.Context(), "alice"); err != nil {
					t.Fatal(err)
				}
				expect(t, f.do("PUT", upload, "", stateBody(1)), 403)
				return
			}
			expect(t, f.do("PUT", upload, "", stateBody(1)), 200)
			f.now = f.now.Add(time.Millisecond)
			_, upload = f.pending("alice", stateBody(2), 2)
			f.now = f.now.Add(30 * time.Second)
			expect(t, f.do("PUT", upload, "", stateBody(2)), 403)
		})
	}
}
