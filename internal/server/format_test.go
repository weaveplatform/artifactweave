package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// followingFormat is a format whose index follows properties, resolves
// "latest.txt" to "a/f.txt" and rebuilds an index on request, recording
// what it is asked.
type followingFormat struct {
	generic.Handler
	annotated   []string // "path set-keys -removed-keys"
	annotateErr error
	resolveErr  error
	resolved    format.Download
	reindexed   []string
	reindexErr  error
}

func (f *followingFormat) Annotated(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	it types.Item,
	a format.Annotation,
) error {
	keys := make([]string, 0, len(a.Set))
	for k := range a.Set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	line := it.FullPath() + " " + strings.Join(keys, ",")
	if len(a.Removed) > 0 {
		line += " -" + strings.Join(a.Removed, ",")
	}
	f.annotated = append(f.annotated, line)
	return f.annotateErr
}

func (f *followingFormat) Resolve(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	d format.Download,
) (string, error) {
	f.resolved = d
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	if p, ok := strings.CutPrefix(d.Path, "latest.txt"); ok {
		return "a/f.txt" + p, nil
	}
	return d.Path, nil
}

func (f *followingFormat) Reindex(_ context.Context, _ format.Env, c repo.Config) error {
	f.reindexed = append(f.reindexed, c.Key)
	return f.reindexErr
}

func followingWorld(t *testing.T) (*itemWorld, *followingFormat) {
	t.Helper()
	f := &followingFormat{}
	w := newItemWorld(t, false, func(c *Config) {
		c.Formats = func(repo.PackageType) format.Handler { return f }
	})
	return w, f
}

func (f *followingFormat) take() []string {
	out := f.annotated
	f.annotated = nil
	return out
}

// Every way of changing properties over HTTP reaches the format.
func TestPropertyWritesReachTheFormat(t *testing.T) {
	w, f := followingWorld(t)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a/f.txt;m=1", "admin", "f"),
		http.StatusCreated)
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/a/b/;fold=1", "admin", ""),
		http.StatusCreated)
	if got := f.take(); !slices.Equal(got, []string{"a/f.txt m", "a/b fold"}) {
		t.Fatalf("deploys: %v", got)
	}

	steps := []struct {
		method, path, body string
		want               []string
	}{
		{
			"PUT",
			"/artifactory/api/storage/libs-local/a/f.txt?properties=p=1",
			"",
			[]string{"a/f.txt p"},
		},
		{
			"PATCH", "/artifactory/api/metadata/libs-local/a/f.txt", `{"props":{"q":"2","p":null}}`,
			[]string{"a/f.txt q -p"},
		},
		{
			"DELETE",
			"/artifactory/api/storage/libs-local/a/f.txt?properties=q",
			"",
			[]string{"a/f.txt  -q"},
		},
		{
			"PUT", "/artifactory/api/storage/libs-local/a?properties=r=1", "",
			[]string{"a r", "a/b r", "a/f.txt r"},
		},
		{
			"POST", aqlURL,
			`properties.update({"item.name":"f.txt"}).keys("r").newValue("2").dryRun("false")`,
			[]string{"a/f.txt r"},
		},
		{
			"POST",
			"/artifactory/api/copy/libs-local/a/f.txt?to=/other-local/c/f.txt",
			"",
			[]string{"c/f.txt m,r"},
		},
		{
			"POST",
			"/artifactory/api/move/other-local/c/f.txt?to=/other-local/d/f.txt",
			"",
			[]string{"d/f.txt m,r"},
		},
		// The repository root is annotated like a folder, which full path
		// "" stands for here.
		{
			"PUT", "/artifactory/api/storage/libs-local?properties=s=1", "",
			[]string{" s", "a s", "a/b s", "a/f.txt s"},
		},
		{
			"POST",
			aqlURL,
			`properties.update({"item.repo":"libs-local","item.type":"folder"}).keys("s").newValue("2")` +
				`.dryRun("false")`,
			[]string{" s", "a s", "a/b s"},
		},
		{
			"PATCH", "/artifactory/api/metadata/libs-local?recursiveProperties=0",
			`{"props":{"t":"1","s":null}}`,
			[]string{" t -s"},
		},
		{
			"DELETE", "/artifactory/api/storage/libs-local?properties=t&recursive=0", "",
			[]string{"  -t"},
		},
	}
	for _, s := range steps {
		rec := w.do(s.method, s.path, "admin", s.body, "Content-Type", "application/json")
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", s.method, s.path, rec.Code, rec.Body)
		}
		got := f.take()
		slices.Sort(got)
		if !slices.Equal(got, s.want) {
			t.Errorf("%s %s: %v, want %v", s.method, s.path, got, s.want)
		}
	}

	// A format refusing a property change answers with its own status and
	// leaves the properties as they were.
	f.annotateErr = format.Reject(http.StatusBadRequest, "no such dist-tag")
	rec := w.do("PUT", "/artifactory/api/storage/libs-local/a/f.txt?properties=bad=1", "admin", "")
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "no such dist-tag") {
		t.Errorf("body %s", rec.Body)
	}
	props, err := w.db.Items().
		Properties(context.Background(), w.item("libs-local", false, "a/f.txt").ID)
	if err != nil || props.Has("bad", "1") {
		t.Errorf("properties %v, %v", props, err)
	}
	f.annotateErr = errors.New("index unavailable")
	wantStatus(t,
		w.do("PUT", "/artifactory/api/storage/libs-local/a/f.txt?properties=bad=1", "admin", ""),
		http.StatusInternalServerError)
}

// Downloads read the path the format resolves, through a virtual
// repository's members too; the storage API reads the path as given.
func TestDownloadsResolveThroughTheFormat(t *testing.T) {
	w, f := followingWorld(t)
	wantStatus(
		t,
		w.do("PUT", "/artifactory/libs-local/a/f.txt", "admin", "content"),
		http.StatusCreated,
	)
	for _, p := range []string{"/artifactory/libs-local/latest.txt", "/artifactory/virt/latest.txt"} {
		rec := w.do("GET", p, "admin", "")
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != "content" {
			t.Errorf("%s: %q", p, rec.Body)
		}
	}
	rec := w.do("GET", "/artifactory/libs-local/latest.txt.sha256", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != sha256Hex("content") {
		t.Errorf("sidecar %q", rec.Body)
	}
	wantStatus(t, w.do("GET", "/artifactory/api/storage/libs-local/latest.txt", "admin", ""),
		http.StatusNotFound)

	// The format sees the request's matrix parameters, and which files the
	// caller may read; what it names is then read as the caller.
	wantStatus(t, w.do("GET", "/artifactory/libs-local/latest.txt;tier+=gold", "admin", ""),
		http.StatusNotFound)
	if m := f.resolved.Matrix; !m.Props.Has("tier", "gold") || !m.Mandatory["tier"] {
		t.Errorf("matrix %+v", m)
	}
	if err := w.db.Permissions().Create(context.Background(), types.Permission{
		Name: "latest-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"libs-local": {Include: []string{"latest.txt"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do("GET", "/artifactory/libs-local/latest.txt", "user", ""),
		http.StatusForbidden,
	)
	if r := f.resolved.Readable; r == nil || !r("latest.txt") || r("a/f.txt") {
		t.Error("the caller's reads are not the format's")
	}

	f.resolveErr = format.Reject(http.StatusConflict, "no release yet")
	rec = w.do("GET", "/artifactory/libs-local/latest.txt", "admin", "")
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "no release yet") {
		t.Errorf("body %s", rec.Body)
	}
}

// The deploy policy every format inherits answers with Artifactory's
// statuses and wording.
func TestDeployPolicy(t *testing.T) {
	blacked, excluded := true, "**/*.tmp"
	w := newItemWorld(t, false)
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "dark-local", Class: repo.Local, BlackedOut: &blacked},
		{Key: "picky-local", Class: repo.Local, ExcludesPattern: &excluded},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	rec := w.do("PUT", "/artifactory/dark-local/a.jar", "admin", "a")
	wantStatus(t, rec, http.StatusNotFound)
	if !strings.Contains(
		rec.Body.String(),
		"is blacked out and cannot serve artifact 'dark-local:a.jar'",
	) {
		t.Errorf("body %s", rec.Body)
	}
	rec = w.do("PUT", "/artifactory/picky-local/a/b.tmp", "admin", "b")
	wantStatus(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "due to its include/exclude pattern settings") {
		t.Errorf("body %s", rec.Body)
	}
	wantStatus(t, w.do("PUT", "/artifactory/picky-local/a/b.jar", "admin", "b"), http.StatusCreated)
}

// A reindex operation rebuilds through the format of its package type, for
// an admin, once that format has an index to rebuild.
func TestReindexOperations(t *testing.T) {
	w, f := followingWorld(t)
	ctx := context.Background()
	for _, c := range []repo.Config{
		{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		{Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI},
		{Key: "npm-remote", Class: repo.Remote, PackageType: repo.NPM, URL: "https://up.example"},
	} {
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	rec := w.do("POST", "/artifactory/api/npm/npm-local/reindex", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "npm-local") ||
		!slices.Equal(f.reindexed, []string{"npm-local"}) {
		t.Errorf("reindex %q, %v", rec.Body, f.reindexed)
	}
	wantStatus(
		t,
		w.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "admin", ""),
		http.StatusOK,
	)
	for _, c := range []struct {
		path, as string
		want     int
	}{
		{"/artifactory/api/npm/npm-local/reindex", "user", http.StatusForbidden},
		{"/artifactory/api/npm/npm-local/reindex", "", http.StatusUnauthorized},
		{"/artifactory/api/npm/missing/reindex", "admin", http.StatusNotFound},
		{"/artifactory/api/npm/pypi-local/reindex", "admin", http.StatusBadRequest},
		{"/artifactory/api/npm/npm-remote-cache/reindex", "admin", http.StatusBadRequest},
	} {
		wantStatus(t, w.do("POST", c.path, c.as, ""), c.want)
	}
	// npm's remote and virtual repositories are the API's 400 (decision
	// 0034), before the format runs; a format's own refusal is answered as
	// it is.
	wantStatus(t, w.do("POST", "/artifactory/api/npm/npm-remote/reindex", "admin", ""),
		http.StatusBadRequest)
	f.reindexErr = format.Reject(http.StatusBadRequest, "refused by the format")
	wantStatus(t, w.do("POST", "/artifactory/api/npm/npm-local/reindex", "admin", ""),
		http.StatusBadRequest)

	// Every generated reindex operation reaches the same handler.
	f.reindexErr = nil
	for _, p := range []string{
		"alpine/x/reindex", "bower/x/reindex", "cargo/x/reindex", "cocoapods/x/reindex",
		"conan/x/reindex", "conda/x/reindex", "cran/reindex/x", "deb/reindex/x", "gems/x/reindex",
		"helm/x/reindex", "nuget/x/reindex", "opkg/reindex/x", "pub/x/reindex", "swift/x/reindex",
		"terraform/x/reindex",
	} {
		wantStatus(t, w.do("POST", "/artifactory/api/"+p, "admin", ""), http.StatusNotFound)
	}

	// Without a format that rebuilds an index, the operation stays a stub;
	// PyPI's format comes in P8.
	plain := newItemWorld(t, false)
	if err := plain.db.Repos().Create(ctx, *repo.Defaults(&repo.Config{
		Key: "pypi-local", Class: repo.Local, PackageType: repo.PyPI,
	})); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, plain.do("POST", "/artifactory/api/pypi/pypi-local/reindex", "admin", ""),
		http.StatusNotImplemented)
}
