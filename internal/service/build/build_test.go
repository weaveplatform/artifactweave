package build

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // wire checksum
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const started0 = "2026-10-15T09:30:00.000+0000"

// perms answers Access from two functions; nil allows everything.
type perms struct {
	build func(repoKey, path string, folder bool, want types.Actions) bool
	item  func(c repo.Config, path string, want types.Actions) bool
}

func (p perms) Build(repoKey, path string, folder bool, want types.Actions) bool {
	return p.build == nil || p.build(repoKey, path, folder, want)
}

func (p perms) Item(c repo.Config, path string, want types.Actions) bool {
	return p.item == nil || p.item(c, path, want)
}

var everyone = perms{}

// only allows the build actions in have, on every path.
func only(have types.Actions) perms {
	return perms{
		build: func(_, _ string, _ bool, want types.Actions) bool { return have.Has(want) },
	}
}

type world struct {
	t     *testing.T
	db    *inmem.Store
	blobs blob.Store
	svc   *Service
}

// newWorld is a store with artifactory-build-info and libs-local.
func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	db := inmem.New()
	for _, c := range []repo.Config{
		{Key: types.BuildInfoRepository, Class: repo.Local, PackageType: repo.BuildInfo},
		{Key: "libs-local", Class: repo.Local},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, db: db, blobs: blobs, svc: &Service{Store: db, Blobs: blobs}}
}

func doc(name, number, started string, modules ...string) string {
	return fmt.Sprintf(`{"name":%q,"number":%q,"started":%q,"modules":[%s]}`,
		name, number, started, strings.Join(modules, ","))
}

func sha1Hex(s string) string {
	b := sha1.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

// artifactModule is a module that produced content.
func artifactModule(content string) string {
	return fmt.Sprintf(`{"id":"m","artifacts":[{"name":"a.bin","sha1":%q}]}`, sha1Hex(content))
}

func at(n int) string {
	t, err := ParseStarted(started0)
	if err != nil {
		panic(err)
	}
	return FormatStarted(t.Add(time.Duration(n) * time.Second))
}

func (w *world) publish(d string) Published {
	w.t.Helper()
	p, err := w.svc.Publish(context.Background(), "", []byte(d), "admin", everyone)
	if err != nil {
		w.t.Fatal(err)
	}
	return p
}

// deploy puts content at path in libs-local with properties.
func (w *world) deploy(path, content string, props types.Properties) {
	w.t.Helper()
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, "libs-local")
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(content),
		Properties: props,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// seed publishes run 1 of name, whose artifact app/a.bin in libs-local
// carries the run's build properties.
func (w *world) seed(name string) {
	w.t.Helper()
	w.deploy("app/a.bin", "artifact", types.Properties{
		"build.name": {name}, "build.number": {"1"},
	})
	w.publish(doc(name, "1", started0, artifactModule("artifact")))
}

func (w *world) has(key, path string) bool {
	w.t.Helper()
	_, err := w.db.Items().Get(context.Background(), key, false, path)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		w.t.Fatal(err)
	}
	return err == nil
}

func runPath(name, number, started string) string {
	t, err := ParseStarted(started)
	if err != nil {
		panic(err)
	}
	_, p := types.Build{Name: name, Number: number, Started: t}.DocumentItem()
	return p
}

func TestParse(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`{`, "not valid JSON"},
		{`[]`, "not valid JSON"},
		{`null`, "has no name"},
		{`{"name":" ","number":"1","started":"` + started0 + `"}`, "has no name"},
		{`{"name":"a","started":"` + started0 + `"}`, "has no number"},
		{`{"name":"a","number":"1"}`, "has no started"},
		{`{"name":"a","number":"1","started":"2026-10-15"}`, "layout"},
	} {
		if _, err := Parse("", []byte(tc.doc)); !errors.Is(err, ErrInvalid) ||
			!strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.doc, err, tc.want)
		}
	}

	raw := `{"name":"a","number":"1","started":"2026-10-15T11:30:00.250+02:00","url":"https://ci",` +
		`"properties":{"s":"v","n":3,"z":null,"l":["x"]},` +
		`"modules":[{"id":"m","type":"generic","properties":{"k":"v"},` +
		`"artifacts":[{"name":"a","type":"jar","path":"p/a","sha1":"1","sha256":"2","md5":"3"}],` +
		`"dependencies":[{"id":"d","scopes":["compile"],"sha1":"4"}]}]}`
	b, err := Parse("proj", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := types.Build{
		ProjectKey: "proj", Name: "a", Number: "1",
		Started: time.Date(2026, 10, 15, 9, 30, 0, 250e6, time.UTC), URL: "https://ci",
		Properties: types.Properties{"s": {"v"}, "n": {"3"}, "l": {`["x"]`}},
		Modules: []types.BuildModule{{
			ID: "m", Type: "generic", Properties: types.Properties{"k": {"v"}},
			Artifacts: []types.BuildFile{{
				Name: "a", Type: "jar", Path: "p/a",
				Checksums: types.Checksums{SHA1: "1", SHA256: "2", MD5: "3"},
			}},
			Dependencies: []types.BuildFile{{
				ID: "d", Scopes: []string{"compile"}, Checksums: types.Checksums{SHA1: "4"},
			}},
		}},
		Document: []byte(raw),
	}
	if fmt.Sprint(b) != fmt.Sprint(want) {
		t.Errorf("parsed\n%+v\nwant\n%+v", b, want)
	}
}

func TestStarted(t *testing.T) {
	for _, s := range []string{started0, "2026-10-15T09:30:00.000 0000", "2026-10-15T09:30:00Z"} {
		got, err := ParseStarted(s)
		if err != nil {
			t.Fatal(err)
		}
		if FormatStarted(got) != started0 {
			t.Errorf("%s reads as %s", s, FormatStarted(got))
		}
	}
}

func TestSetFields(t *testing.T) {
	got, err := setFields([]byte(` {"b": [1, 2], "a":"x" , "c":{}} `), map[string]json.RawMessage{
		"a": json.RawMessage(`"y"`), "e": json.RawMessage(`1`), "d": json.RawMessage(`2`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"b":[1, 2],"a":"y","c":{},"d":2,"e":1}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
	for _, bad := range []string{`[]`, `{"a" 1}`, `{"a":}`, `{1:2}`} {
		if _, err := setFields([]byte(bad), nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestProjectKey(t *testing.T) {
	if ProjectKey("default") != "" || ProjectKey("p") != "p" {
		t.Error("ProjectKey")
	}
}

// TestPublish stores runs and their documents, a second run of a number as
// a new run, and the same identity again as a replacement.
func TestPublish(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	first := doc("team/app", "1", started0)
	p := w.publish(first)
	if p.Run.ID == 0 || p.SHA256 == "" {
		t.Fatalf("published %+v", p)
	}
	path := runPath("team/app", "1", started0)
	if path != "team%2Fapp/1-1792056600000.json" || !w.has(types.BuildInfoRepository, path) {
		t.Fatalf("no document item at %s", path)
	}
	w.publish(doc("team/app", "1", at(1)))

	replaced := doc("team/app", "1", started0, `{"id":"m"}`)
	if _, err := w.svc.Publish(
		ctx,
		"default",
		[]byte(replaced),
		"ci",
		only(types.ActionWrite),
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Errorf("replace without delete: %v", err)
	}
	again, err := w.svc.Publish(ctx, "", []byte(replaced), "ci", everyone)
	if err != nil {
		t.Fatal(err)
	}
	if again.Run.ID != p.Run.ID || again.Run.CreatedBy != "admin" || again.Run.ModifiedBy != "ci" {
		t.Errorf("replacement %+v of %+v", again.Run, p.Run)
	}
	runs, err := w.svc.Runs(ctx, "", "team/app", everyone)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs %v, %v", runs, err)
	}
	b, err := w.svc.Run(ctx, "", "team/app", "1", runs[1].Started, everyone)
	if err != nil || string(b.Document) != replaced || len(b.Modules) != 1 {
		t.Errorf("replaced run %s, %v", b.Document, err)
	}

	if _, err := w.svc.Publish(ctx, "", []byte(first), "ci", only(types.ActionRead)); !errors.Is(
		err, ErrForbidden) {
		t.Errorf("publish without deploy: %v", err)
	}
	if _, err := w.svc.Publish(ctx, "p1", []byte(first), "ci", everyone); !errors.Is(
		err, ErrNoRepository) {
		t.Errorf("publish to a project without a build-info repository: %v", err)
	}
	if _, err := w.svc.Publish(ctx, "", []byte(`{}`), "ci", everyone); !errors.Is(err, ErrInvalid) {
		t.Errorf("publish without a name: %v", err)
	}
}

// TestPublishOverStaleDocument needs delete to overwrite a document item
// that has no run.
func TestPublishOverStaleDocument(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c, err := w.db.Repos().Get(ctx, types.BuildInfoRepository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: runPath("app", "1", started0)},
		Body:   bytes.NewReader([]byte("stale")),
	}); err != nil {
		t.Fatal(err)
	}
	d := []byte(doc("app", "1", started0))
	if _, err := w.svc.Publish(
		ctx,
		"",
		d,
		"ci",
		only(types.ActionWrite),
	); !errors.Is(
		err,
		ErrForbidden,
	) {
		t.Errorf("overwrite without delete: %v", err)
	}
	if _, err := w.svc.Publish(
		ctx,
		"",
		d,
		"ci",
		only(types.ActionWrite|types.ActionDelete),
	); err != nil {
		t.Error(err)
	}
}

// TestReads lists names and runs page by page, as far as the caller may
// read them.
func TestReads(t *testing.T) {
	w := newWorld(t)
	w.svc.Page = 1
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c"} {
		w.publish(doc(name, "1", at(i)))
		w.publish(doc(name, "2", at(10+i)))
	}
	notB := perms{build: func(_, path string, folder bool, _ types.Actions) bool {
		return !strings.HasPrefix(path, "b") && !strings.HasSuffix(path, "-1792056610000.json")
	}}
	names, err := w.svc.Names(ctx, "", notB)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(names); !strings.Contains(got, "{ a ") || !strings.Contains(got, "{ c ") ||
		len(names) != 2 {
		t.Errorf("names %v", got)
	}
	runs, err := w.svc.Runs(ctx, "", "a", notB)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Number != "1" {
		t.Errorf("runs of a: %v", runs)
	}
	if _, err := w.svc.Runs(ctx, "", "b", notB); !errors.Is(err, ErrForbidden) {
		t.Errorf("runs of b: %v", err)
	}
	if _, err := w.svc.Runs(ctx, "", "absent", everyone); !errors.Is(err, ErrNotFound) {
		t.Errorf("runs of absent: %v", err)
	}
	if _, err := w.svc.Run(ctx, "", "b", "1", time.Time{}, notB); !errors.Is(err, ErrForbidden) {
		t.Errorf("run of b: %v", err)
	}
	if _, err := w.svc.Run(ctx, "", "a", "3", time.Time{}, everyone); !errors.Is(err, ErrNotFound) {
		t.Errorf("run 3 of a: %v", err)
	}
}

func TestAppend(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.publish(doc("app", "1", started0, `{"id":"m1"}`))
	later := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	w.svc.Now = func() time.Time { return later }
	ap := Append{Name: "app", Number: "1", Modules: []byte(`[{"id":"m2","x":1}]`), By: "ci"}
	if err := w.svc.Append(ctx, ap, everyone); err != nil {
		t.Fatal(err)
	}
	b, err := w.svc.Run(ctx, "", "app", "1", time.Time{}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	if want := doc(
		"app",
		"1",
		started0,
		`{"id":"m1"}`,
		`{"id":"m2","x":1}`,
	); string(b.Document) != want ||
		len(b.Modules) != 2 || b.ModifiedBy != "ci" ||
		!b.Modified.Equal(later) {
		t.Errorf("appended %s at %s, want %s", b.Document, b.Modified, want)
	}

	for _, tc := range []struct {
		ap   Append
		a    Access
		want error
	}{
		{Append{Name: "app", Number: "1", Modules: []byte(`{}`)}, everyone, ErrInvalid},
		{Append{Name: "app", Number: "1", Modules: []byte(`[1]`)}, everyone, ErrInvalid},
		{Append{Name: "app", Number: "2", Modules: []byte(`[]`)}, everyone, ErrNotFound},
		{Append{ProjectKey: "p1", Name: "app", Number: "1", Modules: []byte(`[]`)}, everyone, ErrNoRepository},
		{Append{Name: "app", Number: "1", Modules: []byte(`[]`)}, only(types.ActionWrite), ErrForbidden},
	} {
		if err := w.svc.Append(ctx, tc.ap, tc.a); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", tc.ap, err, tc.want)
		}
	}

	// A stored document that is not an object cannot take modules.
	started, err := ParseStarted(at(5))
	if err != nil {
		t.Fatal(err)
	}
	for number, stored := range map[string]string{"3": `[]`, "4": `null`} {
		if _, err := w.db.Builds().Put(ctx, types.Build{
			Name: "app", Number: number, Started: started, Document: []byte(stored),
		}); err != nil {
			t.Fatal(err)
		}
		err := w.svc.Append(
			ctx,
			Append{Name: "app", Number: number, Modules: []byte(`[]`)},
			everyone,
		)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("append to %s: %v", stored, err)
		}
	}
}

func TestRename(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.publish(doc("old", "1", started0))
	w.publish(doc("old", "2", at(1)))
	w.publish(doc("taken", "1", started0))

	for _, tc := range []struct {
		from, to string
		a        Access
		want     error
	}{
		{"old", " ", everyone, ErrInvalid},
		{"absent", "x", everyone, ErrNotFound},
		{"old", "taken", everyone, ErrConflict},
		{"old", "new", only(types.ActionWrite), ErrForbidden},
		{"old", "new", perms{build: func(_, path string, _ bool, _ types.Actions) bool {
			return strings.HasPrefix(path, "old/")
		}}, ErrForbidden},
	} {
		if err := w.svc.Rename(ctx, "", tc.from, tc.to, "ci", tc.a); !errors.Is(err, tc.want) {
			t.Errorf("rename %s to %s: %v, want %v", tc.from, tc.to, err, tc.want)
		}
	}
	started, err := ParseStarted(started0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Builds().Put(ctx, types.Build{
		Name: "nulldoc", Number: "1", Started: started, Document: []byte(`null`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Rename(ctx, "", "nulldoc", "x", "ci", everyone); !errors.Is(err, ErrInvalid) {
		t.Errorf("rename of a run whose document is not an object: %v", err)
	}
	if err := w.svc.Rename(
		ctx,
		"p1",
		"old",
		"new",
		"ci",
		everyone,
	); !errors.Is(
		err,
		ErrNoRepository,
	) {
		t.Errorf("rename in a project without a repository: %v", err)
	}
	if err := w.svc.Rename(ctx, "", "old", "new", "ci", everyone); err != nil {
		t.Fatal(err)
	}
	b, err := w.svc.Run(ctx, "", "new", "2", time.Time{}, everyone)
	if err != nil || string(b.Document) != doc("new", "2", at(1)) {
		t.Fatalf("renamed run %s, %v", b.Document, err)
	}
	if w.has(types.BuildInfoRepository, "old") ||
		!w.has(types.BuildInfoRepository, runPath("new", "2", at(1))) {
		t.Error("the documents did not move")
	}

	// Names that differ in whitespace alone share their document paths.
	w.publish(doc("a b", "1", started0))
	if err := w.svc.Rename(ctx, "", "a b", "a\tb", "ci", everyone); err != nil {
		t.Fatal(err)
	}
	if !w.has(types.BuildInfoRepository, runPath("a\tb", "1", started0)) {
		t.Error("the document of a rename to the same path went")
	}
	if err := w.svc.Rename(ctx, "", "a\tb", "a\tb", "ci", everyone); err != nil {
		t.Error(err)
	}
}

// TestDelete deletes one run per occurrence of a number, the earliest
// first, and keeps the build's folder while documents remain in it.
func TestDelete(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i, number := range []string{"1", "1", "2", "1"} {
		w.publish(doc("app", number, at(i)))
	}
	gone, err := w.svc.Delete(ctx, Delete{Name: "app", Numbers: []string{"1", "1", "9"}}, everyone)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 || !gone[0].Started.Before(gone[1].Started) ||
		FormatStarted(gone[0].Started) != at(0) {
		t.Errorf("deleted %v", gone)
	}
	if w.has(types.BuildInfoRepository, runPath("app", "1", at(0))) ||
		!w.has(types.BuildInfoRepository, runPath("app", "1", at(3))) {
		t.Error("document items do not follow their runs")
	}

	// A document already deleted through the item API is no obstacle.
	c, err := w.db.Repos().Get(ctx, types.BuildInfoRepository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{Store: w.db, Blobs: w.blobs}).Purge(ctx, item.Target{
		Repo: c, Path: runPath("app", "2", at(2)),
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		d    Delete
		a    Access
		want error
	}{
		{Delete{Name: "app"}, everyone, ErrInvalid},
		{Delete{Name: "absent", All: true}, everyone, ErrNotFound},
		{Delete{ProjectKey: "p1", Name: "app", All: true}, everyone, ErrNoRepository},
		{Delete{Name: "app", All: true}, only(types.ActionRead | types.ActionWrite), ErrForbidden},
	} {
		if _, err := w.svc.Delete(ctx, tc.d, tc.a); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", tc.d, err, tc.want)
		}
	}
	gone, err = w.svc.Delete(ctx, Delete{Name: "app", All: true}, everyone)
	if err != nil || len(gone) != 2 {
		t.Fatalf("deleted %v, %v", gone, err)
	}
	if w.has(types.BuildInfoRepository, "app") {
		t.Error("the folder outlived the build")
	}
}

// TestArtifacts finds a run's artifacts by checksum, once each, and
// deletes those carrying its build properties.
func TestArtifacts(t *testing.T) {
	w := newWorld(t)
	w.svc.Page = 1
	ctx := context.Background()
	w.seed("app")
	w.deploy("copy/a.bin", "artifact", nil)
	w.deploy("hidden/a.bin", "artifact", nil)
	w.publish(doc("app", "2", at(1), artifactModule("artifact"), artifactModule("artifact"),
		`{"id":"none","artifacts":[{"name":"x"}]}`))
	noHidden := perms{item: func(_ repo.Config, path string, _ types.Actions) bool {
		return !strings.HasPrefix(path, "hidden/")
	}}
	items, err := w.svc.Artifacts(ctx, "", "app", "2", noHidden)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range items {
		paths = append(paths, it.FullPath())
	}
	if want := []string{"app/a.bin", "copy/a.bin"}; !slices.Equal(paths, want) {
		t.Errorf("artifacts %v, want %v", paths, want)
	}
	if _, err := w.svc.Artifacts(ctx, "", "app", "3", everyone); !errors.Is(err, ErrNotFound) {
		t.Errorf("artifacts of a missing run: %v", err)
	}

	refused := perms{item: func(_ repo.Config, _ string, want types.Actions) bool {
		return !want.Has(types.ActionDelete)
	}}
	if _, err := w.svc.Delete(ctx, Delete{Name: "app", Numbers: []string{"1"}, Artifacts: true},
		refused); !errors.Is(err, ErrForbidden) {
		t.Errorf("delete without delete on the artifact: %v", err)
	}
	if !w.has("libs-local", "app/a.bin") || len(must(w.svc.Runs(ctx, "", "app", everyone))) != 2 {
		t.Error("a refused delete changed something")
	}
	if _, err := w.svc.Delete(ctx, Delete{Name: "app", Numbers: []string{"1"}, Artifacts: true},
		everyone); err != nil {
		t.Fatal(err)
	}
	if w.has("libs-local", "app/a.bin") || !w.has("libs-local", "copy/a.bin") {
		t.Error("deleted the wrong artifacts")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
