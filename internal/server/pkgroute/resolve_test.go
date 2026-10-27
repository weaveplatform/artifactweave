package pkgroute_test

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

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// encodedFormat is a test protocol shaped like Terraform's registry
// (decision 0040): "v1/modules/<repo>__<ns>/..." and
// "v1/providers/<repo>-rt-ns-<ns>/..." carry the key in the namespace, and
// any other path names it first. Its routes echo what the kit resolved, in
// the Terraform registry's envelope, and it has a discovery document at the
// host root that skips authentication.
func encodedFormat() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:         "testfmt",
		Types:        []repo.PackageType{repo.Generic},
		ErrorProfile: apierror.ProfileTerraformRegistry,
		Reserved:     []string{"{repoKey}/reindex"},
		Resolve: func(rel string) []pkgroute.Key {
			segs := strings.SplitN(rel, "/", 4)
			seps := map[string]string{"modules": "__", "providers": "-rt-ns-"}
			if len(segs) == 4 && segs[0] == "v1" && seps[segs[1]] != "" {
				var out []pkgroute.Key
				for _, k := range pkgroute.SplitKey(segs[2], seps[segs[1]]) {
					out = append(out, pkgroute.Key{
						Key: k.Key, Rest: "v1/" + segs[1] + "/" + k.Rest + "/" + segs[3],
					})
				}
				return out
			}
			return pkgroute.FirstSegment(rel)
		},
		Routes: func(r chi.Router, _ *pkgroute.Kit) {
			r.Get("/v1/{kind}/{ns}/*", func(w http.ResponseWriter, r *http.Request) {
				q := pkgroute.RequestFrom(r)
				_ = json.NewEncoder(w).Encode(resolved{
					Repo: q.Repo.Key, Rest: q.Rest, Kind: chi.URLParam(r, "kind"),
					NS: chi.URLParam(r, "ns"), Tail: chi.URLParam(r, "*"),
				})
			})
		},
		Host: []pkgroute.HostRoute{
			{
				Path:            "/.well-known/testfmt.json",
				Methods:         []string{http.MethodGet, http.MethodHead},
				Unauthenticated: true,
				Serve: func(w http.ResponseWriter, _ *http.Request, k *pkgroute.Kit) {
					if k.DB() == nil {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_, _ = io.WriteString(
						w,
						`{"modules.v1":"/artifactory/api/testfmt/v1/modules/"}`,
					)
				},
			},
			{
				Path:    "/.well-known/private.json",
				Methods: []string{http.MethodGet},
				Serve: func(w http.ResponseWriter, _ *http.Request, _ *pkgroute.Kit) {
					w.WriteHeader(http.StatusNoContent)
				},
			},
		},
	}
}

type resolved struct {
	Repo string `json:"repo"`
	Rest string `json:"rest"`
	Kind string `json:"kind"`
	NS   string `json:"ns"`
	Tail string `json:"tail"`
}

// terraformError is the Terraform registry envelope's message, which says
// the kit answered in the protocol's envelope.
func terraformError(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(body, &e); err != nil || len(e.Errors) != 1 {
		t.Fatalf("not the protocol's envelope: %s", body)
	}
	return e.Errors[0]
}

// newEncodedFixture is the kit fixture with encodedFormat mounted and
// repositories whose keys hold capitals and the separators.
func newEncodedFixture(t *testing.T) *kitFixture {
	t.Helper()
	f := newKitFixture(t, nil)
	for _, key := range []string{"Mixed-Case", "a__b", "x-rt-ns-y", "p", "p__q"} {
		c := repo.Config{Key: key, Class: repo.Local}
		if err := f.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	f.h = f.router(t, f.o, encodedFormat())
	return f
}

func TestAccResolveEncodedKey(t *testing.T) {
	f := newEncodedFixture(t)
	for _, c := range []struct {
		name, path string
		expected   resolved
	}{
		{
			"module", "v1/modules/gen-local__team/net/aws/versions",
			resolved{local, "v1/modules/team/net/aws/versions", "modules", "team", "net/aws/versions"},
		},
		{
			"provider", "v1/providers/gen-local-rt-ns-team/null/versions",
			resolved{local, "v1/providers/team/null/versions", "providers", "team", "null/versions"},
		},
		// Terraform lower-cases a provider's namespace, so the key matches
		// without regard to case.
		{
			"case", "v1/providers/mixed-case-rt-ns-team/null/versions",
			resolved{"Mixed-Case", "v1/providers/team/null/versions", "providers", "team", "null/versions"},
		},
		// A key holding the separator: "a" names no repository, "a__b" does.
		{
			"separator in the key", "v1/modules/a__b__team/n/s/versions",
			resolved{"a__b", "v1/modules/team/n/s/versions", "modules", "team", "n/s/versions"},
		},
		{
			"provider separator in the key", "v1/providers/x-rt-ns-y-rt-ns-team/t/versions",
			resolved{"x-rt-ns-y", "v1/providers/team/t/versions", "providers", "team", "t/versions"},
		},
		// Both "p" and "p__q" exist: the first split from the left wins.
		{
			"first split wins", "v1/modules/p__q__team/n/s/versions",
			resolved{"p", "v1/modules/q__team/n/s/versions", "modules", "q__team", "n/s/versions"},
		},
		// The repository-scoped forms name the key first, in any case.
		{
			"repository-scoped", "gen-local/v1/modules/team/n/s/versions",
			resolved{local, "v1/modules/team/n/s/versions", "modules", "team", "n/s/versions"},
		},
		{
			"repository-scoped, case", "GEN-LOCAL/v1/providers/team/null/versions",
			resolved{local, "v1/providers/team/null/versions", "providers", "team", "null/versions"},
		},
		{
			"federated", "v1/modules/gen-fed__team/n/s/versions",
			resolved{federated, "v1/modules/team/n/s/versions", "modules", "team", "n/s/versions"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(http.MethodGet, base+c.path, "reader", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var got resolved
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != c.expected {
				t.Fatalf("resolved %+v, want %+v", got, c.expected)
			}
		})
	}
}

// Every refusal of a resolved path is in the protocol's envelope.
func TestAccResolveEncodedKeyRefusals(t *testing.T) {
	f := newEncodedFixture(t)
	for _, c := range []struct {
		name, path string
		expected   int
		message    string
	}{
		{
			"missing", "v1/modules/nope__team/n/s/versions", http.StatusNotFound,
			"Repository 'nope' does not exist",
		},
		{
			"missing provider", "v1/providers/nope-rt-ns-team/t/versions", http.StatusNotFound,
			"Repository 'nope' does not exist",
		},
		// No separator, or an empty side, is no candidate at all.
		{"no separator", "v1/modules/team/n/s/versions", http.StatusNotFound, "not found: GET"},
		{"empty namespace", "v1/modules/gen-local__/n/s/versions", http.StatusNotFound, "not found: GET"},
		{"empty key", "v1/modules/__team/n/s/versions", http.StatusNotFound, "not found: GET"},
		{"nothing", "", http.StatusNotFound, "not found: GET"},
		{
			"other type", "v1/modules/img-local__team/n/s/versions", http.StatusNotFound,
			"Repository 'img-local' is not a testfmt repository",
		},
		// The class gate: local and federated only until P9 and P10.
		{
			"remote", "v1/modules/gen-remote__team/n/s/versions", http.StatusNotImplemented,
			"'gen-remote' is a remote repository",
		},
		{
			"virtual", "v1/providers/gen-virtual-rt-ns-team/t/versions", http.StatusNotImplemented,
			"'gen-virtual' is a virtual repository",
		},
		{
			"repository-scoped missing", "nope/v1/modules/team/n/s/versions", http.StatusNotFound,
			"Repository 'nope' does not exist",
		},
		{"no route", "gen-local/elsewhere", http.StatusNotFound, "not found: GET"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(http.MethodGet, base+c.path, "reader", "")
			if rec.Code != c.expected {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.expected, rec.Body)
			}
			if msg := terraformError(t, rec.Body.Bytes()); !strings.Contains(msg, c.message) {
				t.Fatalf("message %q, want it to contain %q", msg, c.message)
			}
		})
	}
	rec := f.do(http.MethodPut, base+"v1/modules/gen-local__team/n/s/versions", "reader", "")
	if rec.Code != http.StatusMethodNotAllowed ||
		!strings.Contains(terraformError(t, rec.Body.Bytes()), "PUT") {
		t.Fatalf("method no route serves: %d %s", rec.Code, rec.Body)
	}
	// The JFrog operation keeps its method, and its other methods answer
	// 405 in the Artifactory envelope.
	if rec := f.do(
		http.MethodPost,
		base+local+"/reindex",
		"reader",
		"",
	); rec.Code != http.StatusOK ||
		rec.Body.String() != "jfrog operation" {
		t.Fatalf("reindex: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		base+local+"/reindex",
		"reader",
		"",
	); rec.Code != http.StatusMethodNotAllowed ||
		!strings.HasPrefix(rec.Body.String(), `{"errors":[{`) {
		t.Fatalf("reindex GET: %d %s", rec.Code, rec.Body)
	}
}

// A Resolve that hands the kit a key it cannot decode is answered 400, and
// a store that cannot say whether a candidate exists is a fault.
func TestAccResolveFailures(t *testing.T) {
	f := newKitFixture(t, nil)
	p := encodedFormat()
	p.Resolve = func(string) []pkgroute.Key { return []pkgroute.Key{{Key: "bad%zz", Rest: "x"}} }
	f.h = f.router(t, f.o, p)
	rec := f.do(http.MethodGet, base+"v1/anything", "reader", "")
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(terraformError(t, rec.Body.Bytes()), "malformed repository key") {
		t.Fatalf("malformed key: %d %s", rec.Code, rec.Body)
	}
	for name, st := range map[string]store.Store{
		"get":  brokenRepos{f.db},
		"list": unlistable{f.db},
	} {
		f.faults = nil
		o := f.o
		o.Store = st
		f.h = f.router(t, o, encodedFormat())
		rec := f.do(http.MethodGet, base+"v1/modules/nope__team/n/s/versions", "reader", "")
		if rec.Code != http.StatusInternalServerError || len(f.faults) != 1 {
			t.Errorf("%s: %d %s, faults %v", name, rec.Code, rec.Body, f.faults)
		}
	}
	// Without a store the kit answers as the server's handlers do.
	o := f.o
	o.Store = nil
	f.h = f.router(t, o, encodedFormat())
	if rec := f.do(
		http.MethodGet,
		base+"v1/modules/a__team/n/s/versions",
		"reader",
		"",
	); rec.Code != http.StatusNotImplemented {
		t.Fatalf("no store: %d %s", rec.Code, rec.Body)
	}
}

type unlistableRepos struct{ store.RepoStore }

func (unlistableRepos) List(context.Context, store.RepoFilter) ([]repo.Config, error) {
	return nil, errors.New("repos down")
}

// unlistable finds repositories by their exact key, and fails to list them.
type unlistable struct{ store.Store }

func (s unlistable) Repos() store.RepoStore { return unlistableRepos{s.Store.Repos()} }

func TestUnitSplitKey(t *testing.T) {
	for _, c := range []struct {
		seg, sep string
		expected []pkgroute.Key
	}{
		{"repo__ns", "__", []pkgroute.Key{{Key: "repo", Rest: "ns"}}},
		{"a__b__c", "__", []pkgroute.Key{{Key: "a", Rest: "b__c"}, {Key: "a__b", Rest: "c"}}},
		// Overlapping separators are each a split.
		{"a___b", "__", []pkgroute.Key{{Key: "a", Rest: "_b"}, {Key: "a_", Rest: "b"}}},
		{"r-rt-ns-n", "-rt-ns-", []pkgroute.Key{{Key: "r", Rest: "n"}}},
		{"__ns", "__", nil},
		{"repo__", "__", nil},
		{"plain", "__", nil},
		{"repo__ns", "", nil},
	} {
		if got := pkgroute.SplitKey(c.seg, c.sep); !slices.Equal(got, c.expected) {
			t.Errorf("SplitKey(%q, %q) = %v, want %v", c.seg, c.sep, got, c.expected)
		}
	}
	if got := pkgroute.FirstSegment(
		"repo/a/b",
	); !slices.Equal(
		got,
		[]pkgroute.Key{{Key: "repo", Rest: "a/b"}},
	) {
		t.Errorf("FirstSegment = %v", got)
	}
	if got := pkgroute.FirstSegment("/a"); got != nil {
		t.Errorf("FirstSegment of an empty key = %v", got)
	}
}

// A host-root route is served outside /artifactory/api/, to anyone, and
// answers other methods in the protocol's envelope.
func TestAccHostRoutes(t *testing.T) {
	f := newEncodedFixture(t)
	for _, user := range []string{"", "anonymous", "reader"} {
		rec := f.do(http.MethodGet, "/.well-known/testfmt.json", user, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "modules.v1") {
			t.Fatalf("as %q: %d %s", user, rec.Code, rec.Body)
		}
	}
	if rec := f.do(
		http.MethodHead,
		"/.well-known/testfmt.json",
		"",
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", rec.Code)
	}
	rec := f.do(http.MethodPost, "/.well-known/testfmt.json", "", "")
	if rec.Code != http.StatusMethodNotAllowed ||
		!strings.Contains(terraformError(t, rec.Body.Bytes()), "POST") {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		"/.well-known/private.json",
		"",
		"",
	); rec.Code != http.StatusNoContent {
		t.Fatalf("second route: %d", rec.Code)
	}
	p := encodedFormat()
	p.Absent = []string{"hidden/"}
	p.Signed = func(r *http.Request) bool { return r.URL.Query().Has("sig") }
	for path, want := range map[string]bool{
		"/.well-known/testfmt.json":             true,
		"/.well-known/testfmt.json/x":           false,
		"/.well-known/private.json":             false,
		"/artifactory/api/testfmt/r/hidden/x":   true,
		"/artifactory/api/testfmt/r/v1/x":       false,
		"/artifactory/api/other/r/hidden/x":     false,
		"/artifactory/.well-known/testfmt.json": false,
		// A signed request skips it under the protocol's prefix alone.
		"/artifactory/api/testfmt/r/v1/x?sig=t": true,
		"/artifactory/api/other/r/v1/x?sig=t":   false,
		"/artifactory/r/v1/x?sig=t":             false,
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if got := p.SkipsAuthentication(r); got != want {
			t.Errorf("SkipsAuthentication(%s) = %v, want %v", path, got, want)
		}
	}
}

// A signed request names its repository first, so the kit takes its key
// from the first segment even where Resolve would read an encoded one: a
// repository keyed v1 serves its signed files at v1/v1/... and at
// v1/providers/... alike.
func TestAccSignedRequestsNameTheKeyFirst(t *testing.T) {
	f := newEncodedFixture(t)
	c := repo.Config{Key: "v1", Class: repo.Local}
	if err := f.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	p := encodedFormat()
	p.Signed = func(r *http.Request) bool { return r.URL.Query().Has("sig") }
	routes := p.Routes
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		routes(r, k)
		r.Get("/providers/*", func(w http.ResponseWriter, r *http.Request) {
			q := pkgroute.RequestFrom(r)
			_, _ = io.WriteString(w, q.Repo.Key+" "+q.Rest)
		})
	}
	f.h = f.router(t, f.o, p)
	rec := f.do(http.MethodGet, base+"v1/v1/modules/team/n/s/1.0.0.zip?sig=t", "reader", "")
	var got resolved
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil ||
		got != (resolved{"v1", "v1/modules/team/n/s/1.0.0.zip", "modules", "team", "n/s/1.0.0.zip"}) {
		t.Fatalf("v1/v1: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, base+"v1/providers/team/null/x?sig=t", "reader", "")
	if rec.Body.String() != "v1 providers/team/null/x" {
		t.Fatalf("signed v1/providers: %d %s", rec.Code, rec.Body)
	}
	// Unsigned, the same path is the global form, whose namespace names no
	// repository.
	rec = f.do(http.MethodGet, base+"v1/providers/team/null/x", "reader", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unsigned: %d %s", rec.Code, rec.Body)
	}
}

// A repository the protocol challenges answers an anonymous request 401
// before it is routed, whatever the anonymous user may read, and any other
// caller as it would.
func TestAccChallenge(t *testing.T) {
	f := newKitFixture(t, nil)
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "forced", Class: repo.Local},
		{Key: "forced-remote", Class: repo.Remote, URL: "https://up.example"},
	} {
		if err := f.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	f.deploy(t, "forced", "a.txt", "forced")
	f.deploy(t, local, "open.txt", "open")
	everyone := map[string]types.Actions{"anonymous": types.ActionRead, "reader": types.ActionRead}
	if err := f.db.Permissions().Create(ctx, types.Permission{
		Name: "anyone-reads",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: everyone},
			Targets: map[string]types.Target{
				"forced": {Include: []string{"**"}}, local: {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	p := testFormat(nil)
	p.Challenge = func(c repo.Config) bool { return strings.HasPrefix(c.Key, "forced") }
	f.h = f.router(t, f.o, p)
	for _, c := range []struct {
		path, user string
		expected   int
	}{
		{"forced/files/a.txt", "anonymous", http.StatusUnauthorized},
		{"forced/files/a.txt", "", http.StatusUnauthorized},
		// Before the 404 a missing file would get.
		{"forced/files/missing.txt", "anonymous", http.StatusUnauthorized},
		// Before the class gate's 501.
		{"forced-remote/files/a.txt", "anonymous", http.StatusUnauthorized},
		{"forced-remote/files/a.txt", "reader", http.StatusNotImplemented},
		{"forced/files/a.txt", "reader", http.StatusOK},
		// Another repository serves the anonymous user what it may read.
		{local + "/files/open.txt", "anonymous", http.StatusOK},
		// A missing repository has nothing to challenge.
		{"nope/files/a.txt", "anonymous", http.StatusNotFound},
	} {
		rec := f.do(http.MethodGet, base+c.path, c.user, "")
		if rec.Code != c.expected {
			t.Errorf("%s as %q: %d, want %d: %s", c.path, c.user, rec.Code, c.expected, rec.Body)
			continue
		}
		if c.expected == http.StatusUnauthorized {
			npmError(t, rec)
			if rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
				t.Errorf("%s as %q: no Basic challenge", c.path, c.user)
			}
		}
	}
}
