package agentplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// minimal is the smallest root plugin.json the Agent Plugins Repositories
// docs show for the standard layout.
const minimal = `{"$schema":"` + StandardSchema + `","name":"hello-plugin"}`

// readBundle reads a bundle of entries, failing the test if it is refused.
func readBundle(t *testing.T, entries ...[2]string) Metadata {
	t.Helper()
	data := archive(t, entries...)
	m, err := ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("bundle refused: %v", err)
	}
	return m
}

// TestUnitStandardManifestConformsToTheSchema: a root plugin.json is the
// Agent Plugins Standard only when it validates against the 1.0.0 schema,
// so only then does a root-only bundle have a layout to index. A root
// plugin.json with a name that does not conform still reads, as it did
// before the standard, and indexes nothing.
func TestUnitStandardManifestConformsToTheSchema(t *testing.T) {
	schema := `"$schema":"` + StandardSchema + `"`
	conforming := map[string]string{
		"minimal": minimal,
		// The spec's own full example, section 5.2.
		"full": `{` + schema + `,"name":"plugin-name","version":"1.2.0",` +
			`"description":"Brief plugin description",` +
			`"author":{"name":"Author Name","email":"author@example.com","url":"https://example.com"},` +
			`"homepage":"https://docs.example.com/plugin","repository":"https://github.com/example/plugin",` +
			`"license":"MIT","keywords":["keyword1","keyword2"],` +
			`"extensions":{"com.example.client":{"setting":true}}}`,
		"periods in the name":   `{` + schema + `,"name":"acme.tools"}`,
		"one character":         `{` + schema + `,"name":"a"}`,
		"64 characters":         `{` + schema + `,"name":"` + strings.Repeat("a", 64) + `"}`,
		"version not semver":    `{` + schema + `,"name":"a","version":"next"}`,
		"empty author":          `{` + schema + `,"name":"a","author":{}}`,
		"empty keywords":        `{` + schema + `,"name":"a","keywords":[]}`,
		"empty extension value": `{` + schema + `,"name":"a","extensions":{"com.example":{}}}`,
	}
	for name, body := range conforming {
		t.Run("conforms: "+name, func(t *testing.T) {
			m := readBundle(t, [2]string{"plugin.json", body})
			if !m.Root || !bytes.Equal(m.Standard, []byte(body)) || !m.Indexed() {
				t.Errorf("read %+v, want the root plugin.json kept as the standard", m)
			}
		})
	}
	notConforming := map[string]string{
		"no $schema":               `{"name":"hello-plugin"}`,
		"the 1.1.0 schema":         `{"$schema":"https://agent-plugins.org/schemas/1.1.0/plugin.schema.json","name":"a"}`,
		"another schema":           `{"$schema":"https://json.schemastore.org/claude-code-plugin-manifest.json","name":"a"}`,
		"$schema not a string":     `{"$schema":1,"name":"a"}`,
		"uppercase name":           `{` + schema + `,"name":"My-Plugin"}`,
		"leading hyphen":           `{` + schema + `,"name":"-start"}`,
		"trailing period":          `{` + schema + `,"name":"end."}`,
		"consecutive hyphens":      `{` + schema + `,"name":"has--double"}`,
		"consecutive periods":      `{` + schema + `,"name":"too.many..dots"}`,
		"65 characters":            `{` + schema + `,"name":"` + strings.Repeat("a", 65) + `"}`,
		"underscore":               `{` + schema + `,"name":"snake_case"}`,
		"version a number":         `{` + schema + `,"name":"a","version":1}`,
		"description null":         `{` + schema + `,"name":"a","description":null}`,
		"homepage an object":       `{` + schema + `,"name":"a","homepage":{}}`,
		"repository an array":      `{` + schema + `,"name":"a","repository":[]}`,
		"license a boolean":        `{` + schema + `,"name":"a","license":true}`,
		"author a string":          `{` + schema + `,"name":"a","author":"Author Name"}`,
		"author null":              `{` + schema + `,"name":"a","author":null}`,
		"author with another key":  `{` + schema + `,"name":"a","author":{"name":"n","twitter":"t"}}`,
		"author name a number":     `{` + schema + `,"name":"a","author":{"name":1}}`,
		"author email null":        `{` + schema + `,"name":"a","author":{"email":null}}`,
		"keywords a string":        `{` + schema + `,"name":"a","keywords":"k"}`,
		"keywords null":            `{` + schema + `,"name":"a","keywords":null}`,
		"keyword a number":         `{` + schema + `,"name":"a","keywords":["k",1]}`,
		"keyword null":             `{` + schema + `,"name":"a","keywords":["k",null]}`,
		"extensions an array":      `{` + schema + `,"name":"a","extensions":[]}`,
		"extensions null":          `{` + schema + `,"name":"a","extensions":null}`,
		"extension value a string": `{` + schema + `,"name":"a","extensions":{"com.example":"x"}}`,
		"extension value null":     `{` + schema + `,"name":"a","extensions":{"com.example":null}}`,
		"unknown field":            `{` + schema + `,"name":"a","skills":"./skills"}`,
	}
	for name, body := range notConforming {
		t.Run("does not conform: "+name, func(t *testing.T) {
			m := readBundle(t, [2]string{"plugin.json", body})
			if !m.Root || m.Standard != nil || m.Indexed() {
				t.Errorf("read %+v, want a root plugin.json that indexes nothing", m)
			}
		})
	}
	// A root plugin.json must still be a JSON object with a name in every
	// bundle, as before the standard.
	for _, body := range []string{"{", "null", "[]", `{"name":""}`, `{"$schema":"` + StandardSchema + `"}`} {
		data := archive(t,
			[2]string{".claude-plugin/plugin.json", `{"name":"reader"}`},
			[2]string{"plugin.json", body})
		if _, err := ReadArchive(
			bytes.NewReader(data),
			int64(len(data)),
		); !errors.Is(
			err,
			ErrArchive,
		) {
			t.Errorf("accepted root plugin.json %s: %v", body, err)
		}
	}
	if conformsToStandard([]byte("[")) {
		t.Error("a manifest that is not JSON conforms")
	}
}

// TestUnitStandardLayoutTypes: agentplugins.type lists the harnesses a
// bundle carries and, for a conforming root plugin.json, generic, vscode
// and cursor. A harness bundle whose root plugin.json does not conform
// keeps the harness types it had.
func TestUnitStandardLayoutTypes(t *testing.T) {
	claude := [2]string{".claude-plugin/plugin.json", `{"name":"reader"}`}
	codex := [2]string{".codex-plugin/plugin.json", `{"name":"reader"}`}
	cursor := [2]string{".cursor-plugin/plugin.json", `{"name":"reader"}`}
	standard := [2]string{"plugin.json", minimal}
	legacy := [2]string{"plugin.json", `{"name":"reader"}`}
	for _, tc := range []struct {
		name    string
		entries [][2]string
		want    []string
	}{
		{"standard layout", [][2]string{standard}, []string{"cursor", "generic", "vscode"}},
		{"Claude and standard", [][2]string{claude, standard}, []string{"claude", "cursor", "generic", "vscode"}},
		{"Cursor and standard", [][2]string{cursor, standard}, []string{"cursor", "generic", "vscode"}},
		{"Claude and Codex", [][2]string{claude, codex}, []string{"claude", "codex"}},
		{"Claude and a root plugin.json that does not conform", [][2]string{claude, legacy}, []string{"claude"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readBundle(t, tc.entries...).Types(); !slices.Equal(got, tc.want) {
				t.Errorf("types %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUnitStandardBundleIsAdmittedAndIndexed: a bundle whose only manifest
// is a conforming root plugin.json is admitted and indexed, with the
// standard layout's agentplugins.type values, where a root plugin.json
// that does not conform is still refused, as every root-only bundle was
// before.
func TestUnitStandardBundleIsAdmittedAndIndexed(t *testing.T) {
	ctx := context.Background()
	h := Handler{}
	c := repo.Config{Key: "plugins", Class: repo.Local, PackageType: repo.AgentPlugins}
	p := Path("hello-plugin", "1.0.0")
	std := readBundle(t, [2]string{"plugin.json", minimal})
	legacy := readBundle(t, [2]string{"plugin.json", `{"name":"hello-plugin"}`})
	w := format.Write{Op: format.OpDeploy, Path: p}
	if _, err := h.Admit(ctx, format.Env{Inspection: &inspection{meta: std}}, c, w); err != nil {
		t.Errorf("standard bundle refused: %v", err)
	}
	_, err := h.Admit(ctx, format.Env{Inspection: &inspection{meta: legacy}}, c, w)
	var rej *format.RejectionError
	if !errors.As(err, &rej) || rej.Status != http.StatusConflict {
		t.Errorf("root-only bundle that does not conform: %v, want 409", err)
	}
	for _, name := range []string{"vscode-marketplace.json", "cursor-marketplace.json"} {
		w.Path = name
		if _, err := h.Admit(
			ctx,
			format.Env{},
			c,
			w,
		); !errors.As(err, &rej) ||
			rej.Status != http.StatusConflict {
			t.Errorf("upload to %s: %v, want 409", name, err)
		}
	}

	st := inmem.New()
	sha := strings.Repeat("b", 64)
	it, err := st.Items().Put(ctx, model.Item{
		RepoKey: c.Key, Name: p, Actual: kernel.Checksums{SHA256: sha}, Created: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var queued []string
	env := format.Env{
		Store: st, Inspection: &inspection{sha: sha, meta: std},
		Enqueue: func(_ context.Context, kind, key string, _ []byte) error {
			queued = append(queued, kind+" "+key)
			return nil
		},
	}
	if err := h.Deployed(ctx, env, c, it); err != nil {
		t.Fatalf("Deployed: %v", err)
	}
	if want := []string{KindMarketplace + " plugins"}; !slices.Equal(queued, want) {
		t.Errorf("Deployed queued %v, want %v", queued, want)
	}
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := props["agentplugins.type"]; !slices.Equal(
		got,
		[]string{"cursor", "generic", "vscode"},
	) {
		t.Errorf("agentplugins.type %v, want cursor, generic and vscode", got)
	}
	for _, file := range []string{"marketplace.json", "cursor-marketplace.json", "vscode-marketplace.json"} {
		if got := listedVersions(t, st, file); !slices.Equal(got, []string{"hello-plugin@1.0.0"}) {
			t.Errorf("%s lists %v, want hello-plugin@1.0.0", file, got)
		}
	}
	for _, file := range []string{"claude-marketplace.json", "codex-marketplace.json"} {
		if got := listedVersions(t, st, file); len(got) != 0 {
			t.Errorf("%s lists %v, want nothing", file, got)
		}
	}

	env.Inspection = &inspection{sha: sha, meta: legacy}
	if err := h.Deployed(ctx, env, c, it); !errors.Is(err, ErrArchive) {
		t.Errorf("Deployed a root-only bundle that does not conform: %v", err)
	}
}

// listedVersions builds file of repository plugins and returns its plugins
// as name@version.
func listedVersions(t *testing.T, st *inmem.Store, file string) []string {
	t.Helper()
	var doc struct {
		Plugins []struct{ Name, Version string } `json:"plugins"`
	}
	files, err := marketplaces(context.Background(), st, "plugins")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files[file], &doc); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, p := range doc.Plugins {
		out = append(out, p.Name+"@"+p.Version)
	}
	return out
}

// TestUnitStandardMarketplaceEntries: each file lists a plugin's newest
// version of the layouts it names, and takes the entry's fields from that
// harness's own manifest first, then from the root plugin.json, without
// its $schema. The docs' example: Claude's file keeps 1.0.0 when 2.0.0
// adds only the standard layout, which the Cursor, VS Code and unified
// files list.
func TestUnitStandardMarketplaceEntries(t *testing.T) {
	ctx := context.Background()
	st := inmem.New()
	put := func(name, version string, entries ...[2]string) {
		t.Helper()
		pkg, err := st.Packages().Ensure(ctx, model.Package{
			RepoKey: "plugins", Type: repo.AgentPlugins, Name: name, Created: time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		m := readBundle(t, entries...)
		m.Name, m.Version, m.Path, m.SHA1 = name, version, Path(
			name,
			version,
		), strings.Repeat(
			"a",
			40,
		)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Packages().PutVersion(ctx, model.PackageVersion{
			PackageID: pkg.ID, Version: version, Metadata: b,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rootManifest := `{"$schema":"` + StandardSchema + `","name":"lie","version":"9.9.9",` +
		`"description":"from the root","keywords":["k"],"extensions":{"com.example":{"x":1}}}`
	put(
		"moving",
		"1.0.0",
		[2]string{".claude-plugin/plugin.json", `{"name":"moving","description":"claude"}`},
	)
	put("moving", "2.0.0", [2]string{"plugin.json", rootManifest})
	put("both", "1.0.0",
		[2]string{".claude-plugin/plugin.json", `{"name":"both","description":"claude"}`},
		[2]string{".cursor-plugin/plugin.json", `{"name":"both","description":"cursor"}`},
		[2]string{"plugin.json", rootManifest})
	put("legacy", "1.0.0",
		[2]string{".claude-plugin/plugin.json", `{"name":"legacy","description":"claude"}`},
		[2]string{"plugin.json", `{"name":"legacy","description":"not the standard"}`})

	entries := func(file string) map[string]map[string]any {
		t.Helper()
		files, err := marketplaces(ctx, st, "plugins")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Plugins []map[string]any `json:"plugins"`
		}
		if err := json.Unmarshal(files[file], &doc); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, p := range doc.Plugins {
			out[p["name"].(string)] = p
		}
		return out
	}
	want := func(file, name, version, description, source string) map[string]any {
		t.Helper()
		e, ok := entries(file)[name]
		if !ok {
			t.Fatalf("%s does not list %s", file, name)
		}
		if e["version"] != version || e["description"] != description {
			t.Errorf(
				"%s lists %s as %v, want %s with description %q",
				file,
				name,
				e,
				version,
				description,
			)
		}
		src, _ := e["source"].(map[string]any)
		if src["source"] != source || src["url"] != Path(name, version) {
			t.Errorf(
				"%s source of %s %v, want %s at %s",
				file,
				name,
				src,
				source,
				Path(name, version),
			)
		}
		return e
	}
	vscode := want("vscode-marketplace.json", "moving", "2.0.0", "from the root", "zip")
	if _, ok := vscode["$schema"]; ok {
		t.Errorf("VS Code entry keeps the manifest's $schema: %v", vscode)
	}
	if !slices.Equal(vscode["keywords"].([]any), []any{"k"}) ||
		vscode["extensions"].(map[string]any)["com.example"] == nil {
		t.Errorf("VS Code entry drops the manifest's fields: %v", vscode)
	}
	want("cursor-marketplace.json", "moving", "2.0.0", "from the root", "zip")
	want("marketplace.json", "moving", "2.0.0", "from the root", "zip")
	want("claude-marketplace.json", "moving", "1.0.0", "claude", "url")

	want("vscode-marketplace.json", "both", "1.0.0", "from the root", "zip")
	want("cursor-marketplace.json", "both", "1.0.0", "cursor", "zip")
	want("marketplace.json", "both", "1.0.0", "claude", "zip")
	want("claude-marketplace.json", "both", "1.0.0", "claude", "url")

	want("marketplace.json", "legacy", "1.0.0", "claude", "zip")
	want("claude-marketplace.json", "legacy", "1.0.0", "claude", "url")
	for _, file := range []string{"vscode-marketplace.json", "cursor-marketplace.json", "codex-marketplace.json"} {
		if e, ok := entries(file)["legacy"]; ok {
			t.Errorf("%s lists a bundle whose root plugin.json does not conform: %v", file, e)
		}
	}
	if e, ok := entries("codex-marketplace.json")["moving"]; ok {
		t.Errorf("Codex file lists the standard layout: %v", e)
	}
}
