package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// actionWorld is an item world with two more users, both readers of every
// repository: cleaner may also delete under libs-local/a, and annotator
// annotate the jars directly in libs-local/a.
func actionWorld(t *testing.T, tune ...func(*Config)) *itemWorld {
	t.Helper()
	ctx := context.Background()
	w := newItemWorld(t, false, tune...)
	for name, grant := range map[string]struct {
		actions types.Actions
		include string
	}{
		"cleaner":   {types.ActionRead | types.ActionDelete, "a/**"},
		"annotator": {types.ActionRead | types.ActionAnnotate, "a/*.jar"},
	} {
		if err := w.db.Users().Create(ctx, types.User{
			Name: name, PasswordHash: cheapHash, Groups: []string{auth.ReadersGroup},
		}); err != nil {
			t.Fatal(err)
		}
		if err := w.db.Permissions().Create(ctx, types.Permission{
			Name: name,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{name: grant.actions}},
				Targets: map[string]types.Target{"libs-local": {Include: []string{grant.include}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func TestAQLDelete(t *testing.T) {
	ctx := context.Background()
	w := actionWorld(t)
	for _, p := range []string{
		"libs-local/a/x.tmp", "libs-local/a/y.jar", "libs-local/b/z.tmp", "other-local/o.tmp",
	} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
	}
	const tmp = `items.delete({"name":{"$match":"*.tmp"}})`
	x := []string{"libs-local/a/x.tmp"}
	// cleaner may delete under a/ only, so the other matches are skipped and
	// left out; without .dryRun(), and with it true, nothing is deleted.
	for _, query := range []string{tmp, tmp + `.dryRun("true")`} {
		if rows, rng := aqlRows(t, w, "cleaner", query); !slices.Equal(rows, x) ||
			rng["total"] != float64(1) {
			t.Fatalf("%s: %v %v", query, rows, rng)
		}
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/a/x.tmp", "admin", ""), http.StatusOK)
	// deployer reads and deploys libs-local, but deletes nothing.
	if rows, _ := aqlRows(t, w, "deployer", tmp+`.dryRun("false")`); len(rows) != 0 {
		t.Fatalf("deployer %v", rows)
	}
	if rows, _ := aqlRows(t, w, "cleaner", tmp+`.dryRun("false")`); !slices.Equal(rows, x) {
		t.Fatalf("cleaner %v", rows)
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/a/x.tmp", "admin", ""), http.StatusNotFound)
	// The file went to the trash can, as DELETE sends it.
	wantStatus(t, w.do("GET", "/artifactory/api/storage/auto-trashcan/libs-local/a/x.tmp",
		"admin", ""), http.StatusOK)
	for _, p := range []string{"libs-local/a/y.jar", "libs-local/b/z.tmp", "other-local/o.tmp"} {
		wantStatus(t, w.do("GET", "/artifactory/"+p, "admin", ""), http.StatusOK)
	}

	// A folder takes what lies below it: z.tmp is gone by its turn, and is
	// still listed.
	rows, _ := aqlRows(t, w, "admin", `items.delete({"repo":"libs-local","type":"any",`+
		`"$or":[{"name":"b"},{"path":"b"}]}).dryRun("false")`)
	if !slices.Equal(rows, []string{"libs-local/./b", "libs-local/b/z.tmp"}) {
		t.Fatalf("folder %v", rows)
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/b/z.tmp", "admin", ""), http.StatusNotFound)

	// A remote's items are deleted from its cache. Nothing of a virtual
	// repository's own, or of a repository no longer configured, is
	// touched; nothing deploys there, so those items are stored directly.
	for _, it := range []types.Item{
		{RepoKey: "up", Cached: true, Path: types.RootPath, Name: "c.tmp"},
		{RepoKey: "virt", Path: types.RootPath, Name: "v.tmp"},
		{RepoKey: "gone", Path: types.RootPath, Name: "g.tmp"},
	} {
		if _, err := w.db.Items().Put(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ = aqlRows(t, w, "admin", `items.delete({"$or":[{"name":"c.tmp"},{"name":"v.tmp"},`+
		`{"name":"g.tmp"}]}).dryRun(false)`)
	if !slices.Equal(rows, []string{"up-cache/./c.tmp"}) {
		t.Fatalf("remote and virtual %v", rows)
	}
	if _, err := w.db.Items().Get(ctx, "up", true, "c.tmp"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cached item: %v", err)
	}
	w.item("virt", false, "v.tmp")
	w.item("gone", false, "g.tmp")
}

// aqlUpdates runs query as user and returns each row as
// "<repo>/<path>/<name>" and its properties as " <key>=<value>".
func aqlUpdates(t *testing.T, w *itemWorld, user, query string) []string {
	t.Helper()
	rec := w.do("POST", aqlURL, user, query)
	var res struct {
		Results []struct {
			Repo       string `json:"repo"`
			Path       string `json:"path"`
			Name       string `json:"name"`
			Properties []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"properties"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
	}
	out := make([]string, len(res.Results))
	for n, r := range res.Results {
		out[n] = r.Repo + "/" + r.Path + "/" + r.Name
		for _, p := range r.Properties {
			out[n] += " " + p.Key + "=" + p.Value
		}
	}
	return out
}

func TestAQLUpdateProperties(t *testing.T) {
	ctx := context.Background()
	w := actionWorld(t)
	for _, p := range []string{
		"libs-local/a/x.jar", "libs-local/b/y.jar", "libs-local/n.jar", "other-local/z.jar",
	} {
		wantStatus(t, w.do("PUT", "/artifactory/"+p, "admin", p), http.StatusCreated)
	}
	set := func(it types.Item, props types.Properties) {
		t.Helper()
		if err := w.db.Items().SetProperties(ctx, it.ID, props); err != nil {
			t.Fatal(err)
		}
	}
	has := func(it types.Item, want types.Properties) {
		t.Helper()
		got, err := w.db.Items().Properties(ctx, it.ID)
		if err != nil || !maps.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("properties of %s: %v %v, want %v", it.FullPath(), got, err, want)
		}
	}
	x := w.item("libs-local", false, "a/x.jar")
	y := w.item("libs-local", false, "b/y.jar")
	z := w.item("other-local", false, "z.jar")
	set(x, types.Properties{"status": {"old"}, "keep": {"k"}})
	set(y, types.Properties{"status": {"old1", "old2"}})
	set(w.item("libs-local", false, "a"), types.Properties{"status": {"f"}})
	set(z, types.Properties{"status": {"old"}})

	const libs = `properties.update({"item.repo":"libs-local"}).keys("status","absent")` +
		`.newValue("approved")`
	// annotator may annotate a/x.jar only, so folder a and b/y.jar are
	// skipped; n.jar has none of the keys. The row shows the new value.
	xRow := []string{"libs-local/a/x.jar status=approved"}
	if rows := aqlUpdates(t, w, "annotator", libs); !slices.Equal(rows, xRow) {
		t.Fatalf("dry run %v", rows)
	}
	has(x, types.Properties{"status": {"old"}, "keep": {"k"}})
	if rows := aqlUpdates(t, w, "deployer", libs+`.dryRun("false")`); len(rows) != 0 {
		t.Fatalf("deployer %v", rows)
	}
	if rows := aqlUpdates(t, w, "annotator", libs+`.dryRun("false")`); !slices.Equal(rows, xRow) {
		t.Fatalf("annotator %v", rows)
	}
	// Only the keys an item has change; absent is not added.
	has(x, types.Properties{"status": {"approved"}, "keep": {"k"}})
	has(y, types.Properties{"status": {"old1", "old2"}})

	// Folders carry properties too, and every value of a key is replaced.
	rows := aqlUpdates(
		t,
		w,
		"admin",
		`properties.update({"item.repo":"libs-local"}).keys("status").newValue("done").dryRun("false")`,
	)
	if !slices.Equal(rows, []string{
		"libs-local/./a status=done", "libs-local/a/x.jar status=done",
		"libs-local/b/y.jar status=done",
	}) {
		t.Fatalf("admin %v", rows)
	}
	has(y, types.Properties{"status": {"done"}})
	has(z, types.Properties{"status": {"old"}})

	// A remote's properties live on its cache; a virtual repository's own
	// items, and a repository no longer configured, are not touched.
	stored := map[string]types.Item{}
	for _, it := range []types.Item{
		{RepoKey: "up", Cached: true, Path: types.RootPath, Name: "c.jar"},
		{RepoKey: "up", Path: types.RootPath, Name: "u.jar"},
		{RepoKey: "virt", Path: types.RootPath, Name: "v.jar"},
		{RepoKey: "gone", Path: types.RootPath, Name: "g.jar"},
	} {
		put, err := w.db.Items().Put(ctx, it)
		if err != nil {
			t.Fatal(err)
		}
		set(put, types.Properties{"status": {"old"}})
		stored[it.Name] = put
	}
	rows = aqlUpdates(t, w, "admin", `properties.update({"$or":[{"item.name":"c.jar"},`+
		`{"item.name":"u.jar"},{"item.name":"v.jar"},{"item.name":"g.jar"}]})`+
		`.keys("status").newValue("cached").dryRun("false")`)
	if !slices.Equal(rows, []string{"up-cache/./c.jar status=cached"}) {
		t.Fatalf("remote and virtual %v", rows)
	}
	has(stored["c.jar"], types.Properties{"status": {"cached"}})
	for _, name := range []string{"u.jar", "v.jar", "g.jar"} {
		has(stored[name], types.Properties{"status": {"old"}})
	}
}

func TestAQLActionLimitsAndErrors(t *testing.T) {
	w := actionWorld(t, func(c *Config) { c.AQL.MaxResults = 1 })
	for _, p := range []string{"a.tmp", "b.tmp"} {
		wantStatus(t, w.do("PUT", "/artifactory/libs-local/"+p, "admin", p), http.StatusCreated)
	}
	// The cap trims an action as it trims find, and what it trims is left
	// alone.
	rows, rng := aqlRows(t, w, "admin", `items.delete({"name":{"$match":"*.tmp"}}).dryRun("false")`)
	if !slices.Equal(rows, []string{"libs-local/./a.tmp"}) || rng["notification"] != aqlTrimmed {
		t.Fatalf("capped %v %v", rows, rng)
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/a.tmp", "admin", ""), http.StatusNotFound)
	wantStatus(t, w.do("GET", "/artifactory/libs-local/b.tmp", "admin", ""), http.StatusOK)
	for _, query := range []string{
		`items.delete().limit(1)`,
		`items.delete().dryRun("maybe")`,
		`properties.update().keys("status")`,
		`properties.update({"key":"status"}).keys("status").newValue("v")`,
	} {
		wantStatus(t, w.do("POST", aqlURL, "admin", query), http.StatusBadRequest)
	}
	wantStatus(t, w.do("GET", "/artifactory/libs-local/b.tmp", "admin", ""), http.StatusOK)
	// Anonymous callers are refused as for find, before anything runs.
	anon := newItemWorld(t, true)
	wantStatus(t, anon.do("PUT", "/artifactory/libs-local/c.tmp", "admin", "c"), http.StatusCreated)
	wantStatus(t, anon.do("POST", aqlURL, "", `items.delete().dryRun("false")`),
		http.StatusForbidden)
	wantStatus(t, anon.do("GET", "/artifactory/libs-local/c.tmp", "admin", ""), http.StatusOK)
}
