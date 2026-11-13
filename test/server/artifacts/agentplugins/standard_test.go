package agentplugins_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// zipOf is a plugin bundle of the files, path to content.
func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, content := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// standardManifest is the docs' smallest Agent Plugins Standard manifest.
const standardManifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",` +
	`"name":"hello-plugin","description":"standard"}`

// TestAccAgentPluginStandardLayoutIsServed: self-managed 7.173.8 indexes a
// bundle whose only manifest is a conforming root plugin.json into
// vscode-marketplace.json, cursor-marketplace.json and marketplace.json,
// and sets agentplugins.type to generic, vscode and cursor. A bundle whose
// root plugin.json does not conform answers as before: refused on its own,
// and listed by its harness manifests alone beside one.
func TestAccAgentPluginStandardLayoutIsServed(t *testing.T) {
	w := newQueuedWorld(t)
	ctx := context.Background()
	c := repo.Config{Key: "plugins-std", Class: repo.Local, PackageType: repo.AgentPlugins}
	if err := w.DB.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	const (
		stdPath    = "/artifactory/plugins-std/hello-plugin/1.0.0/hello-plugin-1.0.0.zip"
		legacyPath = "/artifactory/plugins-std/legacy/1.0.0/legacy-1.0.0.zip"
	)
	bundle := zipOf(t, map[string]string{
		"plugin.json":           standardManifest,
		"skills/greet/SKILL.md": "---\nname: greet\ndescription: Greets.\n---\nSay hello.\n",
	})
	wantStatus(t, w.Do(http.MethodPut, stdPath, "admin", bundle), http.StatusCreated)
	rootOnly := w.Do(http.MethodPut, "/artifactory/plugins-std/old/1.0.0/old-1.0.0.zip", "admin",
		zipOf(t, map[string]string{"plugin.json": `{"name":"old"}`}))
	wantStatus(t, rootOnly, http.StatusConflict)
	if want := `{"errors":[{"message":"invalid Agent Plugin bundle","status":409}]}`; !bytes.Equal(
		bytes.TrimSpace(rootOnly.Body.Bytes()), []byte(want)) {
		t.Errorf("root-only bundle that does not conform answered %s, want %s", rootOnly.Body, want)
	}
	wantStatus(t, w.Do(http.MethodPut, legacyPath, "admin", zipOf(t, map[string]string{
		".claude-plugin/plugin.json": `{"name":"legacy","description":"claude"}`,
		"plugin.json":                `{"name":"legacy","description":"predates the standard"}`,
	})), http.StatusCreated)

	listing := func(path string) []string {
		t.Helper()
		r := w.Do(http.MethodGet, path, "admin", "")
		wantStatus(t, r, http.StatusOK)
		var doc struct {
			Plugins []struct {
				Name, Version, Description string
				Schema                     *string `json:"$schema"`
				Source                     struct{ Source, URL string }
			} `json:"plugins"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out := []string{}
		for _, p := range doc.Plugins {
			out = append(
				out,
				p.Name+"@"+p.Version+" "+p.Description+" "+p.Source.Source+" "+p.Source.URL,
			)
			if p.Schema != nil {
				t.Errorf("%s entry %s keeps $schema %q", path, p.Name, *p.Schema)
			}
		}
		return out
	}
	std := "hello-plugin@1.0.0 standard zip hello-plugin/1.0.0/hello-plugin-1.0.0.zip"
	legacy := "legacy@1.0.0 claude zip legacy/1.0.0/legacy-1.0.0.zip"
	check := func(when string) {
		t.Helper()
		for _, tc := range []struct {
			file string
			want []string
		}{
			{"vscode-marketplace.json", []string{std}},
			{"cursor-marketplace.json", []string{std}},
			{"marketplace.json", []string{std, legacy}},
			{"claude-marketplace.json", []string{"legacy@1.0.0 claude url legacy/1.0.0/legacy-1.0.0.zip"}},
			{"codex-marketplace.json", []string{}},
		} {
			for _, path := range []string{
				"/artifactory/api/agentplugins/plugins-std/" + tc.file,
				"/artifactory/plugins-std/" + tc.file,
			} {
				if got := listing(path); !slices.Equal(got, tc.want) {
					t.Errorf("%s: %s lists %q, want %q", when, path, got, tc.want)
				}
			}
		}
	}
	check("after the upload")

	types := func(path string) []string {
		t.Helper()
		r := w.Do(
			http.MethodGet,
			"/artifactory/api/storage/plugins-std/"+path+"?properties=agentplugins.type",
			"admin",
			"",
		)
		wantStatus(t, r, http.StatusOK)
		var doc struct {
			Properties map[string][]string `json:"properties"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Properties["agentplugins.type"]
	}
	if got := types(
		"hello-plugin/1.0.0/hello-plugin-1.0.0.zip",
	); !slices.Equal(
		got,
		[]string{"cursor", "generic", "vscode"},
	) {
		t.Errorf("standard bundle's agentplugins.type %v, want cursor, generic and vscode", got)
	}
	if got := types("legacy/1.0.0/legacy-1.0.0.zip"); !slices.Equal(got, []string{"claude"}) {
		t.Errorf("Claude bundle's agentplugins.type %v, want claude alone", got)
	}
	r := w.Do(http.MethodGet, stdPath, "admin", "")
	wantStatus(t, r, http.StatusOK)
	if r.Body.String() != bundle {
		t.Error("the standard bundle downloads other bytes than were uploaded")
	}

	for _, name := range []string{"hello-plugin", "legacy"} {
		p, err := w.DB.Packages().Find(ctx, c.Key, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.DB.Packages().Delete(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Items.Reindex(ctx, c); err != nil {
		t.Fatal(err)
	}
	check("after a reindex")
}
