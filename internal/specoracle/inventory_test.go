package specoracle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// blockA is a family with a oneOf union, a nested object, an enum schema, a
// property literally named "description" (which must survive scaffolding) and
// 3.1 constructs the converter must fold.
const blockA = `{
  "openapi": "3.1.0",
  "info": {"title": "x", "version": "1", "description": "PROSE"},
  "servers": [{"url": "https://{jfrog_url}/artifactory/api"}],
  "paths": {
    "/repositories/{repoKey}": {
      "parameters": [{"$ref": "#/components/parameters/RepoKey"}],
      "put": {
        "operationId": "createRepository",
        "summary": "PROSE",
        "description": "PROSE",
        "tags": ["Repositories"],
        "security": [{"BasicAuth": []}],
        "parameters": [{"name": "project", "in": "query", "schema": {"type": "string", "default": "p", "enum": ["p", "q"]}, "description": "PROSE"}],
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Repo"}, "examples": {"a": {"value": {}}}}}},
        "responses": {
          "200": {"description": "PROSE", "content": {"text/plain": {"schema": {"type": "string", "example": "ok"}}}},
          "400": {"$ref": "#/components/responses/Err"}
        }
      },
      "delete": {
        "deprecated": true,
        "responses": {"204": {"description": "PROSE"}, "500": {"description": "PROSE", "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Error"}}}}}}
      }
    },
    "/other/{x}": {"get": {"operationId": "elsewhere", "responses": {"200": {"description": "PROSE"}}}}
  },
  "components": {
    "securitySchemes": {"BasicAuth": {"type": "http", "scheme": "basic"}},
    "parameters": {"RepoKey": {"name": "repoKey", "in": "path", "required": true, "schema": {"type": "string"}, "description": "PROSE"}},
    "responses": {"Err": {"description": "PROSE", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}},
    "schemas": {
      "Union": {"description": "PROSE", "oneOf": [{"$ref": "#/components/schemas/Repo"}, {"type": "object", "properties": {"inline": {"type": "string"}}}], "discriminator": {"propertyName": "rclass", "mapping": {"local": "#/components/schemas/Repo"}}},
      "Repo": {
        "type": "object",
        "title": "PROSE",
        "required": ["key"],
        "properties": {
          "key": {"type": "string", "description": "PROSE", "examples": ["x"]},
          "description": {"type": "string"},
          "count": {"type": ["integer", "null"], "format": "int64", "exclusiveMinimum": 0, "const": 1},
          "policy": {"$ref": "#/components/schemas/Policy"},
          "members": {"type": "array", "items": {"$ref": "#/components/schemas/Error"}, "readOnly": true},
          "tags": {"type": "array", "items": {"type": "string"}, "writeOnly": true, "default": []},
          "nested": {"allOf": [{"$ref": "#/components/schemas/Policy"}, {"type": "object", "properties": {"deep": {"type": "boolean"}}}]}
        }
      },
      "Policy": {"type": "string", "enum": ["a", "b"], "description": "PROSE"},
      "Error": {"type": "object", "properties": {"status": {"type": "integer"}}}
    }
  }
}`

// blockA2 is a second page of the same family whose Error schema differs in
// shape, so the inventory counts two variants and the scaffold renames it.
const blockA2 = `{
  "openapi": "3.1.0",
  "servers": [{"url": "https://{jfrog_url}/artifactory/api"}],
  "paths": {"/repositories": {"get": {"operationId": "listRepositories", "responses": {"200": {"description": "PROSE", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}}}}},
  "components": {"schemas": {"Error": {"type": "object", "properties": {"code": {"type": "string"}}}, "ErrorResponse": {"type": "object"}}}
}`

// blockB is another family that defines Repo with a different shape and
// Policy with the same shape.
const blockB = `{
  "openapi": "3.1.0",
  "servers": [{"url": "https://{jfrog_url}/access/api"}],
  "paths": {"/v2/users": {"get": {"operationId": "createRepository", "responses": {"200": {"description": "PROSE"}}}}},
  "components": {"schemas": {
    "Repo": {"type": "object", "properties": {"different": {"type": "string"}}},
    "Policy": {"type": "string", "enum": ["a", "b"]}
  }}
}`

func inventoryCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"artifactory-repositories-api/createrepository.json": blockA,
		"artifactory-repositories-api/listrepositories.json": blockA2,
		"administration-jfrog-platform-rest-apis/users.json": blockB,
		"artifactory-repositories-api/broken.json":           "{not json",
		"artifactory-repositories-api/notes.txt":             "ignored",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := Manifest{Entries: []ManifestEntry{{
		File: "artifactory-repositories-api/createrepository.json",
		URL:  "https://docs.jfrog.com/artifactory/reference/createrepository.md",
	}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildInventory(t *testing.T) {
	dir := inventoryCache(t)
	inv, err := BuildInventory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Pages != 3 || len(inv.Errors) != 1 {
		t.Fatalf("pages %d errors %v", inv.Pages, inv.Errors)
	}
	if len(inv.Operations) != 5 {
		t.Fatalf("operations %d", len(inv.Operations))
	}
	ops := map[string]OpRecord{}
	for _, op := range inv.Operations {
		ops[op.Family+" "+op.Key] = op
	}
	create := ops["artifactory-repositories-api PUT /artifactory/api/repositories/{}"]
	if create.ID != "createRepository" || create.Tag != "Repositories" ||
		create.Source != "https://docs.jfrog.com/artifactory/reference/createrepository.md" {
		t.Fatalf("create op %+v", create)
	}
	if len(create.Params) != 2 || create.Params[0].Name != "repoKey" ||
		create.Params[1].Default != "p" ||
		len(create.Params[1].Enum) != 2 {
		t.Fatalf("params %+v", create.Params)
	}
	if len(create.Request) != 1 || create.Request[0].Schema != "Repo" {
		t.Fatalf("request %+v", create.Request)
	}
	if create.Responses["400"][0].Schema != "Error" ||
		create.Responses["200"][0].Schema != "string" {
		t.Fatalf("responses %+v", create.Responses)
	}
	del := ops["artifactory-repositories-api DELETE /artifactory/api/repositories/{}"]
	if !del.Deprecated || del.Responses["500"][0].Schema != "[]Error" {
		t.Fatalf("delete op %+v", del)
	}
	schemas := map[string]SchemaRecord{}
	for _, s := range inv.Schemas {
		schemas[s.Family+"/"+s.Name] = s
	}
	repo := schemas["artifactory-repositories-api/Repo"]
	fields := map[string]SchemaField{}
	for _, f := range repo.Fields {
		fields[f.Name] = f
	}
	if !fields["key"].Required || fields["description"].Type != "string" {
		t.Fatalf("repo fields %+v", fields)
	}
	if c := fields["count"]; c.Type != "integer" || c.Format != "int64" || !c.Nullable {
		t.Fatalf("count %+v", c)
	}
	if fields["policy"].Ref != "Policy" || fields["members"].Items != "Error" ||
		!fields["members"].ReadOnly ||
		fields["tags"].Items != "string" ||
		!fields["tags"].WriteOnly {
		t.Fatalf("refs %+v", fields)
	}
	errRec := schemas["artifactory-repositories-api/Error"]
	if errRec.Variants != 2 || errRec.UsedBy != 2 {
		t.Fatalf("Error record %+v", errRec)
	}
	union := schemas["artifactory-repositories-api/Union"]
	if union.Type != "oneOf" || len(union.Composed) != 1 || len(union.Fields) != 1 ||
		union.Fields[0].Name != "inline" {
		t.Fatalf("union %+v", union)
	}
	policy := schemas["artifactory-repositories-api/Policy"]
	if len(policy.Fields) != 1 || policy.Fields[0].Name != "(value)" ||
		len(policy.Fields[0].Enum) != 2 {
		t.Fatalf("policy %+v", policy)
	}
	if _, ok := schemas["administration-jfrog-platform-rest-apis/Repo"]; !ok {
		t.Fatal("other family's Repo missing")
	}

	jsonPath := filepath.Join(dir, "inv.json")
	mdPath := filepath.Join(dir, "inv.md")
	if err := inv.WriteJSON(jsonPath); err != nil {
		t.Fatal(err)
	}
	if err := inv.WriteMarkdown(mdPath); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{jsonPath, mdPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "PROSE") {
			t.Fatalf("%s carries source prose", p)
		}
	}
	md, _ := os.ReadFile(mdPath)
	for _, want := range []string{"do not edit", "## artifactory-repositories-api", "| PUT | `/artifactory/api/repositories/{repoKey}` | createRepository |", "Error (2 shapes)", "oneOf:Repo", "| `(value)` | string | a, b |"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
	if err := inv.WriteJSON(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if err := inv.WriteMarkdown(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if err := inv.WriteJSON(filepath.Join(dir, "nope", "x.json")); err == nil {
		t.Fatal("expected write error")
	}
	if err := inv.WriteMarkdown(filepath.Join(dir, "nope", "x.md")); err == nil {
		t.Fatal("expected write error")
	}
	if _, err := BuildInventory(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected error for missing cache")
	}
}

func TestManifestSourcesDegrades(t *testing.T) {
	dir := t.TempDir()
	if len(manifestSources(dir)) != 0 {
		t.Fatal("missing manifest should yield no sources")
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(manifestSources(dir)) != 0 {
		t.Fatal("bad manifest should yield no sources")
	}
}

func TestScaffold(t *testing.T) {
	dir := inventoryCache(t)
	res, err := Scaffold(ScaffoldOptions{
		CacheDir:    dir,
		Family:      "artifactory-repositories-api",
		ServiceBase: "/artifactory",
		SkipSchemas: []string{"ErrorResponse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(res.YAML)
	if strings.Contains(text, "PROSE") {
		t.Fatalf("scaffold carries source prose:\n%s", text)
	}
	if res.Operations != 4 || len(res.Skipped) != 0 {
		t.Fatalf("ops %d skipped %v", res.Operations, res.Skipped)
	}
	// Repo clashes with another family's different shape and is prefixed;
	// Policy has the same shape elsewhere and keeps its name; the second
	// Error shape within the family gets a version suffix.
	if res.Renamed["Repo"] != "ArtifactoryRepositoriesApiRepo" {
		t.Fatalf("renamed %v", res.Renamed)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(res.YAML, &doc); err != nil {
		t.Fatal(err)
	}
	comps := doc["components"].(map[string]any)
	schemas := comps["schemas"].(map[string]any)
	for _, name := range []string{"ArtifactoryRepositoriesApiRepo", "Policy", "Error", "ErrorListrepositories", "Union"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("schema %s missing; have %v", name, keysOf(schemas))
		}
	}
	if _, ok := schemas["ErrorResponse"]; ok {
		t.Error("skipped schema was emitted")
	}
	repo := schemas["ArtifactoryRepositoriesApiRepo"].(map[string]any)
	props := repo["properties"].(map[string]any)
	if _, ok := props["description"].(map[string]any); !ok {
		t.Fatalf("property named description was clobbered: %v", props["description"])
	}
	count := props["count"].(map[string]any)
	if count["type"] != "integer" || count["nullable"] != true ||
		fmt.Sprint(count["minimum"]) != "0" ||
		count["exclusiveMinimum"] != true {
		t.Fatalf("3.1 folding wrong: %v", count)
	}
	if enum, ok := count["enum"].([]any); !ok || len(enum) != 1 {
		t.Fatalf("const not folded: %v", count)
	}
	if _, ok := props["key"].(map[string]any)["examples"]; ok {
		t.Fatal("examples not dropped")
	}
	if props["policy"].(map[string]any)["$ref"] != "#/components/schemas/Policy" {
		t.Fatalf("policy ref %v", props["policy"])
	}
	paths := doc["paths"].(map[string]any)
	item := paths["/api/repositories/{repoKey}"].(map[string]any)
	put := item["put"].(map[string]any)
	if put["operationId"] != "createRepository" || put["x-draft"] != true ||
		put["description"] != Placeholder {
		t.Fatalf("put op %v", put)
	}
	if put["x-oracle-source"] != "https://docs.jfrog.com/artifactory/reference/createrepository.md" {
		t.Fatalf("source %v", put["x-oracle-source"])
	}
	if _, ok := put["security"]; ok {
		t.Fatal("security must be dropped")
	}
	if tags := put["tags"].([]any); len(tags) != 1 || tags[0] != "repositories" {
		t.Fatalf("tags %v", tags)
	}
	if put["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"] != "#/components/schemas/ArtifactoryRepositoriesApiRepo" {
		t.Fatalf("request ref not renamed: %v", put["requestBody"])
	}
	del := item["delete"].(map[string]any)
	if del["operationId"] != "createrepository" {
		t.Fatalf("missing operationId should fall back to the page slug: %v", del["operationId"])
	}
	if item["parameters"] == nil {
		t.Fatal("path-level parameters lost")
	}
	listOp := paths["/api/repositories"].(map[string]any)["get"].(map[string]any)
	if listOp["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"] != "#/components/schemas/ErrorListrepositories" {
		t.Fatalf("second Error shape not re-pointed: %v", listOp["responses"])
	}

	// A family scaffolded for a service it does not belong to emits nothing
	// but reports every operation as skipped (three on the repository path,
	// one on /other).
	res2, err := Scaffold(
		ScaffoldOptions{
			CacheDir:    dir,
			Family:      "artifactory-repositories-api",
			ServiceBase: "/access",
			Tag:         "custom",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Operations != 0 || len(res2.Skipped) != 4 {
		t.Fatalf("ops %d skipped %v", res2.Operations, res2.Skipped)
	}
	for _, bad := range []ScaffoldOptions{{}, {Family: "x"}, {Family: "x", ServiceBase: "/a", CacheDir: dir}, {Family: "x", ServiceBase: "/a", CacheDir: filepath.Join(dir, "missing")}} {
		if _, err := Scaffold(bad); err == nil {
			t.Fatalf("expected error for %+v", bad)
		}
	}
}

func TestScaffoldHelpers(t *testing.T) {
	if defaultTag("artifactory-repositories-api") != "repositories" || defaultTag("api") != "api" {
		t.Fatal("defaultTag")
	}
	if camel("a-b_c d") != "ABCD" || lowerCamel("") != "" ||
		lowerCamel("Create-Repo") != "createRepo" {
		t.Fatal("camel")
	}
	if !looksNumeric("200") || looksNumeric("") || looksNumeric("2xx") {
		t.Fatal("looksNumeric")
	}
	if firstNonNull([]any{"null"}) != "string" {
		t.Fatal("firstNonNull")
	}
	n := sortNode(
		map[string]any{
			"b":   "true",
			"a":   []any{1.5},
			"200": map[string]map[string]any{"x": {"k": "v"}},
		},
	)
	if n.Content[0].Value != "200" || n.Content[0].Style != yaml.DoubleQuotedStyle {
		t.Fatalf("numeric keys must be quoted and sorted first: %v", n.Content[0])
	}
	if n.Content[5].Style != yaml.DoubleQuotedStyle {
		t.Fatal("string that looks boolean must be quoted")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
