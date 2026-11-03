package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recipeFixture(t *testing.T) (inventory, map[string]recipe) {
	t.Helper()
	t.Chdir(t.TempDir())
	for name, data := range map[string]string{
		"client.Dockerfile": "FROM client:1.0@sha256:pinned\n",
		"native.feature":    "Scenario: authorized fresh client\nThen the server answered 401\n",
	} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	step := snippet{Language: "sh", Code: "client install", Note: "Run in the isolated project."}
	r := recipe{
		Schema:         1,
		Type:           "example",
		Name:           "Example",
		Client:         "client 1.0",
		VersionSources: []string{"client.Dockerfile"},
		Endpoint:       "${AW_URL}/artifactory/api/example/${AW_REPO}",
		Auth:           "runtime token",
		FreshHome:      "temporary directory",
		Configure:      []snippet{step},
		Publish:        operation{Note: "read only"},
		Install:        operation{Classes: "R", Note: "remote reads", Steps: []snippet{step}},
		Evidence: []reference{
			{Path: "native.feature", Contains: []string{"authorized fresh client", "answered 401"}},
		},
	}
	return inventory{
		Rows: []format{{Type: "example", Classes: "R", Status: "native"}},
	}, map[string]recipe{
		"example": r,
	}
}

func TestUnitRecipeRejectsUnsupportedOperationsAndMissingProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*recipe)
		want   string
	}{
		{"unsupported publish", func(r *recipe) {
			r.Publish = operation{Classes: "L", Note: "publish", Steps: r.Configure}
		}, "disagree with inventory"},
		{"read-only command", func(r *recipe) { r.Publish.Steps = r.Configure }, "commands exactly"},
		{"unsupported install", func(r *recipe) { r.Install.Classes = "V" }, "disagree with inventory"},
		{"missing client pin", func(r *recipe) { r.VersionSources = nil }, "required"},
		{"unreviewed version", func(r *recipe) { r.VersionSources[0] = "missing.Dockerfile" }, "missing.Dockerfile"},
		{"missing auth", func(r *recipe) { r.Auth = "" }, "required"},
		{"missing isolation", func(r *recipe) { r.FreshHome = "" }, "required"},
		{"unknown schema", func(r *recipe) { r.Schema = 2 }, "required"},
		{"missing deployment endpoint", func(r *recipe) { r.Endpoint = "/api/example/${AW_REPO}" }, "AW_URL"},
		{"missing auth evidence", func(r *recipe) { r.Evidence[0].Contains[1] = "answered 403" }, "missing evidence anchor"},
		{"unanchored evidence", func(r *recipe) { r.Evidence[0].Contains = nil }, "requires source anchors"},
		{"outside repository", func(r *recipe) { r.Evidence[0].Path = "../native.feature" }, "repository-relative"},
		{"test credential", func(r *recipe) { r.Configure[0].Code = "curl -u admin:password" }, "credential marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, recipes := recipeFixture(t)
			r := recipes["example"]
			tc.change(&r)
			recipes[r.Type] = r
			if _, err := validate(
				catalog,
				recipes,
			); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUnitRecipeInventoryAndEvidenceDrift(t *testing.T) {
	catalog, recipes := recipeFixture(t)
	hashes, err := validate(catalog, recipes)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 2 {
		t.Fatalf("evidence hashes = %v", hashes)
	}
	if err := os.WriteFile("client.Dockerfile", []byte("FROM client:2.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := validate(catalog, recipes)
	if err != nil {
		t.Fatal(err)
	}
	if hashes["client.Dockerfile"] == changed["client.Dockerfile"] {
		t.Fatal("changed client pin retained its fingerprint")
	}
	if _, err := validate(catalog, map[string]recipe{}); err == nil {
		t.Fatal("missing native recipe accepted")
	}
	catalog.Rows[0].Status = "planned"
	if _, err := validate(catalog, recipes); err == nil {
		t.Fatal("planned format offered native commands")
	}
	catalog.Rows = nil
	if _, err := validate(catalog, recipes); err == nil {
		t.Fatal("unknown recipe accepted")
	}
}

func TestUnitRecipeRenderingAndStrictInput(t *testing.T) {
	catalog, recipes := recipeFixture(t)
	r := recipes["example"]
	r.Configure[0].Code = "echo '```'"
	doc := render(r, catalog.Rows[0])
	for _, text := range []string{"No supported command. read only", "Classes: **remote**", "````sh", "Client pin:"} {
		if !strings.Contains(string(doc), text) {
			t.Fatalf("render missing %q", text)
		}
	}
	path := filepath.Join(t.TempDir(), "recipe.md")
	if err := writeOrCheck(path, doc, false); err != nil {
		t.Fatal(err)
	}
	if err := writeOrCheck(path, doc, true); err != nil {
		t.Fatal(err)
	}
	if err := writeOrCheck(path, []byte("changed"), true); err == nil {
		t.Fatal("stale output accepted")
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	path = "recipe.json"
	for _, input := range []string{string(data) + "{}", `{"scheam":1}`, "null\n{}"} {
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRecipe(path); err == nil {
			t.Errorf("invalid recipe accepted: %s", input)
		}
	}
}
