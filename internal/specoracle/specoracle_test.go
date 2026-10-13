package specoracle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const theirRepoPage = "# Create Repository\n\nText with &lt;brackets&gt;.\n\n# OpenAPI definition\n\n```json\n" + theirRepoBlock + "\n```\n"

// theirRepoBlock mimics the shape JFrog embeds: 3.1, host variable in the
// server URL, shared path parameters, refs into components, string-typed
// booleans and a vendor media type.
const theirRepoBlock = `{
  "openapi": "3.1.0",
  "info": {"title": "x", "version": "7.x"},
  "servers": [{"url": "https://{jfrog_url}/artifactory/api"}],
  "paths": {
    "/repositories/{repoKey}": {
      "parameters": [{"$ref": "#/components/parameters/RepoKey"}],
      "put": {
        "operationId": "createRepository",
        "parameters": [
          {"name": "project", "in": "query", "required": false, "schema": {"type": "string"}},
          {"name": "type", "in": "query", "required": true, "schema": {"type": "string", "enum": ["local", "remote"]}}
        ],
        "requestBody": {"content": {
          "application/json": {"schema": {"$ref": "#/components/schemas/Repo"}},
          "application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json": {"schema": {"$ref": "#/components/schemas/Repo"}}
        }},
        "responses": {
          "200": {"description": "ok", "content": {"text/plain": {"schema": {"type": "string"}}}},
          "400": {"$ref": "#/components/responses/Err"},
          "default": {"description": "x"}
        }
      },
      "get": {
        "operationId": "getRepository",
        "deprecated": true,
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Repo"}}}}}
      }
    }
  },
  "components": {
    "parameters": {"RepoKey": {"name": "repoKey", "in": "path", "required": true, "schema": {"type": "string"}}},
    "responses": {"Err": {"description": "bad", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}},
    "schemas": {
      "Repo": {
        "type": "object",
        "properties": {
          "key": {"type": "string"},
          "rclass": {"type": ["string", "null"], "enum": ["local", "remote"]},
          "members": {"type": "array", "items": {"type": "object", "properties": {"url": {"type": "string"}, "enabled": {"type": "string"}}}},
          "contentSynchronisation": {"allOf": [{"$ref": "#/components/schemas/Sync"}]},
          "self": {"$ref": "#/components/schemas/Repo"},
          "extra": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "string"}}}}
        }
      },
      "Sync": {"type": "object", "properties": {"enabled": {"type": "boolean"}}},
      "Error": {"type": "object", "properties": {"errors": {"type": "array", "items": {"type": "object", "properties": {"status": {"type": "integer"}, "message": {"type": "string"}}}}}}
    }
  }
}`

const ourSpec = `openapi: 3.0.3
info: {title: ours, version: 0.1.0}
servers:
  - url: /artifactory
paths:
  /api/repositories/{key}:
    put:
      operationId: createRepo
      parameters:
        - name: key
          in: path
          required: true
          schema: {type: string}
        - name: type
          in: query
          required: true
          schema: {type: string, enum: [local]}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                key: {type: string}
                rclass: {type: string}
                members:
                  type: array
                  items:
                    type: object
                    properties:
                      url: {type: string}
      responses:
        "200":
          description: ok
          content:
            text/plain:
              schema: {type: string}
  /api/system/ping:
    get:
      operationId: ping
      responses:
        "200": {description: ok}
`

func writeCache(t *testing.T, dir, family, name, block string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, family), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, family, name), []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExtractBlock(t *testing.T) {
	cases := []struct {
		name string
		page string
		want bool
	}{
		{"fenced", theirRepoPage, true},
		{"inline", "intro {\"openapi\":\"3.1.0\",\"paths\":{}} trailing", true},
		{"inline pretty", "intro\n{\n  \"openapi\": \"3.1.0\",\n  \"paths\": {}\n}\n", true},
		{
			"entities",
			"```json\n{\"openapi\":\"3.1.0\",\"info\":{\"title\":\"a &lt;b&gt; &amp; c\"},\"paths\":{}}\n```",
			true,
		},
		{"none", "# Page\n\nno spec here\n", false},
		{"bad json", "```json\n{\"openapi\": \n```", false},
		{"not openapi", "```json\n{\"foo\":1}\n```", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ExtractBlock(tc.page)
			if ok != tc.want {
				t.Fatalf("ok=%v want %v", ok, tc.want)
			}
			if ok {
				var doc map[string]any
				if err := json.Unmarshal(got, &doc); err != nil {
					t.Fatalf("not compact json: %v", err)
				}
				if tc.name == "entities" {
					info, _ := doc["info"].(map[string]any)
					if info["title"] != "a <b> & c" {
						t.Fatalf("entities not decoded: %v", info["title"])
					}
				}
			}
		})
	}
}

func TestParseIndexAndSlugs(t *testing.T) {
	body := "# Title\n\n## API Reference: Repositories API\n- [Create Repository](https://docs.example/artifactory/reference/createrepository.md): desc\n* [Other](https://docs.example/x/y): more\nnot a link\n"
	got := ParseIndex(body)
	want := [][2]string{
		{"Create Repository", "https://docs.example/artifactory/reference/createrepository.md"},
		{"Other", "https://docs.example/x/y"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
	for in, want := range map[string]string{
		"https://docs.jfrog.com/artifactory/reference/repositories-api/llms.txt": "artifactory-repositories-api",
		"http://docs.jfrog.com/projects/reference/llms.txt":                      "projects",
		"https://h/llms.txt": "index",
	} {
		if got := FamilySlug(in); got != want {
			t.Errorf("FamilySlug(%q)=%q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"https://docs.example/a/b/createrepository.md": "createrepository",
		"https://docs.example/a/b/page/":               "page",
		"":                                             "page",
	} {
		if got := PageSlug(in); got != want {
			t.Errorf("PageSlug(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFetchWritesCacheAndManifest(t *testing.T) {
	var flaky atomic.Int32
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc(
		"/artifactory/reference/repositories-api/llms.txt",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("User-Agent") != "test-agent" {
				t.Errorf("user agent %q", r.Header.Get("User-Agent"))
			}
			_, _ = w.Write(
				[]byte(
					"- [Create Repository](" + server.URL + "/artifactory/reference/createrepository): d\n" +
						"- [No Block](" + server.URL + "/artifactory/reference/noblock.md): d\n" +
						"- [Gone](" + server.URL + "/artifactory/reference/gone.md): d\n" +
						"- [Flaky](" + server.URL + "/artifactory/reference/flaky.md): d\n",
				),
			)
		},
	)
	mux.HandleFunc(
		"/artifactory/reference/createrepository.md",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(theirRepoPage))
		},
	)
	mux.HandleFunc(
		"/artifactory/reference/noblock.md",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("# nothing here\n"))
		},
	)
	mux.HandleFunc("/artifactory/reference/gone.md", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/artifactory/reference/flaky.md", func(w http.ResponseWriter, _ *http.Request) {
		if flaky.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(theirRepoPage))
	})
	mux.HandleFunc("/dead/llms.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	m, err := Fetch(context.Background(), FetchOptions{
		Indexes: []string{
			server.URL + "/artifactory/reference/repositories-api/llms.txt",
			server.URL + "/dead/llms.txt",
		},
		CacheDir:    dir,
		Client:      server.Client(),
		Delay:       time.Millisecond,
		UserAgent:   "test-agent",
		Concurrency: 2,
		Retries:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Blocks() != 2 {
		t.Fatalf("blocks %d, want 2", m.Blocks())
	}
	byURL := map[string]ManifestEntry{}
	for _, e := range m.Entries {
		byURL[e.URL] = e
	}
	if e := byURL[server.URL+"/artifactory/reference/createrepository"]; e.File != "artifactory-repositories-api/createrepository.json" ||
		e.SHA256 == "" ||
		e.Status != 200 {
		t.Fatalf("unexpected entry %+v", e)
	}
	if e := byURL[server.URL+"/artifactory/reference/noblock.md"]; e.Skipped == "" {
		t.Fatalf("noblock should be skipped: %+v", e)
	}
	if e := byURL[server.URL+"/artifactory/reference/gone.md"]; !strings.Contains(e.Error, "404") {
		t.Fatalf("gone should record 404: %+v", e)
	}
	if e := byURL[server.URL+"/artifactory/reference/flaky.md"]; e.File == "" {
		t.Fatalf("flaky should succeed after retry: %+v", e)
	}
	dead := false
	for _, e := range m.Entries {
		if e.Index == server.URL+"/dead/llms.txt" && strings.Contains(e.Error, "giving up") {
			dead = true
		}
	}
	if !dead {
		t.Fatal("dead index should be recorded with an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		t.Fatal(err)
	}
	var onDisk Manifest
	data, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err := json.Unmarshal(data, &onDisk); err != nil || len(onDisk.Entries) != len(m.Entries) {
		t.Fatalf("manifest on disk: %v, %d entries", err, len(onDisk.Entries))
	}
}

func TestFetchErrors(t *testing.T) {
	if _, err := Fetch(context.Background(), FetchOptions{CacheDir: t.TempDir()}); err == nil {
		t.Fatal("expected error for no indexes")
	}
	if _, err := Fetch(
		context.Background(),
		FetchOptions{Indexes: []string{"http://x"}},
	); err == nil {
		t.Fatal("expected error for no cache dir")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("- [p](http://127.0.0.1:1/p.md): d\n"))
	}))
	defer server.Close()
	m, err := Fetch(
		ctx,
		FetchOptions{
			Indexes:  []string{server.URL + "/i/llms.txt"},
			CacheDir: t.TempDir(),
			Delay:    time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Entries {
		if e.Error == "" {
			t.Fatalf("cancelled fetch should record errors: %+v", e)
		}
	}
	f := newFetcher(FetchOptions{})
	if f.concurrency != 4 || f.delay != 200*time.Millisecond || f.retries != 3 ||
		f.userAgent != DefaultUserAgent ||
		f.client == nil {
		t.Fatalf("defaults not applied: %+v", f)
	}
	if _, _, err := f.get(context.Background(), "::bad url"); err == nil {
		t.Fatal("expected request build error")
	}
}

func TestLoadAndCompare(t *testing.T) {
	dir := t.TempDir()
	writeCache(t, dir, "artifactory-repositories-api", "createrepository.json", theirRepoBlock)
	writeCache(t, dir, "artifactory-repositories-api", "broken.json", "{not json")
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	theirs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs.Errors) != 1 || !strings.Contains(theirs.Errors[0], "broken.json") {
		t.Fatalf("expected one parse error, got %v", theirs.Errors)
	}
	put, ok := theirs.Ops["PUT /artifactory/api/repositories/{}"]
	if !ok {
		t.Fatalf("keys: %v", keys(theirs.Ops))
	}
	wantParams := []Param{
		{Name: "repoKey", In: "path", Required: true, Type: "string"},
		{Name: "project", In: "query", Type: "string"},
		{
			Name:     "type",
			In:       "query",
			Required: true,
			Type:     "string",
			Enum:     []string{"local", "remote"},
		},
	}
	if diff := cmp.Diff(wantParams, put.Params); diff != "" {
		t.Fatal(diff)
	}
	wantMedia := []string{
		"application/json",
		"application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json",
	}
	if diff := cmp.Diff(wantMedia, put.RequestMedia); diff != "" {
		t.Fatal(diff)
	}
	wantProps := []string{
		"contentSynchronisation",
		"contentSynchronisation.enabled",
		"extra",
		"extra.*.v",
		"key",
		"members",
		"members[].enabled",
		"members[].url",
		"rclass",
		"self",
	}
	if diff := cmp.Diff(wantProps, put.RequestProps); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff(
		[]string{"errors", "errors[].message", "errors[].status"},
		put.Responses["400"],
	); diff != "" {
		t.Fatal(diff)
	}
	if put.ID != "createRepository" || put.Family != "artifactory-repositories-api" ||
		put.Path != "/artifactory/api/repositories/{repoKey}" {
		t.Fatalf("unexpected op %+v", put)
	}
	if !theirs.Ops["GET /artifactory/api/repositories/{}"].Deprecated {
		t.Fatal("deprecated flag lost")
	}

	ourPath := filepath.Join(t.TempDir(), "artifactory.openapi.yaml")
	if err := os.WriteFile(ourPath, []byte(ourSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	ours, err := LoadOurs(ourPath)
	if err != nil {
		t.Fatal(err)
	}
	report := Compare(theirs, ours)
	if diff := cmp.Diff(
		[]string{"GET /artifactory/api/repositories/{}"},
		report.Missing,
	); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff([]string{"GET /artifactory/api/system/ping"}, report.Extra); diff != "" {
		t.Fatal(diff)
	}
	wantFindings := []string{
		"missing query parameter project",
		"missing request media type application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json",
		"missing request property contentSynchronisation",
		"missing request property contentSynchronisation.enabled",
		"missing request property extra",
		"missing request property extra.*.v",
		"missing request property members[].enabled",
		"missing request property self",
		"missing response 400",
		"query parameter type lacks enum value remote",
	}
	if diff := cmp.Diff(
		wantFindings,
		report.Divergent["PUT /artifactory/api/repositories/{}"],
	); diff != "" {
		t.Fatal(diff)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("errors should flow into the report: %v", report.Errors)
	}
	sum := report.Summary()
	if diff := cmp.Diff(
		[]FamilySummary{
			{Family: "artifactory-repositories-api", Total: 2, Missing: 1, Divergent: 1},
		},
		sum,
	); diff != "" {
		t.Fatal(diff)
	}
	text := report.String()
	for _, want := range []string{"TOTAL", "Divergent:", "Extra (ours only):", "Parse errors:", "missing response 400"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text lacks %q:\n%s", want, text)
		}
	}

	// A matching definition produces no findings.
	ours.Ops["PUT /artifactory/api/repositories/{}"] = put
	delete(ours.Ops, "GET /artifactory/api/system/ping")
	ours.Ops["GET /artifactory/api/repositories/{}"] = theirs.Ops["GET /artifactory/api/repositories/{}"]
	again := Compare(theirs, ours)
	if len(again.Divergent) != 0 || len(again.Missing) != 0 || len(again.Matched) != 2 {
		t.Fatalf("expected full match: %+v", again)
	}
}

func TestLoadOursErrors(t *testing.T) {
	if _, err := LoadOurs(); err == nil {
		t.Fatal("expected error for no paths")
	}
	if _, err := LoadOurs(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("- just\n- a list\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOurs(bad); err == nil {
		t.Fatal("expected error for non-mapping")
	}
	invalid := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(invalid, []byte("a: [b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOurs(invalid); err == nil {
		t.Fatal("expected yaml error")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing cache")
	}
}

func TestHelpers(t *testing.T) {
	if got := serverBasePath(map[string]any{}); got != "" {
		t.Fatalf("no servers: %q", got)
	}
	if got := serverBasePath(
		map[string]any{"servers": []any{map[string]any{"url": "https://host"}}},
	); got != "" {
		t.Fatalf("host only: %q", got)
	}
	if got := serverBasePath(
		map[string]any{"servers": []any{map[string]any{"url": "/access/"}}},
	); got != "/access" {
		t.Fatalf("relative: %q", got)
	}
	if got := joinPath("/a", "b"); got != "/a/b" {
		t.Fatalf("joinPath: %q", got)
	}
	if NormalisePath("/x/{a}/y/{b}") != "/x/{}/y/{}" {
		t.Fatal("NormalisePath")
	}
	r := resolver{
		doc: map[string]any{
			"components": map[string]any{
				"schemas": map[string]any{
					"A": map[string]any{"$ref": "#/components/schemas/B"},
					"B": map[string]any{"type": "string"},
					"S": map[string]any{"$ref": "#/components/schemas/S"},
				},
			},
		},
	}
	if got := r.deref(
		map[string]any{"$ref": "#/components/schemas/A"},
	); got.(map[string]any)["type"] != "string" {
		t.Fatalf("chained deref: %v", got)
	}
	if got := r.deref(map[string]any{"$ref": "#/components/missing/x"}); got != nil {
		t.Fatalf("missing ref should be nil: %v", got)
	}
	if got := r.deref(map[string]any{"$ref": "http://remote"}); got == nil {
		t.Fatal("remote refs are returned as is")
	}
	if got := r.deref("scalar"); got != "scalar" {
		t.Fatal("scalars pass through")
	}
	out := map[string]bool{}
	r.walk(map[string]any{"$ref": "#/components/schemas/S"}, "", out, map[string]bool{}, 0)
	if len(out) != 0 {
		t.Fatalf("self-referential ref must terminate: %v", out)
	}
	r.walk("not a schema", "", out, map[string]bool{}, 0)
	r.walk(map[string]any{"properties": map[string]any{"x": nil}}, "", out, map[string]bool{}, 9)
	if schemaType(map[string]any{"type": []any{"null", "string", "number"}}) != "number|string" {
		t.Fatal("schemaType union")
	}
	if schemaType(map[string]any{}) != "" || enumValues(map[string]any{}) != nil {
		t.Fatal("empty schema")
	}
	if got := toJSONish(
		map[any]any{1: []any{int(2)}},
	); got.(map[string]any)["1"].([]any)[0] != float64(
		2,
	) {
		t.Fatalf("toJSONish: %v", got)
	}
	o := Oracle{Ops: map[string]Operation{}}
	o.add(
		map[string]any{
			"paths": map[string]any{
				"/p": "not an item",
				"/q": map[string]any{"get": "not an op", "x-ext": 1},
			},
		},
		"f",
		"s",
	)
	if len(o.Ops) != 0 {
		t.Fatalf("malformed paths should be ignored: %v", o.Ops)
	}
	if !isMethod("PATCH") || isMethod("parameters") {
		t.Fatal("isMethod")
	}
	if diff := cmp.Diff([]string{"a"}, diff([]string{"a", "b"}, []string{"b"})); diff != "" {
		t.Fatal(diff)
	}
	if sortedKeys(nil) != nil {
		t.Fatal("sortedKeys nil")
	}
	m := mergeParams(
		[]Param{{Name: "z", In: "query"}, {Name: "a", In: "path"}},
		[]Param{{Name: "z", In: "query", Required: true}},
	)
	if len(m) != 2 || m[0].Name != "a" || !m[1].Required {
		t.Fatalf("mergeParams: %+v", m)
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	report := Report{
		Missing: []string{"GET /a", "GET /b"},
		Divergent: map[string][]string{
			"PUT /c": {"missing response 400", "missing request property x"},
		},
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := report.WriteBaseline(path); err != nil {
		t.Fatal(err)
	}
	b, err := ReadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(
		Baseline{
			Missing: []string{"GET /a", "GET /b"},
			Divergent: map[string][]string{
				"PUT /c": {"missing response 400", "missing request property x"},
			},
		},
		b,
	); diff != "" {
		t.Fatal(diff)
	}
	d := report.Diff(b)
	if len(d.Regressions) != 0 || len(d.Improvements) != 0 {
		t.Fatalf("identical report should be clean: %+v", d)
	}
	// Implement /b, break /c further, and fix one finding on /c.
	later := Report{
		Missing: []string{"GET /a", "GET /new"},
		Divergent: map[string][]string{
			"PUT /c": {"missing response 400", "missing response 409"},
			"GET /d": {"missing response 200"},
		},
	}
	d = later.Diff(b)
	wantReg := []string{
		"GET /d: missing response 200",
		"PUT /c: missing response 409",
		"missing operation GET /new",
	}
	wantImp := []string{"PUT /c: resolved missing request property x", "no longer missing GET /b"}
	if diff := cmp.Diff(wantReg, d.Regressions); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff(wantImp, d.Improvements); diff != "" {
		t.Fatal(diff)
	}
	empty, err := ReadBaseline(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || len(empty.Missing) != 0 || empty.Divergent == nil {
		t.Fatalf("absent baseline: %+v %v", empty, err)
	}
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBaseline(path); err == nil {
		t.Fatal("expected json error")
	}
	if err := os.WriteFile(path, []byte(`{"missing":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadBaseline(path); err != nil || b.Divergent == nil {
		t.Fatalf("nil divergent map should be initialised: %+v %v", b, err)
	}
	if err := report.WriteBaseline(
		filepath.Join(t.TempDir(), "no", "such", "dir", "b.json"),
	); err == nil {
		t.Fatal("expected write error")
	}
}

func keys(m map[string]Operation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
