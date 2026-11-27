package specoracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// These are the six current wire operations, not vendor prose or schemas.
const currentGroupsDefinition = `{
 "openapi":"3.1.0", "info":{"title":"JFrog Platform - Groups API","version":"1.0.0"},
 "paths":{
  "/access/api/v2/groups/{name}":{
   "get":{"operationId":"getGroupDetails","responses":{"200":{"description":"test"}}},
   "patch":{"operationId":"updateGroup","responses":{"200":{"description":"test"}}},
   "delete":{"operationId":"deleteGroup","responses":{"204":{"description":"test"}}}},
  "/access/api/v2/groups":{
   "get":{"operationId":"getGroupList","responses":{"200":{"description":"test"}}},
   "post":{"operationId":"createGroup","responses":{"201":{"description":"test"}}}},
  "/access/api/v2/groups/{name}/members":{
   "patch":{"operationId":"updateGroupMembers","responses":{"200":{"description":"test"}}}}
 }}`

var groupPages = []struct{ slug, method, path, id string }{
	{"getgroupdetails", "get", "/access/api/v2/groups/{name}", "getGroupDetails"},
	{"updategroup", "patch", "/access/api/v2/groups/{name}", "updateGroup"},
	{"deletegroup", "delete", "/access/api/v2/groups/{name}", "deleteGroup"},
	{"getgrouplist", "get", "/access/api/v2/groups", "getGroupList"},
	{"creategroup", "post", "/access/api/v2/groups", "createGroup"},
	{"updategroupmembers", "patch", "/access/api/v2/groups/{name}/members", "updateGroupMembers"},
}

type currentRegistryTransport struct {
	base *url.URL
}

func (tr currentRegistryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	if copy.URL.Host == "dash.readme.com" {
		copy.URL.Scheme, copy.URL.Host = tr.base.Scheme, tr.base.Host
	}
	return http.DefaultTransport.RoundTrip(copy)
}

func currentGroupsSite(t *testing.T, definition string) *docsSite {
	t.Helper()
	pages := map[string]string{"storage": theirRepoPage}
	for _, page := range groupPages {
		status := "200"
		if page.method == "post" {
			status = "201"
		} else if page.method == "delete" {
			status = "204"
		}
		doc := map[string]any{
			"openapi": "3.1.0", "info": map[string]string{"title": "JFrog Platform - Groups API", "version": "1.0.0"},
			"paths": map[string]any{page.path: map[string]any{page.method: map[string]any{
				"operationId": page.id, "responses": map[string]any{status: map[string]string{"description": "test"}},
			}}},
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		pages[page.slug] = "```json\n" + string(raw) + "\n```\n"
	}
	site := newDocsSite(t, pages)
	var links strings.Builder
	links.WriteString("## API Reference: JFrog Platform REST APIs\n- Groups\n")
	children := []map[string]string{}
	for _, page := range groupPages {
		links.WriteString("  - [" + page.id + "]({URL}/administration/reference/" + page.slug + ".md)\n")
		children = append(children, map[string]string{"slug": page.slug, "api_method": page.method, "type": "endpoint"})
	}
	links.WriteString("\n## API Reference: Storage and Archive\n  - [Storage]({URL}/administration/reference/storage.md)\n")
	site.parent("/administration/reference/llms.txt", http.StatusOK, links.String())
	site.index("/administration/reference/jfrog-platform-groups-api/llms.txt", http.StatusNotFound, "")
	site.index("/administration/reference/jfrog-platform-rest-apis/llms.txt", http.StatusOK, links.String())
	site.index("/administration/reference/storage-and-archive/llms.txt", http.StatusOK,
		"- [Storage]({URL}/administration/reference/storage.md)\n")
	props := map[string]any{
		"oasPublicUrl": "@administration/v1.0#currentgroups",
		"document": map[string]any{"parent": map[string]string{"uri": "/branches/1.0/reference/groups"},
			"api": map[string]any{"uri": "/branches/1.0/apis/groups-openapi.yml", "method": "get", "path": "/access/api/v2/groups/{name}",
				"schema": map[string]any{"openapi": "3.1.0", "info": map[string]string{"title": "JFrog Platform - Groups API"},
					"paths": map[string]any{"/access/api/v2/groups/{name}": map[string]any{"get": map[string]string{"operationId": "getGroupDetails"}}}}}},
		"sidebar": []any{map[string]any{"pages": []any{map[string]any{"slug": "groups", "pages": children}}}},
	}
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatal(err)
	}
	site.index("/administration/reference/groups", http.StatusOK,
		`<script id="ssr-props" type="application/x-ssr-props">`+string(raw)+`</script>`)
	site.router.Get("/api/v1/api-registry/currentgroups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(definition))
	})
	return site
}

func TestAccFetchCurrentGroupsUsesCompletePublishedDefinition(t *testing.T) {
	const family = "administration-jfrog-platform-groups-api"
	for _, name := range []string{"cold", "warm with orphaned historical files"} {
		t.Run(name, func(t *testing.T) {
			site := currentGroupsSite(t, currentGroupsDefinition)
			base, err := url.Parse(site.server.URL)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if strings.HasPrefix(name, "warm") {
				writeCache(t, dir, family, "replacegroup.json", `{"openapi":"3.1.0","paths":{"/access/api/v2/groups/{name}":{"put":{"responses":{"200":{}}}}}}`)
				writeCache(t, dir, family, "unrelated.json", theirRepoBlock)
			}
			groups := site.url("/administration/reference/jfrog-platform-groups-api/llms.txt")
			m, err := Fetch(context.Background(), FetchOptions{
				Indexes: []string{site.url("/administration/reference/jfrog-platform-rest-apis/llms.txt"), groups,
					site.url("/administration/reference/storage-and-archive/llms.txt")},
				CacheDir: dir, Client: &http.Client{Transport: currentRegistryTransport{base: base}},
				Delay: time.Millisecond, Known: map[string][]string{family: {site.url("/administration/reference/replacegroup.md")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Missing()) != 0 {
				t.Fatalf("complete current Groups source must resolve the missing family: %+v", m.Missing())
			}
			if err := CheckFamilies(dir); err != nil {
				t.Fatal(err)
			}
			oracle, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			groupsCount := 0
			for key, op := range oracle.Ops {
				if strings.Contains(key, "/access/api/v2/groups") {
					groupsCount++
					if op.Family != family {
						t.Errorf("current group operation %s attributed to %s", key, op.Family)
					}
				} else if op.Family == family {
					t.Errorf("unrelated sibling %s attributed to Groups", key)
				}
			}
			if groupsCount != 6 {
				t.Fatalf("want six distinct current Group operations; got %d", groupsCount)
			}
			old := "PUT /access/api/v2/groups/{}"
			if _, ok := oracle.Ops[old]; ok {
				t.Fatal("historical orphan must not become a current vendor assertion")
			}
			ours := Oracle{Ops: map[string]Operation{old: {Key: old}}}
			for key, op := range oracle.Ops {
				ours.Ops[key] = op
			}
			r := Compare(oracle, ours)
			if len(r.Extra) != 1 || r.Extra[0] != old {
				t.Fatalf("existing authored PUT must remain explicitly ours-only: %v", r.Extra)
			}
			baselinePath := repopath.Path(t, "api/parity-baseline.json")
			before, err := os.ReadFile(baselinePath)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := ReadBaseline(baselinePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, improvement := range r.Diff(baseline).Improvements {
				if strings.Contains(improvement, old) {
					t.Fatalf("publication gap is not a resolved baseline item: %s", improvement)
				}
			}
			after, err := os.ReadFile(baselinePath)
			if err != nil || string(before) != string(after) {
				t.Fatal("baseline changed while reading current source")
			}
		})
	}
}

func TestAccFetchCurrentGroupsRejectsUnverifiedSources(t *testing.T) {
	cases := []struct {
		name, definition, html string
	}{
		{"wrong family", strings.Replace(currentGroupsDefinition, "JFrog Platform - Groups API", "JFrog Platform - Workers API", 1), ""},
		{"malformed definition", "{bad", ""},
		{"no operations", `{"openapi":"3.1.0","info":{"title":"JFrog Platform - Groups API"},"paths":{}}`, ""},
		{"missing advertised operation", strings.Replace(currentGroupsDefinition, `"post":{"operationId":"createGroup","responses":{"201":{"description":"test"}}}`, `"parameters":[]`, 1), ""},
		{"missing public handle", currentGroupsDefinition, `<script id="ssr-props">{"sidebar":[]}</script>`},
		{"unrelated html", currentGroupsDefinition, `<h1>Groups</h1><a href="https://elsewhere.invalid">related pages</a>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := currentGroupsSite(t, tc.definition)
			if tc.html != "" {
				site.index("/administration/reference/groups", http.StatusOK, tc.html)
			}
			base, err := url.Parse(site.server.URL)
			if err != nil {
				t.Fatal(err)
			}
			index := site.url("/administration/reference/jfrog-platform-groups-api/llms.txt")
			dir := t.TempDir()
			m, err := Fetch(context.Background(), FetchOptions{
				Indexes: []string{index}, CacheDir: dir,
				Client: &http.Client{Transport: currentRegistryTransport{base: base}}, Delay: time.Millisecond,
				Known: map[string][]string{FamilySlug(index): {site.url("/administration/reference/replacegroup.md")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Missing()) != 1 {
				t.Fatalf("unverified source must leave Groups missing: %+v", m)
			}
			if err := CheckFamilies(dir); err == nil {
				t.Fatal("required-family guard accepted an unverified source")
			}
		})
	}
}

// The live hierarchy may advertise fewer operations than its endorsed full
// definition. Membership comes from the verified definition, not a path-prefix
// assumption or a requirement that every operation has a sidebar page.
func TestAccFetchCurrentGroupsRetainsAdditionalPublishedOperations(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(currentGroupsDefinition), &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	paths["/access/api/v3/groups/{name}"] = map[string]any{"get": map[string]any{
		"operationId": "getGroupV3", "responses": map[string]any{"200": map[string]any{}},
	}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	site := currentGroupsSite(t, string(raw))
	base, err := url.Parse(site.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	index := site.url("/administration/reference/jfrog-platform-groups-api/llms.txt")
	dir := t.TempDir()
	m, err := Fetch(context.Background(), FetchOptions{
		Indexes: []string{index}, CacheDir: dir, Delay: time.Millisecond,
		Client: &http.Client{Transport: currentRegistryTransport{base: base}},
		Known:  map[string][]string{FamilySlug(index): {site.url("/administration/reference/replacegroup.md")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Missing()) != 0 {
		t.Fatalf("verified full family was rejected: %+v", m.Missing())
	}
	oracle, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(oracle.Ops) != 7 || oracle.Ops["GET /access/api/v3/groups/{}"].Family != FamilySlug(index) {
		t.Fatalf("additional published operation was lost or misattributed: %+v", oracle.Ops)
	}
}
