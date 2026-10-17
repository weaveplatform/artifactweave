package maven_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// deployWith deploys a file carrying properties, as matrix parameters do.
func (w *world) deployWith(p string, props types.Properties) {
	w.t.Helper()
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.c, Path: p}, Body: strings.NewReader(p), Properties: props,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// resolve resolves a download of p from the world's repository.
func (w *world) resolve(d format.Download) string {
	w.t.Helper()
	got, err := w.items.Resolve(w.ctx, w.c, d)
	if err != nil {
		w.t.Fatalf("resolve %s: %v", d.Path, err)
	}
	return got
}

type resolveCase struct {
	name   string
	path   string
	matrix types.Matrix
	want   string
}

func (w *world) check(cases []resolveCase) {
	w.t.Helper()
	for _, tc := range cases {
		w.t.Run(tc.name, func(t *testing.T) {
			got, err := w.items.Resolve(
				w.ctx,
				w.c,
				format.Download{Path: tc.path, Matrix: tc.matrix},
			)
			if err != nil || got != tc.want {
				t.Errorf("%s resolves to %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

// Artifactory's rules for a Maven repository (LocalLatestVersionResolver):
// [RELEASE] is the highest release by Mercury order with the path's
// classifier and extension, a -SNAPSHOT name is its folder's newest unique
// build, and [INTEGRATION] is not processed under the Maven layout.
func TestResolveMaven(t *testing.T) {
	w := newWorld(t, nil)
	dir := "com/acme/app/"
	w.deploy(
		dir+"1.0/app-1.0.pom",
		dir+"1.0/app-1.0.jar",
		dir+"1.0/app-1.0-sources.jar",
		dir+"1.10/app-1.10.jar",
		dir+"1.10/app-1.10-tests.jar",
		dir+"1.9/app-1.9.jar",
		dir+"1.9/app-1.9-tests.jar",
		// A pre-release is a release to [RELEASE]: only an integration
		// revision makes a file a snapshot.
		dir+"2.0-alpha-1/app-2.0-alpha-1.jar",
		dir+"1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar",
		dir+"1.0-SNAPSHOT/app-1.0-20261017.100000-2.pom",
		dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar",
		dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1-sources.jar",
		dir+"2.0-SNAPSHOT/app-2.0.jar",
		dir+"2.0-SNAPSHOT/app-2.0-20261017.090000-1.jar",
		"com/acme/other/3.0/other-3.0.jar",
	)
	w.check([]resolveCase{
		{
			name: "release jar", path: dir + "[RELEASE]/app-[RELEASE].jar",
			want: dir + "2.0-alpha-1/app-2.0-alpha-1.jar",
		},
		{
			name: "release classifier", path: dir + "[RELEASE]/app-[RELEASE]-sources.jar",
			want: dir + "1.0/app-1.0-sources.jar",
		},
		{
			// Mercury order, not the alphabetical order of other layouts.
			name: "release by Mercury", path: dir + "[RELEASE]/app-[RELEASE]-tests.jar",
			want: dir + "1.10/app-1.10-tests.jar",
		},
		{
			name: "release pom",
			path: dir + "[RELEASE]/app-[RELEASE].pom",
			want: dir + "1.0/app-1.0.pom",
		},
		{
			name: "release without the extension", path: dir + "[RELEASE]/app-[RELEASE].war",
			want: dir + "[RELEASE]/app-[RELEASE].war",
		},
		{
			name: "release of a missing module", path: "com/acme/none/[RELEASE]/none-[RELEASE].jar",
			want: "com/acme/none/[RELEASE]/none-[RELEASE].jar",
		},
		{
			// The file name's base revision must be the folder's, so the
			// layout does not read the path, as in Artifactory.
			name: "release token in a name only", path: dir + "1.0/app-[RELEASE].jar",
			want: dir + "1.0/app-[RELEASE].jar",
		},
		{
			name: "snapshot jar", path: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar",
			want: dir + "1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar",
		},
		{
			name: "snapshot pom", path: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.pom",
			want: dir + "1.0-SNAPSHOT/app-1.0-20261017.100000-2.pom",
		},
		{
			name: "snapshot classifier", path: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT-sources.jar",
			want: dir + "1.0-SNAPSHOT/app-1.0-20261017.090000-1-sources.jar",
		},
		{
			name: "snapshot without the extension", path: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.war",
			want: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.war",
		},
		{
			// A file without a file revision joins as "1.0-null", below
			// every unique build, as Artifactory compares it.
			name: "snapshot beside a release name", path: dir + "2.0-SNAPSHOT/app-2.0-SNAPSHOT.jar",
			want: dir + "2.0-SNAPSHOT/app-2.0-20261017.090000-1.jar",
		},
		{
			name: "snapshot folder missing", path: dir + "3.0-SNAPSHOT/app-3.0-SNAPSHOT.jar",
			want: dir + "3.0-SNAPSHOT/app-3.0-SNAPSHOT.jar",
		},
		{
			name: "snapshot metadata", path: dir + "1.0-SNAPSHOT/maven-metadata.xml",
			want: dir + "1.0-SNAPSHOT/maven-metadata.xml",
		},
		{
			name: "integration ignored", path: dir + "1.0-[INTEGRATION]/app-1.0-[INTEGRATION].jar",
			want: dir + "1.0-[INTEGRATION]/app-1.0-[INTEGRATION].jar",
		},
		{name: "a plain path", path: dir + "1.0/app-1.0.jar", want: dir + "1.0/app-1.0.jar"},
		{
			name: "a checksum", path: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar.sha1",
			want: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar.sha1",
		},
	})
}

// A candidate must match the request's matrix parameters: a key it lacks
// does not stop it, unless the key is mandatory.
func TestResolveMatrix(t *testing.T) {
	w := newWorld(t, nil)
	dir := "com/acme/app/"
	w.deployWith(dir+"1.9/app-1.9.jar", types.Properties{"tier": {"gold"}})
	w.deployWith(dir+"1.10/app-1.10.jar", types.Properties{"tier": {"silver"}})
	w.deploy(dir + "1.8/app-1.8.jar")
	w.deployWith(
		dir+"1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar",
		types.Properties{"tier": {"gold"}},
	)
	w.deployWith(
		dir+"1.0-SNAPSHOT/app-1.0-20261017.100000-2.jar",
		types.Properties{"tier": {"silver"}},
	)
	gold := types.Matrix{Props: types.Properties{"tier": {"gold"}}}
	bronze := types.Matrix{Props: types.Properties{"tier": {"bronze"}}}
	mandatory := types.Matrix{
		Props:     types.Properties{"tier": {"bronze"}},
		Mandatory: map[string]bool{"tier": true},
	}
	w.check([]resolveCase{
		{
			name:   "release gold",
			path:   dir + "[RELEASE]/app-[RELEASE].jar",
			matrix: gold,
			want:   dir + "1.9/app-1.9.jar",
		},
		{
			name: "release bronze", path: dir + "[RELEASE]/app-[RELEASE].jar", matrix: bronze,
			want: dir + "1.8/app-1.8.jar",
		},
		{
			name:   "release bronze mandatory",
			path:   dir + "[RELEASE]/app-[RELEASE].jar",
			matrix: mandatory,
			want:   dir + "[RELEASE]/app-[RELEASE].jar",
		},
		{
			name: "snapshot gold", path: dir + "1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar", matrix: gold,
			want: dir + "1.0-SNAPSHOT/app-1.0-20261017.090000-1.jar",
		},
	})
}

// A snapshot resolves among the files the caller may read and the
// repository's patterns accept, as Artifactory lists the folder for the
// caller; under non-unique it is served as named, and under deployer the
// last modified build wins.
func TestResolveSnapshotBehaviours(t *testing.T) {
	dir := "com/acme/app/1.0-SNAPSHOT/"
	builds := func(w *world) {
		// The higher build deploys first, so the last modified is the lower.
		w.deploy(dir+"app-1.0-20261017.100000-2.jar", dir+"app-1.0-20261017.090000-1.jar")
	}
	req := format.Download{Path: dir + "app-1.0-SNAPSHOT.jar"}

	w := newWorld(t, nil)
	builds(w)
	if got := w.resolve(req); got != dir+"app-1.0-20261017.100000-2.jar" {
		t.Errorf("unique: %s", got)
	}
	unreadable := req
	unreadable.Readable = func(p string) bool { return !strings.HasSuffix(p, "-2.jar") }
	if got := w.resolve(unreadable); got != dir+"app-1.0-20261017.090000-1.jar" {
		t.Errorf("unreadable build: %s", got)
	}

	// The patterns change after the deploys, which they would refuse.
	excludes := "**/*-2.jar"
	w.c.ExcludesPattern = &excludes
	if got := w.resolve(req); got != dir+"app-1.0-20261017.090000-1.jar" {
		t.Errorf("excluded build: %s", got)
	}

	w = newWorld(t, func(c *repo.Config) { c.SnapshotVersionBehavior = repo.Deployer })
	builds(w)
	if got := w.resolve(req); got != dir+"app-1.0-20261017.090000-1.jar" {
		t.Errorf("deployer: %s", got)
	}

	w = newWorld(t, func(c *repo.Config) { c.SnapshotVersionBehavior = repo.NonUnique })
	builds(w)
	if got := w.resolve(req); got != req.Path {
		t.Errorf("non-unique: %s", got)
	}
}

// Outside the Maven layout -SNAPSHOT is a name like any other, and
// [RELEASE] and [INTEGRATION] name the alphabetically last release, or
// integration revision of the path's base revision, as JFrog documents,
// whatever was created last.
func TestResolveGradle(t *testing.T) {
	w := newWorld(t, func(c *repo.Config) {
		c.PackageType, c.RepoLayoutRef = repo.Gradle, "gradle-default"
		// Gradle's default (pkg/repo/defaults.go): the fixture's POM sits
		// in a Gradle path, which the POM checks would refuse.
		c.SuppressPomConsistencyChecks = on()
	})
	dir := "org.acme/lib/"
	w.deployWith(
		dir+"1.0-20261017090000/lib-1.0-20261017090000.jar",
		types.Properties{"tier": {"gold"}},
	)
	w.deployWith(
		dir+"1.0-20261017100000/lib-1.0-20261017100000.jar",
		types.Properties{"tier": {"silver"}},
	)
	w.deploy(
		dir+"1.0-20261017100000/lib-1.0-20261017100000.pom",
		dir+"2.0-20261017070000/lib-2.0-20261017070000.jar",
		dir+"1.10/lib-1.10.jar",
		dir+"1.9/lib-1.9.jar",
		dir+"1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar",
		dir+"1.0-20261017080000/lib-1.0-20261017080000.jar",
	)
	w.check([]resolveCase{
		{
			name: "integration", path: dir + "1.0-[INTEGRATION]/lib-1.0-[INTEGRATION].jar",
			want: dir + "1.0-20261017100000/lib-1.0-20261017100000.jar",
		},
		{
			name: "integration gold", path: dir + "1.0-[INTEGRATION]/lib-1.0-[INTEGRATION].jar",
			matrix: types.Matrix{Props: types.Properties{"tier": {"gold"}}},
			want:   dir + "1.0-20261017090000/lib-1.0-20261017090000.jar",
		},
		{
			name: "integration pom", path: dir + "1.0-[INTEGRATION]/lib-1.0-[INTEGRATION].pom",
			want: dir + "1.0-20261017100000/lib-1.0-20261017100000.pom",
		},
		{
			name: "integration of another base",
			path: dir + "2.0-[INTEGRATION]/lib-2.0-[INTEGRATION].jar",
			want: dir + "2.0-20261017070000/lib-2.0-20261017070000.jar",
		},
		{
			name: "integration of a missing base",
			path: dir + "3.0-[INTEGRATION]/lib-3.0-[INTEGRATION].jar",
			want: dir + "3.0-[INTEGRATION]/lib-3.0-[INTEGRATION].jar",
		},
		{
			name: "release alphabetically",
			path: dir + "[RELEASE]/lib-[RELEASE].jar",
			want: dir + "1.9/lib-1.9.jar",
		},
		{
			name: "snapshot name", path: dir + "1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar",
			want: dir + "1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar",
		},
	})
}

// Of the releases with the path's coordinates, the first whose path holds
// the request with its token replaced wins, and otherwise the highest: in
// ivy-default a release's type folder is part of the path but not of the
// coordinates.
func TestResolvePrefersTheRequestedPath(t *testing.T) {
	w := newWorld(t, func(c *repo.Config) {
		c.PackageType, c.RepoLayoutRef = repo.Ivy, "ivy-default"
	})
	dir := "acme/lib/"
	w.deploy(dir+"2.0/bundles/lib-2.0.jar", dir+"1.0/jars/lib-1.0.jar")
	w.check([]resolveCase{
		{
			name: "the path asked for",
			path: dir + "[RELEASE]/jars/lib-[RELEASE].jar",
			want: dir + "1.0/jars/lib-1.0.jar",
		},
		{
			name: "the highest",
			path: dir + "[RELEASE]/wars/lib-[RELEASE].jar",
			want: dir + "2.0/bundles/lib-2.0.jar",
		},
	})
}

// JFrog resolves in local repositories and remote repositories' caches,
// and in a virtual repository through its members; a federated repository
// is local.
func TestResolveRepositories(t *testing.T) {
	dir := "com/acme/app/"
	req := format.Download{Path: dir + "[RELEASE]/app-[RELEASE].jar"}
	w := newWorld(t, func(c *repo.Config) { c.Class = repo.Federated })
	w.deploy(dir + "1.0/app-1.0.jar")
	if got := w.resolve(req); got != dir+"1.0/app-1.0.jar" {
		t.Errorf("federated: %s", got)
	}
	virtual := w.c
	virtual.Class = repo.Virtual
	if got, err := w.items.Resolve(w.ctx, virtual, req); err != nil || got != req.Path {
		t.Errorf("virtual: %s, %v", got, err)
	}

	w = newWorld(t, func(c *repo.Config) { c.Class, c.URL = repo.Remote, "https://repo.example" })
	if _, err := w.items.Deploy(w.ctx, item.Deploy{
		Target: item.Target{Repo: w.c, Cached: true, Path: dir + "1.0/app-1.0.jar"},
		Body:   strings.NewReader("cached"),
	}); err != nil {
		t.Fatal(err)
	}
	cached := req
	cached.Cached = true
	if got := w.resolve(cached); got != dir+"1.0/app-1.0.jar" {
		t.Errorf("cache: %s", got)
	}
	// A remote repository holds nothing of its own.
	if got := w.resolve(req); got != req.Path {
		t.Errorf("remote: %s", got)
	}
}

// A repository whose layout is not defined resolves nothing; a layout that
// cannot be read fails the download.
func TestResolveLayouts(t *testing.T) {
	w := newWorld(t, func(c *repo.Config) { c.RepoLayoutRef = "no-such-layout" })
	w.deploy("com/acme/app/1.0/app-1.0.jar")
	if got := w.resolve(format.Download{Path: "com/acme/app/[RELEASE]/app-[RELEASE].jar"}); got !=
		"com/acme/app/[RELEASE]/app-[RELEASE].jar" {
		t.Errorf("undefined layout: %s", got)
	}
	h := maven.Handler{}
	broken := format.Env{Store: w.db, Layouts: layouts{fail: true}}
	if _, err := h.Resolve(
		w.ctx,
		broken,
		w.c,
		format.Download{Path: "a/[RELEASE]/a-[RELEASE].jar"},
	); !errors.Is(
		err,
		errLayout,
	) {
		t.Errorf("broken layout: %v", err)
	}
}

// A search that fails fails the download.
func TestResolveSearchFailure(t *testing.T) {
	w := newWorld(t, nil)
	env := format.Env{Store: faulty{w.db, map[string]bool{"Search": true}}, Layouts: layouts{}}
	for _, p := range []string{
		"com/acme/app/[RELEASE]/app-[RELEASE].jar",
		"com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT.jar",
	} {
		if _, err := (maven.Handler{}).Resolve(
			w.ctx,
			env,
			w.c,
			format.Download{Path: p},
		); !errors.Is(
			err,
			errIndex,
		) {
			t.Errorf("%s: %v", p, err)
		}
	}
}
