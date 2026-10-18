package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// repoWorld is a seeded store behind the router: admin and "reader" (in the
// readers group, so the seeded Anything permission lets it read every
// repository) both use password "pw".
type repoWorld struct {
	t  *testing.T
	h  http.Handler
	db *inmem.Store
	// fail makes the store's repository index fail the named operations.
	fail map[string]bool
}

func newRepoWorld(t *testing.T) *repoWorld {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	reader := types.User{
		Name:         "reader",
		PasswordHash: cheapHash,
		Groups:       []string{auth.ReadersGroup},
	}
	if err := db.Users().Create(ctx, reader); err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(db.Users(), db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	w := &repoWorld{t: t, db: db, fail: map[string]bool{}}
	w.h = New(Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: faultyStore{Store: db, fail: w.fail}, Auth: a,
	})
	return w
}

func (w *repoWorld) create(cs ...repo.Config) {
	w.t.Helper()
	for _, c := range cs {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *repoWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	h := map[string]string{}
	if as != "" {
		h = basic(as, "pw")
	}
	if body != "" {
		h["Content-Type"] = "application/json"
	}
	for i := 0; i+1 < len(headers); i += 2 {
		h[headers[i]] = headers[i+1]
	}
	return do(w.t, w.h, method, path, h, body)
}

func (w *repoWorld) stored(key string) repo.Config {
	w.t.Helper()
	c, err := w.db.Repos().Get(context.Background(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// faultyStore fails the repository operations named in fail with errBoom.
type faultyStore struct {
	*inmem.Store
	fail map[string]bool
}

var errBoom = errors.New("boom")

func (f faultyStore) Repos() store.RepoStore {
	return faultyRepos{RepoStore: f.Store.Repos(), fail: f.fail}
}

func (f faultyStore) Permissions() store.PermissionStore {
	if f.fail["permissions"] {
		return failingPermissionStore{f.Store.Permissions()}
	}
	return f.Store.Permissions()
}

type failingPermissionStore struct{ store.PermissionStore }

func (failingPermissionStore) ForPrincipal(
	context.Context,
	string,
	[]string,
) ([]types.Permission, error) {
	return nil, errBoom
}

func (f faultyRepos) Including(ctx context.Context, key string) ([]string, error) {
	if f.fail["including"] {
		return nil, errBoom
	}
	return f.RepoStore.Including(ctx, key) //nolint:wrapcheck // test double
}

type faultyRepos struct {
	store.RepoStore
	fail map[string]bool
}

func (f faultyRepos) List(ctx context.Context, rf store.RepoFilter) ([]repo.Config, error) {
	if f.fail["list"] {
		return nil, errBoom
	}
	return f.RepoStore.List(ctx, rf) //nolint:wrapcheck // test double
}

// Get fails for every key under "get", or for one key under "get:<key>".
func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail["get"] || f.fail["get:"+key] {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

func (f faultyRepos) Create(ctx context.Context, c repo.Config) error {
	if f.fail["create"] {
		return errBoom
	}
	return f.RepoStore.Create(ctx, c) //nolint:wrapcheck // test double
}

func (f faultyRepos) Update(ctx context.Context, c repo.Config) error {
	if f.fail["update"] {
		return errBoom
	}
	return f.RepoStore.Update(ctx, c) //nolint:wrapcheck // test double
}

func (f faultyRepos) Delete(ctx context.Context, key string) error {
	if f.fail["delete"] {
		return errBoom
	}
	return f.RepoStore.Delete(ctx, key) //nolint:wrapcheck // test double
}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
}

func TestListRepositories(t *testing.T) {
	w := newRepoWorld(t)
	w.create(
		repo.Config{
			Key:         "maven-local",
			Class:       repo.Local,
			PackageType: repo.Maven,
			Description: "jars",
		},
		repo.Config{
			Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM, URL: "https://r.example",
			ProjectKey: "proj", Environments: []string{"DEV"},
		},
	)
	type entry struct {
		Key, Type, PackageType, URL, Description string
	}
	var got []entry
	rec := w.do("GET", "/artifactory/api/repositories?type=local&packageType=MAVEN", "admin", "")
	jsonBody(t, rec, &got)
	if rec.Code != 200 || len(got) != 1 || got[0] != (entry{
		Key: "maven-local", Type: "LOCAL", PackageType: "Maven",
		URL: "https://reg.example/artifactory/maven-local", Description: "jars",
	}) {
		t.Fatalf("filtered list %d %+v", rec.Code, got)
	}
	// The list spells each type as Artifactory's RepoType names it, rpm as
	// YUM, and a filter matches either spelling without case.
	w.create(repo.Config{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM})
	for _, filter := range []string{"rpm", "yum", "YUM"} {
		jsonBody(
			t,
			w.do("GET", "/artifactory/api/repositories?packageType="+filter, "admin", ""),
			&got,
		)
		if len(got) != 1 || got[0].Key != "rpm-local" || got[0].PackageType != "YUM" {
			t.Fatalf("packageType=%s: %+v", filter, got)
		}
	}
	jsonBody(
		t,
		w.do("GET", "/artifactory/api/repositories?packageType=BuildInfo", "admin", ""),
		&got,
	)
	if len(got) != 1 || got[0].Key != "artifactory-build-info" ||
		got[0].PackageType != "BuildInfo" {
		t.Fatalf("build-info: %+v", got)
	}
	jsonBody(t, w.do("GET", "/artifactory/api/repositories?project=proj", "admin", ""), &got)
	if len(got) != 1 || got[0].Key != "npm-remote" {
		t.Fatalf("project filter %+v", got)
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/repositories?type=distribution",
		"admin",
		"",
	); rec.Body.String() != "[]\n" {
		t.Fatalf("distribution %s", rec.Body)
	}
	rec = w.do("GET", "/artifactory/api/repositories", "reader", "", "Accept", mediaDetails)
	jsonBody(t, rec, &got)
	if rec.Header().Get("Content-Type") != mediaDetails || len(got) != 4 {
		t.Fatalf("reader with vendor type: %s %+v", rec.Header().Get("Content-Type"), got)
	}
	w.fail["list"] = true
	if rec := w.do("GET", "/artifactory/api/repositories", "admin", ""); rec.Code != 500 ||
		strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("store failure %d %s", rec.Code, rec.Body)
	}
}

func TestGetRepository(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{
		Key: "up", Class: repo.Remote, URL: "https://up.example", Username: "u", Password: "secret",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{{Name: "X-Key", Value: "k", Sensitive: true}},
	})
	rec := w.do("GET", "/artifactory/api/repositories/up", "admin", "", "Accept", mediaRemote)
	var full map[string]any
	jsonBody(t, rec, &full)
	if rec.Header().Get("Content-Type") != mediaRemote || full["username"] != "u" ||
		full["password"] != nil || !strings.Contains(rec.Body.String(), `"value":""`) {
		t.Fatalf("admin view %s %s", rec.Header().Get("Content-Type"), rec.Body)
	}
	var public map[string]any
	jsonBody(t, w.do("GET", "/artifactory/api/repositories/up", "reader", ""), &public)
	if public["key"] != "up" || public["url"] != nil || public["username"] != nil ||
		public["description"] != "" || public["notes"] != "" {
		t.Fatalf("reader view %v", public)
	}
	// v2 keeps only the fields of the package type: generic has no Maven
	// fields, which v1 fills by default. Both send an empty description,
	// notes and property sets rather than leaving them out.
	w.create(repo.Config{Key: "gen", Class: repo.Local})
	var v1, v2 map[string]any
	jsonBody(t, w.do("GET", "/artifactory/api/repositories/gen", "admin", ""), &v1)
	jsonBody(t, w.do("GET", "/artifactory/api/v2/repositories/gen", "admin", ""), &v2)
	if v1["handleReleases"] == nil || v2["handleReleases"] != nil || v2["key"] != "gen" ||
		v1["checksumPolicyType"] == nil || v2["checksumPolicyType"] != nil ||
		v1["forceMetadataNameVersion"] == nil || v2["forceMetadataNameVersion"] != nil {
		t.Fatalf("v1 %v\nv2 %v", v1, v2)
	}
	for name, doc := range map[string]map[string]any{"v1": v1, "v2": v2} {
		if sets, ok := doc["propertySets"].([]any); !ok || len(sets) != 0 ||
			doc["description"] != "" || doc["notes"] != "" {
			t.Fatalf("%s empty fields %v", name, doc)
		}
	}
	for class, media := range map[repo.Class]string{
		repo.Local: mediaLocal, repo.Remote: mediaRemote, repo.Virtual: mediaVirtual,
		repo.Federated: mediaFederated,
	} {
		key := "m-" + string(class)
		c := repo.Config{
			Key:          key,
			Class:        class,
			URL:          "https://u.example",
			Repositories: []string{"gen"},
		}
		w.create(c)
		for _, path := range []string{"/artifactory/api/repositories/", "/artifactory/api/v2/repositories/"} {
			if rec := w.do(
				"GET",
				path+key,
				"admin",
				"",
				"Accept",
				media,
			); rec.Header().
				Get("Content-Type") !=
				media {
				t.Errorf("%s%s: %s", path, key, rec.Header().Get("Content-Type"))
			}
		}
	}
	w.fail["get"] = true
	if rec := w.do("GET", "/artifactory/api/repositories/gen", "admin", ""); rec.Code != 500 {
		t.Fatalf("store failure %d", rec.Code)
	}
}

func TestAllConfigurationsAndExistence(t *testing.T) {
	w := newRepoWorld(t)
	w.create(
		repo.Config{Key: "a", Class: repo.Local, PackageType: repo.NPM},
		repo.Config{Key: "b", Class: repo.Remote, URL: "https://u.example", Password: "secret"},
		repo.Config{Key: "c", Class: repo.Virtual, Repositories: []string{"a"}},
		repo.Config{Key: "d", Class: repo.Federated},
	)
	var groups map[string][]map[string]any
	rec := w.do(
		"GET",
		"/artifactory/api/repositories/configurations",
		"admin",
		"",
		"Accept",
		mediaConfigs,
	)
	jsonBody(t, rec, &groups)
	// The seeded build-info repository is local as well.
	if rec.Header().Get("Content-Type") != mediaConfigs || len(groups["LOCAL"]) != 2 ||
		len(groups["REMOTE"]) != 1 || groups["REMOTE"][0]["password"] != nil ||
		len(
			groups["VIRTUAL"],
		) != 1 || len(groups["FEDERATED"]) != 1 || groups["RELEASE_BUNDLE"] == nil {
		t.Fatalf("groups %s", rec.Body)
	}
	jsonBody(
		t,
		w.do(
			"GET",
			"/artifactory/api/repositories/configurations?packageType=npm&repoType=local",
			"admin",
			"",
		),
		&groups,
	)
	if len(groups["LOCAL"]) != 1 || len(groups["REMOTE"]) != 0 {
		t.Fatalf("filtered groups %v", groups)
	}

	type existence struct {
		Exists            bool
		MatchingRepoTypes []string
		ProjectKey        *string
	}
	var e existence
	jsonBody(
		t,
		w.do(
			"GET",
			"/artifactory/api/repositories/existence?type=virtual&type=releaseBundle",
			"admin",
			"",
		),
		&e,
	)
	if !e.Exists || !slices.Equal(e.MatchingRepoTypes, []string{"VIRTUAL", "RELEASE_BUNDLE"}) {
		t.Fatalf("existence %+v", e)
	}
	jsonBody(
		t,
		w.do("GET", "/artifactory/api/repositories/existence?projectKey=none", "admin", ""),
		&e,
	)
	if e.Exists || len(e.MatchingRepoTypes) != 4 || e.ProjectKey == nil || *e.ProjectKey != "none" {
		t.Fatalf("existence in a project %+v", e)
	}
	w.fail["list"] = true
	for _, path := range []string{
		"/artifactory/api/repositories/configurations", "/artifactory/api/repositories/existence",
	} {
		if rec := w.do("GET", path, "admin", ""); rec.Code != 500 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

func TestSharedRemoteConfiguration(t *testing.T) {
	w := newRepoWorld(t)
	shared := true
	w.create(repo.Config{
		Key: "up", Class: repo.Remote, URL: "https://up.example", Username: "u", Password: "p",
		ShareConfiguration: &shared,
	})
	var got map[string]any
	rec := w.do(
		"GET",
		"/artifactory/api/repositories/up/configuration",
		"reader",
		"",
		"Accept",
		mediaShared,
	)
	jsonBody(t, rec, &got)
	if rec.Header().Get("Content-Type") != mediaShared || got["key"] != "up" ||
		got["url"] != "https://up.example" || got["username"] != nil || got["packageType"] != "generic" {
		t.Fatalf("shared view %s", rec.Body)
	}
	jsonBody(t, w.do("GET", "/artifactory/api/repositories/up/configuration", "admin", ""), &got)
	if got["username"] != "u" || got["password"] != nil {
		t.Fatalf("admin view %v", got)
	}
	w.fail["get"] = true
	if rec := w.do(
		"GET",
		"/artifactory/api/repositories/up/configuration",
		"admin",
		"",
	); rec.Code != 500 {
		t.Fatalf("store failure %d", rec.Code)
	}
}

func TestBatchRead(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "a", Class: repo.Local}, repo.Config{Key: "b", Class: repo.Local})
	var got map[string]map[string]any
	rec := w.do(
		"GET",
		"/artifactory/api/v2/repositories/batch?names=a,missing&names=b&names=a",
		"reader",
		"",
	)
	jsonBody(t, rec, &got)
	// JFrog's batchByName sample sends the empty description and notes.
	if len(got) != 2 || got["a"]["key"] != "a" || got["a"]["handleReleases"] != nil ||
		got["a"]["description"] != "" || got["a"]["notes"] != "" {
		t.Fatalf("batch %s", rec.Body)
	}
	names := make([]string, 0, repoBatchLimit+1)
	for i := range repoBatchLimit + 1 {
		names = append(
			names,
			"r"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26)),
		)
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/v2/repositories/batch?names="+strings.Join(names, ","),
		"admin",
		"",
	); rec.Code != 400 {
		t.Fatalf("over the limit %d", rec.Code)
	}
	w.fail["get"] = true
	if rec := w.do(
		"GET",
		"/artifactory/api/v2/repositories/batch?names=a",
		"admin",
		"",
	); rec.Code != 500 {
		t.Fatalf("store failure %d", rec.Code)
	}
}

func TestCreateRepository(t *testing.T) {
	w := newRepoWorld(t)
	rec := w.do("PUT", "/artifactory/api/repositories/from-path", "admin",
		`{"key":"from-body","rclass":"local","packageType":"npm"}`)
	if rec.Code != 200 || rec.Body.String() != "Successfully created repository 'from-path'" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("create %d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	c := w.stored("from-path")
	if c.PackageType != repo.NPM || c.IncludesPattern == nil || *c.IncludesPattern != "**/*" {
		t.Fatalf("stored %+v", c)
	}
	// A vendor media type is accepted as the body type.
	if rec := w.do("PUT", "/artifactory/api/repositories/vendor", "admin", `{"rclass":"local"}`,
		"Content-Type", mediaLocal); rec.Code != 200 {
		t.Fatalf("vendor body %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("PUT", "/artifactory/api/repositories/virt", "admin",
		`{"rclass":"virtual","repositories":["from-path"]}`); rec.Code != 200 {
		t.Fatalf("virtual %d %s", rec.Code, rec.Body)
	}
	var e struct {
		Errors []struct{ Message string }
	}
	jsonBody(
		t,
		w.do("PUT", "/artifactory/api/repositories/FROM-PATH", "admin", `{"rclass":"local"}`),
		&e,
	)
	if e.Errors[0].Message != msgKeyExists {
		t.Fatalf("exists %+v", e)
	}
	if rec := w.do("PUT", "/artifactory/api/repositories/nobody", "admin", ""); rec.Code != 400 {
		t.Fatalf("no body %d %s", rec.Code, rec.Body)
	}
	w.fail["create"] = true
	if rec := w.do(
		"PUT",
		"/artifactory/api/repositories/x",
		"admin",
		`{"rclass":"local"}`,
	); rec.Code != 500 {
		t.Fatalf("store failure %d", rec.Code)
	}
	w.fail["list"] = true
	if rec := w.do(
		"PUT",
		"/artifactory/api/repositories/y",
		"admin",
		`{"rclass":"local"}`,
	); rec.Code != 500 {
		t.Fatalf("list failure %d", rec.Code)
	}
}

func TestUpdateRepositoryMerges(t *testing.T) {
	w := newRepoWorld(t)
	inc := "libs/**"
	w.create(repo.Config{
		Key: "up", Class: repo.Remote, URL: "https://up.example", Description: "keep me",
		Notes: "old", IncludesPattern: &inc, Password: "secret",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{{Name: "X-Key", Value: "k", Sensitive: true}},
	})
	// Read the masked configuration and write it back with one change: the
	// secrets survive, absent fields keep their values, null resets.
	var doc map[string]any
	jsonBody(t, w.do("GET", "/artifactory/api/repositories/up", "admin", ""), &doc)
	doc["notes"] = "new"
	doc["includesPattern"] = nil
	delete(doc, "description")
	body, _ := json.Marshal(doc)
	rec := w.do("POST", "/artifactory/api/repositories/up", "admin", string(body))
	if rec.Code != 200 || rec.Body.String() != "Successfully updated repository 'up'" {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	c := w.stored("up")
	if c.Notes != "new" || c.Description != "keep me" || *c.IncludesPattern != "**/*" ||
		c.Password != "secret" || c.CustomHTTPHeaders[0].Value != "k" {
		t.Fatalf("merged %+v", c)
	}
	for name, patch := range map[string]string{
		"key":          `{"key":"other"}`,
		"package type": `{"packageType":"maven"}`,
		"class":        `{"rclass":42}`,
		"not json":     `{`,
		"not object":   `[]`,
		"invalid":      `{"url":null}`,
		"wrong type":   `{"description":7}`,
	} {
		if rec := w.do(
			"POST",
			"/artifactory/api/repositories/up",
			"admin",
			patch,
		); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A null immutable field is ignored rather than reset.
	if rec := w.do(
		"POST",
		"/artifactory/api/repositories/up",
		"admin",
		`{"rclass":null}`,
	); rec.Code != 200 {
		t.Fatalf("null class %d %s", rec.Code, rec.Body)
	}
	w.create(repo.Config{Key: "v", Class: repo.Virtual, Repositories: []string{"up"}})
	if rec := w.do(
		"POST",
		"/artifactory/api/repositories/v",
		"admin",
		`{"repositories":["ghost"]}`,
	); rec.Code != 400 {
		t.Fatalf("virtual member %d", rec.Code)
	}
	w.fail["update"] = true
	if rec := w.do(
		"POST",
		"/artifactory/api/repositories/up",
		"admin",
		`{"notes":"x"}`,
	); rec.Code != 500 {
		t.Fatalf("store failure %d", rec.Code)
	}
	w.fail["get"] = true
	if rec := w.do(
		"POST",
		"/artifactory/api/repositories/up",
		"admin",
		`{"notes":"x"}`,
	); rec.Code != 500 {
		t.Fatalf("get failure %d", rec.Code)
	}
}

func TestBatchCreateAndUpdate(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "existing", Class: repo.Local})
	// One bad entry leaves the whole batch unapplied.
	if rec := w.do("PUT", "/artifactory/api/v2/repositories/batch", "admin",
		`[{"key":"ok","rclass":"local"},{"key":"bad","rclass":"remote"}]`); rec.Code != 400 {
		t.Fatalf("bad batch %d", rec.Code)
	}
	if _, err := w.db.Repos().Get(context.Background(), "ok"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a rejected batch created a repository: %v", err)
	}
	for name, body := range map[string]string{
		"no key":          `[{"rclass":"local"}]`,
		"unknown member":  `[{"key":"vv","rclass":"virtual","repositories":["ghost"]}]`,
		"not a list":      `{}`,
		"repeated in one": `[{"key":"dup","rclass":"local"},{"key":"DUP","rclass":"local"}]`,
	} {
		if rec := w.do(
			"PUT",
			"/artifactory/api/v2/repositories/batch",
			"admin",
			body,
		); rec.Code/100 != 4 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A virtual repository may list a member created in the same batch.
	rec := w.do(
		"PUT",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		`[{"key":"m1","rclass":"local"},{"key":"vm","rclass":"virtual","repositories":["m1","existing"]}]`,
	)
	if rec.Code != 201 || rec.Body.String() !=
		"Successfully created repository 'm1'\nSuccessfully created repository 'vm'" {
		t.Fatalf("batch %d %q", rec.Code, rec.Body)
	}
	many := make([]string, repoBatchLimit+1)
	for i := range many {
		many[i] = `{"key":"k` + strings.Repeat("x", i) + `","rclass":"local"}`
	}
	if rec := w.do(
		"PUT",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		"["+strings.Join(many, ",")+"]",
	); rec.Code != 400 {
		t.Fatalf("over the limit %d", rec.Code)
	}

	rec = w.do("POST", "/artifactory/api/v2/repositories/batch", "admin",
		`[{"key":"m1","notes":"one"},{"key":"existing","notes":"two"}]`)
	if rec.Code != 200 || w.stored("m1").Notes != "one" || w.stored("existing").Notes != "two" {
		t.Fatalf("batch update %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"invalid entry":  `[{"key":"m1","rclass":"remote"}]`,
		"unknown member": `[{"key":"vm","repositories":["ghost"]}]`,
		"not a list":     `{}`,
		"empty":          `[]`,
	} {
		if rec := w.do(
			"POST",
			"/artifactory/api/v2/repositories/batch",
			"admin",
			body,
		); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	w.fail["update"] = true
	if rec := w.do(
		"POST",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		`[{"key":"m1"}]`,
	); rec.Code != 500 {
		t.Fatalf("update failure %d", rec.Code)
	}
	w.fail["create"] = true
	if rec := w.do(
		"PUT",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		`[{"key":"n1","rclass":"local"}]`,
	); rec.Code != 500 {
		t.Fatalf("create failure %d", rec.Code)
	}
	w.fail["get"] = true
	if rec := w.do(
		"POST",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		`[{"key":"m1"}]`,
	); rec.Code != 500 {
		t.Fatalf("get failure %d", rec.Code)
	}
	w.fail["list"] = true
	if rec := w.do(
		"PUT",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		`[{"key":"n2","rclass":"local"}]`,
	); rec.Code != 500 {
		t.Fatalf("list failure %d", rec.Code)
	}
}

// seedContent puts one file in key (in its cache when cached) referencing a
// blob, and a package.
func seedContent(t *testing.T, db *inmem.Store, key string, cached bool) {
	t.Helper()
	ctx := context.Background()
	sha := strings.Repeat("ab", 32)
	if _, err := db.Blobs().
		Ref(ctx, types.Blob{Checksums: types.Checksums{SHA256: sha}, Size: 3}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Items().Put(ctx, types.Item{
		RepoKey: key, Cached: cached, Name: "f.txt", Size: 3,
		Actual: types.Checksums{SHA256: sha},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Packages().
		Ensure(ctx, types.Package{RepoKey: key, Type: repo.Generic, Name: "p"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRepository(t *testing.T) {
	w := newRepoWorld(t)
	ctx := context.Background()
	w.create(
		repo.Config{Key: "up", Class: repo.Remote, URL: "https://up.example"},
		repo.Config{Key: "lib", Class: repo.Local},
		repo.Config{
			Key: "v", Class: repo.Virtual, Repositories: []string{"up", "lib"},
			DefaultDeploymentRepo: "lib",
		},
	)
	seedContent(t, w.db, "up", true)
	seedContent(t, w.db, "lib", false)
	var report struct {
		RepoKey               string
		StatusMsg             string
		DeletedArtifactsCount int64
		Success               bool
	}
	rec := w.do("DELETE", "/artifactory/api/repositories/up", "admin", "")
	jsonBody(t, rec, &report)
	if rec.Code != 200 || !report.Success || report.DeletedArtifactsCount != 1 ||
		report.StatusMsg != "Repository 'up' and all its content have been removed successfully." {
		t.Fatalf("delete %d %+v", rec.Code, report)
	}
	if _, err := w.db.Items().Get(ctx, "up", true, "f.txt"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the cache survived: %v", err)
	}
	if pkgs, _, _ := w.db.Packages().List(ctx, "up", store.Page{}); len(pkgs) != 0 {
		t.Fatalf("packages survived: %v", pkgs)
	}
	// lib still references the blob; deleting lib drops the last reference.
	if v := w.stored("v"); !slices.Equal(v.Repositories, []string{"lib"}) {
		t.Fatalf("virtual members %v", v.Repositories)
	}
	if rec := w.do("DELETE", "/artifactory/api/repositories/lib", "admin", ""); rec.Code != 200 {
		t.Fatalf("delete lib %d", rec.Code)
	}
	b, err := w.db.Blobs().Get(ctx, strings.Repeat("ab", 32))
	if err != nil || b.Refs != 0 {
		t.Fatalf("blob %+v %v", b, err)
	}
	if v := w.stored("v"); len(v.Repositories) != 0 || v.DefaultDeploymentRepo != "" {
		t.Fatalf("virtual %+v", v)
	}
	var failure struct {
		StatusMsg string
		Success   bool
		Errors    []any
	}
	jsonBody(t, w.do("DELETE", "/artifactory/api/repositories/v", "reader", ""), &failure)
	if failure.Success || failure.Errors == nil || failure.StatusMsg !=
		"Cannot delete repository: 'v', Reason: User: ('reader') has insufficient permission to delete repositories: v" {
		t.Fatalf("forbidden %+v", failure)
	}
	jsonBody(
		t,
		w.do("DELETE", "/artifactory/api/repositories/"+types.BuildInfoRepository, "admin", ""),
		&failure,
	)
	if failure.StatusMsg != msgProtected {
		t.Fatalf("protected %+v", failure)
	}
	w.fail["delete"] = true
	if rec := w.do("DELETE", "/artifactory/api/repositories/v", "admin", ""); rec.Code != 500 ||
		strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("store failure %d %s", rec.Code, rec.Body)
	}
	w.fail["get"] = true
	if rec := w.do("DELETE", "/artifactory/api/repositories/v", "admin", ""); rec.Code != 500 {
		t.Fatalf("get failure %d", rec.Code)
	}
}

func TestBatchDelete(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "a", Class: repo.Local}, repo.Config{Key: "b", Class: repo.Local})
	var report struct {
		Reports       []map[string]any
		StatusMessage string
	}
	rec := w.do("DELETE", "/artifactory/api/v2/repositories/batch", "admin", `["a","missing"]`)
	jsonBody(t, rec, &report)
	if rec.Code != 200 || report.StatusMessage != msgAllRemoved || len(report.Reports) != 2 ||
		report.Reports[1]["statusMsg"] != "Cannot delete repository: 'missing', repository config does not exist" {
		t.Fatalf("batch %d %s", rec.Code, rec.Body)
	}
	var failFast struct{ StatusMessage string }
	rec = w.do("DELETE", "/artifactory/api/v2/repositories/batch", "reader", `["b"]`)
	jsonBody(t, rec, &failFast)
	if rec.Code != 403 || !strings.Contains(failFast.StatusMessage, "('reader')") {
		t.Fatalf("forbidden %d %s", rec.Code, rec.Body)
	}
	// Every delete failing answers 500; some failing answers 207.
	w.fail["delete"] = true
	jsonBody(t, w.do("DELETE", "/artifactory/api/v2/repositories/batch", "admin", `["b"]`), &report)
	if report.StatusMessage != "No repository could be removed" {
		t.Fatalf("all failed %+v", report)
	}
	rec = w.do("DELETE", "/artifactory/api/v2/repositories/batch", "admin", `["b","missing"]`)
	if rec.Code != 207 {
		t.Fatalf("mixed %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"DELETE",
		"/artifactory/api/v2/repositories/batch",
		"admin",
		"",
	); rec.Code != 400 {
		t.Fatalf("no body %d", rec.Code)
	}
}

func TestRepositoriesWithoutStore(t *testing.T) {
	// A router built without a store answers like the stubs it replaces.
	h := testHandler()
	for _, path := range []string{
		"/artifactory/api/repositories", "/artifactory/api/repositories/x",
		"/artifactory/api/repositories/x/configuration", "/artifactory/api/v2/repositories/x",
		"/artifactory/api/v2/repositories/batch?names=x",
	} {
		if rec := do(t, h, "GET", path, nil, ""); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Name: "admin", Admin: true})
	svc := artifactoryService{}
	if _, err := svc.content(); err == nil {
		t.Fatal("content without a store")
	}
	if _, err := svc.GetAllRepositoryConfigurations(ctx, artgenAllConfigs()); err == nil {
		t.Fatal("configurations without a store")
	}
}

func TestRequestDetailsReadFailure(t *testing.T) {
	h := requestDetails(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("reached the handler")
	}))
	req := httptest.NewRequest(
		"POST",
		"/artifactory/api/repositories/x",
		io.NopCloser(failingReader{}),
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	for path, want := range map[string]bool{
		"/artifactory/api/repositories/x":          true,
		"/artifactory/api/v2/repositories/batch":   true,
		"/artifactory/api/repositories/":           false,
		"/artifactory/api/repositories/x/y":        false,
		"/artifactory/api/repositories/existence2": true,
	} {
		if got := isRepositoryUpdate(path); got != want {
			t.Errorf("%s: %v", path, got)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errBoom }

func artgenAllConfigs() artgen.GetAllRepositoryConfigurationsRequestObject {
	return artgen.GetAllRepositoryConfigurationsRequestObject{}
}

// TestHandlersCalledDirectly covers what the router always supplies: a
// principal, and the raw update body requestDetails saves.
func TestHandlersCalledDirectly(t *testing.T) {
	db := inmem.New()
	ctx := context.Background()
	if err := db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "a", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: db}}
	if _, err := svc.GetRepositoriesByTypeAndProject(
		ctx,
		artgen.GetRepositoriesByTypeAndProjectRequestObject{},
	); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("no principal: %v", err)
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{Name: "admin", Admin: true})
	// Without the saved body the decoded one is used, which cannot express
	// a null reset but still merges.
	if _, err := svc.UpdateRepositoryConfiguration(
		admin,
		artgen.UpdateRepositoryConfigurationRequestObject{
			RepoKey: "a", JSONBody: &repo.Config{Key: "a", Class: repo.Local, Notes: "direct"},
		},
	); err != nil {
		t.Fatal(err)
	}
	// The re-encoded body always carries notes, so the batch repeats it.
	if _, err := svc.UpdateMultipleRepositories(
		admin,
		artgen.UpdateMultipleRepositoriesRequestObject{
			JSONBody: &[]repo.Config{{Key: "a", Description: "batch", Notes: "direct"}},
		},
	); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.Repos().Get(ctx, "a"); c.Notes != "direct" || c.Description != "batch" {
		t.Fatalf("stored %+v", c)
	}
	if _, err := svc.CreateMultipleRepositories(admin,
		artgen.CreateMultipleRepositoriesRequestObject{}); err == nil {
		t.Fatal("a batch without a body was accepted")
	}
}

func TestRepositoryStoreFailures(t *testing.T) {
	w := newRepoWorld(t)
	w.create(repo.Config{Key: "lib", Class: repo.Local},
		repo.Config{Key: "v", Class: repo.Virtual, Repositories: []string{"lib"}})
	w.fail["permissions"] = true
	if rec := w.do("GET", "/artifactory/api/repositories", "reader", ""); rec.Code != 500 {
		t.Fatalf("permissions failure %d", rec.Code)
	}
	delete(w.fail, "permissions")
	w.fail["including"] = true
	if rec := w.do("DELETE", "/artifactory/api/repositories/lib", "admin", ""); rec.Code != 500 {
		t.Fatalf("including failure %d", rec.Code)
	}
	delete(w.fail, "including")
	w.fail["list"] = true
	if rec := w.do(
		"POST",
		"/artifactory/api/repositories/v",
		"admin",
		`{"notes":"x"}`,
	); rec.Code != 500 {
		t.Fatalf("member check failure %d", rec.Code)
	}
}
