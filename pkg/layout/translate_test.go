package layout_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

func bundled(t *testing.T, name string) *layout.Layout {
	t.Helper()
	l, err := layout.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// A translation lands a path where the target layout builds it, unless
// the target has no place for a value of the source path (#496): then two
// source paths could take one target path, and the translation is refused
// with the values it would lose.
func TestUnitTranslate(t *testing.T) {
	for _, c := range []struct {
		from, to, path string
		want           string
		lost           []string
	}{
		// go-default's file revision has no place in simple-default, so a
		// pre-release would take its release's path; the release fits.
		{
			from: "go-default", to: "simple-default", path: "example.com/lib/@v/v1.2.3-rc.1.zip",
			lost: []string{"[fileItegRev] rc.1"},
		},
		{
			from: "go-default", to: "simple-default", path: "example.com/lib/@v/v1.2.3.zip",
			want: "example.com/lib/lib-1.2.3.zip",
		},
		{
			from: "simple-default", to: "go-default", path: "example.com/lib/lib-1.2.3.zip",
			want: "example.com/lib/@v/v1.2.3.zip",
		},
		// A pseudo-version's timestamp and commit are its file revision.
		{
			from: "go-default", to: "nix-default",
			path: "example.com/lib/@v/v0.0.0-20200101120000-abcdef123456.mod",
			lost: []string{"[fileItegRev] 20200101120000-abcdef123456"},
		},
		// maven-2-default's file revision is SNAPSHOT or a timestamp, so it
		// would read rc.1 as a classifier.
		{
			from: "go-default", to: "maven-2-default", path: "example.com/lib/@v/v1.2.3-rc.1.zip",
			lost: []string{"[fileItegRev] rc.1"},
		},
		// Maven's classifier and snapshot revisions.
		{
			from: "maven-2-default", to: "simple-default",
			path: "com/example/my-library/1.0/my-library-1.0-sources.jar",
			lost: []string{"[classifier] sources"},
		},
		{
			from: "maven-2-default", to: "simple-default",
			path: "com/example/my-library/1.0-SNAPSHOT/my-library-1.0-SNAPSHOT.jar",
			lost: []string{"[folderItegRev] SNAPSHOT", "[fileItegRev] SNAPSHOT"},
		},
		// simple-default's base revision stops at the first dot it can, so
		// it reads 7.0.2.v20100331.jar as 7.0.2 with the extension
		// v20100331.jar, which the Maven path .../7.0.2/jetty-ajp-7.0.2.v20100331.jar
		// gives as well.
		{
			from: "maven-2-default", to: "simple-default",
			path: "org/eclipse/jetty/jetty-ajp/7.0.2.v20100331/jetty-ajp-7.0.2.v20100331.jar",
			lost: []string{"[baseRev] 7.0.2.v20100331", "[ext] jar"},
		},
		// Ivy's and Maven 1's type folder.
		{
			from: "maven-1-default", to: "maven-2-default",
			path: "commons-lang/jars/commons-lang-2.1.jar",
			lost: []string{"[type] jar"},
		},
		// cargo-default names no organisation.
		{
			from: "simple-default", to: "cargo-default", path: "rust/serde/serde-1.0.197.crate",
			lost: []string{"[orgPath] rust"},
		},
		// gradle-default's organisation is one folder, so example.com/lib
		// and example/com/lib would both be example.com.
		{
			from: "go-default", to: "gradle-default", path: "example.com/lib/@v/v1.2.3.zip",
			lost: []string{"[orgPath] example.com"},
		},
		// npm-default, nuget-default and puppet-default fix the extension.
		{
			from: "go-default", to: "npm-default", path: "example.com/lib/@v/v1.2.3.zip",
			lost: []string{"[ext] zip"},
		},
		// vcs-default's tags and branches.
		{
			from: "vcs-default", to: "maven-2-default",
			path: "jfrog/jfrog-cli/tags/v2.50.0/jfrog-cli-v2.50.0.tar.gz",
			lost: []string{"[refs] tags"},
		},
		// Descriptors go through the descriptor patterns, both ways.
		{
			from: "maven-2-default", to: "gradle-default",
			path: "com/example/my-library/1.0/my-library-1.0.pom",
			want: "com.example/my-library/ivy-1.0.xml",
		},
		// gradle-default's descriptor has no folder revision, and its file
		// revision is a 14-digit timestamp, so it reads Maven's as part of
		// the base revision.
		{
			from: "maven-2-default", to: "gradle-default",
			path: "com/example/my-library/1.0-SNAPSHOT/my-library-1.0-20240101.120000-3.pom",
			lost: []string{
				"[baseRev] 1.0", "[folderItegRev] SNAPSHOT", "[fileItegRev] 20240101.120000-3",
			},
		},
		// A file the source does not take as a descriptor may read as one in
		// the target, and still lead back.
		{
			from: "simple-default", to: "maven-2-default", path: "a/b/b-1.0.pom",
			want: "a/b/1.0/b-1.0.pom",
		},
		{
			from: "maven-2-default", to: "gradle-default",
			path: "com/example/my-library/1.0/my-library-1.0-sources.jar",
			want: "com.example/my-library/1.0/my-library-1.0-sources.jar",
		},
	} {
		t.Run(c.from+" to "+c.to+" "+c.path, func(t *testing.T) {
			got, err := layout.Translate(bundled(t, c.from), bundled(t, c.to), c.path)
			if c.lost == nil {
				if err != nil || got != c.want {
					t.Fatalf("%q %v, want %q", got, err, c.want)
				}
				return
			}
			var loss *layout.LossError
			if !errors.As(err, &loss) || !errors.Is(err, layout.ErrLossy) {
				t.Fatalf("%q %v, want a loss", got, err)
			}
			if diff := cmp.Diff(c.lost, loss.Lost); diff != "" {
				t.Fatal(diff)
			}
			if loss.From != c.from || loss.To != c.to || loss.Path != c.path || loss.Target == "" {
				t.Fatalf("%+v", loss)
			}
		})
	}
}

// A path the source does not read, or the target cannot build, is no
// loss: the caller keeps it as it is.
func TestUnitTranslateKeeps(t *testing.T) {
	for _, c := range []struct {
		from, to, path string
		want           error
	}{
		{"maven-2-default", "simple-default", "notes.txt", layout.ErrNoMatch},
		// ivy-default needs a type Maven does not give.
		{
			"maven-2-default", "ivy-default", "com/example/my-library/1.0/my-library-1.0.jar",
			layout.ErrMissingValue,
		},
		// simple-default has no descriptor pattern.
		{
			"maven-2-default", "simple-default", "com/example/my-library/1.0/my-library-1.0.pom",
			layout.ErrPattern,
		},
		// swift-default drops every token, so it would build one path for
		// every file (#505).
		{"simple-default", "swift-default", "acme/tool/tool-1.0.zip", layout.ErrUnbuildable},
		{
			"maven-2-default", "ansible-default", "com/example/my-library/1.0/my-library-1.0.jar",
			layout.ErrUnbuildable,
		},
	} {
		if _, err := layout.Translate(
			bundled(t, c.from), bundled(t, c.to), c.path,
		); !errors.Is(err, c.want) || errors.Is(err, layout.ErrLossy) {
			t.Errorf("%s to %s %s: %v, want %v", c.from, c.to, c.path, err, c.want)
		}
	}
}

// The error says what is lost, which other path takes the target, or that
// the target layout does not read its own path back.
func TestUnitLossErrorText(t *testing.T) {
	gomod := bundled(t, "go-default")
	_, err := layout.Translate(gomod, bundled(t, "simple-default"),
		"example.com/lib/@v/v1.2.3-rc.1.zip")
	if err == nil || err.Error() != "layout simple-default has no place for [fileItegRev] rc.1 "+
		"from layout go-default, so another path could translate to example.com/lib/lib-1.2.3.zip" {
		t.Fatalf("%v", err)
	}
	// puppet-default repeats the organisation in the file name, where a
	// slash cannot go, so it does not read what it built.
	_, err = layout.Translate(gomod, bundled(t, "puppet-default"),
		"example.com/acme/lib/@v/v1.0.0.info")
	var loss *layout.LossError
	if !errors.As(err, &loss) || loss.Lost != nil || err.Error() != "layout puppet-default "+
		"does not read example.com/acme/lib/example.com/acme-lib-1.0.0.tar.gz back as the path "+
		"from layout go-default" {
		t.Fatalf("%v", err)
	}

	// Two shapes of the source, its artifact pattern and its descriptor
	// pattern, that the target builds alike: each names the other.
	from := compile(t, layout.Definition{
		Name: "from", ArtifactPathPattern: "[org]/[module]-[baseRev].[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/desc/[module]-[baseRev].xml",
	})
	to := compile(t, layout.Definition{
		Name: "to", ArtifactPathPattern: "[org]/[module]/[baseRev].[ext]",
		DistinctiveDescriptorPathPattern: true,
		DescriptorPathPattern:            "[org]/[module]/[baseRev].xml",
	})
	for path, rival := range map[string]string{
		"o/m-1.xml":      "o/desc/m-1.xml",
		"o/desc/m-1.xml": "o/m-1.xml",
	} {
		_, err := layout.Translate(from, to, path)
		if !errors.As(err, &loss) || loss.Rival != rival ||
			err.Error() != "layout to gives "+rival+" the same path, o/m/1.xml" {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func compile(t *testing.T, d layout.Definition) *layout.Layout {
	t.Helper()
	l, err := layout.Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// translations are paths of each bundled layout that differ only in what
// some target layout has no place for: releases beside pre-releases,
// snapshots, classifiers and types, and the real paths above.
var translations = map[string][]string{
	"go-default": {
		"example.com/lib/@v/v1.2.3.zip", "example.com/lib/@v/v1.2.3-rc.1.zip",
		"example.com/lib/@v/v1.2.3-rc.2.zip", "example.com/lib/@v/v1.2.3.mod",
		"example.com/lib/@v/v1.2.3-rc.1.mod", "example/com/lib/@v/v1.2.3.zip",
		"example.com/lib/@v/v0.0.0-20200101120000-abcdef123456.zip",
		"example.com/lib/@v/v0.0.0-20200102120000-abcdef123456.zip",
		"example.com/lib/@v/v2.0.0+incompatible.zip",
	},
	"maven-2-default": {
		"com/example/lib/1.0/lib-1.0.jar", "com/example/lib/1.0/lib-1.0-sources.jar",
		"com/example/lib/1.0/lib-1.0.pom", "com/example/lib/1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar",
		"com/example/lib/1.0/lib-1.0-SNAPSHOT.jar",
		"com/example/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-3.jar",
		"com/example/lib/1.0-SNAPSHOT/lib-1.0-20240101.120000-4.jar",
		"com/example/lib/1.0.v1/lib-1.0.v1.jar", "com/example/lib/1.0/lib-1.0.v1.jar",
		"com.example/lib/1.0/lib-1.0.jar",
	},
	"simple-default": {
		"acme/tool/tool-1.0.zip", "acme/tool/tool-1.0.tar", "other/tool/tool-1.0.zip",
		"a/b/b-1.0.pom", "example.com/lib/lib-1.2.3.zip",
	},
	"ivy-default": {
		"org.acme/app/1.0/jars/app-1.0.jar", "org.acme/app/1.0/bundles/app-1.0.jar",
		"org.acme/app/1.0/ivys/ivy-1.0.xml",
	},
	"npm-default": {
		"lodash/-/lodash-4.17.21.tgz", "@scope/name/-/@scope/name-1.0.0.tgz",
		"@scope/name/-/name-1.0.0.tgz",
	},
	"vcs-default": {
		"acme/tool/tags/v1/tool-v1.tar.gz", "acme/tool/branches/v1/tool-v1.tar.gz",
	},
}

// No translation between bundled layouts lands two source paths on one
// target path, for every ordered pair.
func TestUnitTranslateNeverMerges(t *testing.T) {
	corpus := map[string][]string{}
	for name, paths := range translations {
		corpus[name] = append(corpus[name], paths...)
	}
	for _, r := range realPaths {
		corpus[r.layout] = append(corpus[r.layout], r.path)
	}
	refused := 0
	for _, a := range layout.Bundled() {
		from := bundled(t, a.Name)
		for _, b := range layout.Bundled() {
			to := bundled(t, b.Name)
			landed := map[string]string{}
			for _, p := range corpus[a.Name] {
				out, err := layout.Translate(from, to, p)
				if errors.Is(err, layout.ErrLossy) {
					refused++
					continue
				}
				if err != nil {
					continue
				}
				if other, ok := landed[out]; ok && other != p {
					t.Errorf("%s to %s: %s and %s both translate to %s",
						a.Name, b.Name, other, p, out)
				}
				landed[out] = p
			}
		}
	}
	if refused == 0 {
		t.Fatal("the corpus has no path any layout would lose")
	}
}
