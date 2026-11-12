package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/devtools/specoracle"
)

const page = "# Create Repository\n\n```json\n" + `{
  "openapi": "3.1.0",
  "info": {"title": "x", "version": "7.x"},
  "servers": [{"url": "https://{jfrog_url}/artifactory/api"}],
  "paths": {"/repositories/{repoKey}": {"put": {
    "operationId": "createRepository",
    "responses": {"200": {"description": "ok"}}
  }}}
}` + "\n```\n"

const spec = `openapi: 3.0.3
info: {title: ours, version: 0.1.0}
servers:
  - url: /artifactory
paths:
  /api/repositories/{key}:
    put:
      operationId: createRepo
      parameters:
        - {name: key, in: path, required: true, schema: {type: string}}
      responses:
        "200": {description: ok}
`

// TestAccFetchReadsKnownPagesFromTheCommittedInventory runs the fetch
// command the parity job runs, against a docs site where the family's own
// index answers 404 as repositories-api's did on 2026-11-12. The pages the
// committed inventory lists for the family find its section of the parent
// index, so the family is still compared.
func TestAccFetchReadsKnownPagesFromTheCommittedInventory(t *testing.T) {
	r := chi.NewRouter()
	var base string
	r.Get("/artifactory/reference/repositories-api/llms.txt", http.NotFound)
	r.Get("/artifactory/reference/llms.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("## API Reference: JFrog Artifactory Repositories API\n" +
			"- Repositories\n  - [Create Repository](" + base + "/artifactory/reference/createrepository.md): d\n"))
	})
	r.Get(
		"/artifactory/reference/createrepository.md",
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(page))
		},
	)
	server := httptest.NewServer(r)
	defer server.Close()
	base = server.URL

	saved := Indexes
	t.Cleanup(func() { Indexes = saved })
	index := base + "/artifactory/reference/repositories-api/llms.txt"
	Indexes = []string{index}

	api := t.TempDir()
	inv := specoracle.Inventory{Operations: []specoracle.OpRecord{{
		Family: specoracle.FamilySlug(index),
		Key:    "PUT /artifactory/api/repositories/{}",
		Source: base + "/artifactory/reference/createrepository.md",
	}}}
	if err := inv.WriteJSON(filepath.Join(api, InventoryFile)); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if err := run([]string{"fetch", "-cache", cache, "-api", api}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(
		filepath.Join(cache, "artifactory-repositories-api", "createrepository.json"),
	); err != nil {
		t.Fatalf("the family's page should be cached from the parent index: %v", err)
	}
	if err := specoracle.CheckFamilies(cache); err != nil {
		t.Fatal(err)
	}
}

// TestUnitCommandsRefuseACacheMissingAFamily: the baseline and the inventory
// are committed, so neither is rewritten from a cache that lacks a family,
// while the same commands still run on a complete cache.
func TestUnitCommandsRefuseACacheMissingAFamily(t *testing.T) {
	api := t.TempDir()
	if err := os.WriteFile(filepath.Join(api, "x.openapi.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := func(t *testing.T, families ...specoracle.FamilyRecord) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "artifactory-a"), 0o750); err != nil {
			t.Fatal(err)
		}
		block := page[strings.Index(page, "{") : strings.LastIndex(page, "}")+1]
		if err := os.WriteFile(
			filepath.Join(dir, "artifactory-a", "p.json"),
			[]byte(block),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(specoracle.Manifest{
			Families: families,
			Entries: []specoracle.ManifestEntry{
				{
					Index:  "https://d/a/llms.txt",
					Family: "artifactory-a",
					URL:    "https://d/p",
					File:   "artifactory-a/p.json",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(dir, specoracle.ManifestFile),
			data,
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	a := specoracle.FamilyRecord{
		Index:  "https://d/a/llms.txt",
		Family: "artifactory-a",
		Source: "https://d/a/llms.txt",
	}
	b := specoracle.FamilyRecord{
		Index:  "https://d/b/llms.txt",
		Family: "artifactory-b",
		Error:  "status 404",
	}
	incomplete := cache(t, a, b)
	md := filepath.Join(t.TempDir(), "inventory.md")
	for _, args := range [][]string{
		{"baseline", "-cache", incomplete, "-api", api, "-update"},
		{"inventory", "-cache", incomplete, "-api", api, "-out", md},
	} {
		err := run(args)
		if !errors.Is(err, errIncompleteCache) || !errors.Is(err, specoracle.ErrMissingFamily) ||
			!strings.Contains(
				err.Error(),
				"artifactory-b, index https://d/b/llms.txt: status 404",
			) {
			t.Errorf("%s = %v, want a refusal naming artifactory-b", args[0], err)
		}
	}
	for _, f := range []string{BaselineFile, InventoryFile} {
		if _, err := os.Stat(filepath.Join(api, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was written from an incomplete cache: %v", f, err)
		}
	}
	if err := run([]string{"report", "-cache", incomplete, "-api", api}); err != nil {
		t.Errorf("report only warns: %v", err)
	}

	complete := cache(t, a)
	if err := run([]string{"baseline", "-cache", complete, "-api", api, "-update"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"inventory", "-cache", complete, "-api", api, "-out", md}); err != nil {
		t.Fatal(err)
	}
}
