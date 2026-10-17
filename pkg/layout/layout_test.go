package layout_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

func mustLookup(t *testing.T, name string) *layout.Layout {
	t.Helper()
	l, err := layout.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestBundledCompile(t *testing.T) {
	defs := layout.Bundled()
	if len(defs) != 21 {
		t.Fatalf("expected 21 bundled layouts, got %d", len(defs))
	}
	for _, d := range defs {
		l, err := layout.Compile(d)
		if err != nil {
			t.Errorf("%s: %v", d.Name, err)
			continue
		}
		if l.Definition().Name != d.Name {
			t.Errorf("definition lost: %+v", l.Definition())
		}
	}
	defs[0].Name = "mutated"
	if layout.Bundled()[0].Name == "mutated" {
		t.Error("Bundled must return a copy")
	}
	if _, err := layout.Lookup("missing"); !errors.Is(err, layout.ErrPattern) {
		t.Errorf("unknown layout: %v", err)
	}
}

func TestMavenMatch(t *testing.T) {
	l := mustLookup(t, "maven-2-default")
	tests := []struct {
		path string
		want layout.Values
		desc bool
	}{
		{
			"org/apache/commons/commons-lang3/3.12.0/commons-lang3-3.12.0.jar",
			layout.Values{
				"orgPath": "org/apache/commons",
				"org":     "org.apache.commons",
				"module":  "commons-lang3",
				"baseRev": "3.12.0",
				"ext":     "jar",
			},
			false,
		},
		{
			"org/apache/commons/commons-lang3/3.12.0/commons-lang3-3.12.0-sources.jar",
			layout.Values{
				"orgPath":    "org/apache/commons",
				"org":        "org.apache.commons",
				"module":     "commons-lang3",
				"baseRev":    "3.12.0",
				"classifier": "sources",
				"ext":        "jar",
			},
			false,
		},
		{
			"com/acme/app/1.0-SNAPSHOT/app-1.0-20240101.120000-3.jar",
			layout.Values{
				"orgPath":       "com/acme",
				"org":           "com.acme",
				"module":        "app",
				"baseRev":       "1.0",
				"folderItegRev": "SNAPSHOT",
				"fileItegRev":   "20240101.120000-3",
				"ext":           "jar",
			},
			false,
		},
		{
			"com/acme/app/1.0-SNAPSHOT/app-1.0-SNAPSHOT-tests.jar",
			layout.Values{
				"orgPath":       "com/acme",
				"org":           "com.acme",
				"module":        "app",
				"baseRev":       "1.0",
				"folderItegRev": "SNAPSHOT",
				"fileItegRev":   "SNAPSHOT",
				"classifier":    "tests",
				"ext":           "jar",
			},
			false,
		},
		{"com/acme/app/1.0/app-1.0.pom", layout.Values{
			"orgPath": "com/acme", "org": "com.acme", "module": "app", "baseRev": "1.0",
		}, true},
		{
			"com/acme/app/1.0/app-1.0.tar.gz",
			layout.Values{
				"orgPath": "com/acme",
				"org":     "com.acme",
				"module":  "app",
				"baseRev": "1.0",
				"ext":     "tar.gz",
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := l.Match(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
			if l.IsDescriptor(tt.path) != tt.desc {
				t.Errorf("IsDescriptor = %v", !tt.desc)
			}
		})
	}
	if _, err := l.Match("not/a/maven/path"); !errors.Is(err, layout.ErrNoMatch) {
		t.Errorf("want ErrNoMatch, got %v", err)
	}
	if _, err := l.Match("com/acme/app/1.0/other-1.0.jar"); !errors.Is(err, layout.ErrNoMatch) {
		t.Errorf("module repeated token must agree: %v", err)
	}
}

func TestMavenBuild(t *testing.T) {
	l := mustLookup(t, "maven-2-default")
	tests := []struct {
		name string
		v    layout.Values
		want string
	}{
		{
			"release from org",
			layout.Values{
				"org":     "org.apache.commons",
				"module":  "commons-lang3",
				"baseRev": "3.12.0",
				"ext":     "jar",
			},
			"org/apache/commons/commons-lang3/3.12.0/commons-lang3-3.12.0.jar",
		},
		{
			"classifier from orgPath",
			layout.Values{
				"orgPath":    "com/acme",
				"module":     "app",
				"baseRev":    "1.0",
				"classifier": "sources",
				"ext":        "jar",
			},
			"com/acme/app/1.0/app-1.0-sources.jar",
		},
		{
			"unique snapshot",
			layout.Values{
				"orgPath":       "com/acme",
				"module":        "app",
				"baseRev":       "1.0",
				"folderItegRev": "SNAPSHOT",
				"fileItegRev":   "20240101.120000-3",
				"ext":           "jar",
			},
			"com/acme/app/1.0-SNAPSHOT/app-1.0-20240101.120000-3.jar",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := l.Build(tt.v)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %s want %s", got, tt.want)
			}
			back, err := l.Match(got)
			if err != nil {
				t.Fatalf("built path does not match: %v", err)
			}
			for k, v := range tt.v {
				if back[k] != v {
					t.Errorf("round trip lost %s=%s (got %s)", k, v, back[k])
				}
			}
		})
	}
	if _, err := l.Build(
		layout.Values{"module": "app", "baseRev": "1.0", "ext": "jar"},
	); !errors.Is(
		err,
		layout.ErrMissingValue,
	) {
		t.Errorf("missing org: %v", err)
	}
	pom, err := l.BuildDescriptor(
		layout.Values{"org": "com.acme", "module": "app", "baseRev": "1.0"},
	)
	if err != nil || pom != "com/acme/app/1.0/app-1.0.pom" {
		t.Errorf("descriptor: %s %v", pom, err)
	}
	simple := mustLookup(t, "simple-default")
	if _, err := simple.BuildDescriptor(layout.Values{}); !errors.Is(err, layout.ErrPattern) {
		t.Errorf("layout without descriptor: %v", err)
	}
	if simple.IsDescriptor("a/b/b-1.zip") {
		t.Error("simple layout has no descriptors")
	}
}

func TestOptionalGroupNesting(t *testing.T) {
	l, err := layout.Compile(
		layout.Definition{
			Name:                "nested",
			ArtifactPathPattern: "[module](-[baseRev](-[classifier])).[ext]",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		v    layout.Values
		want string
	}{
		{layout.Values{"module": "m", "ext": "jar"}, "m.jar"},
		{layout.Values{"module": "m", "baseRev": "1", "ext": "jar"}, "m-1.jar"},
		{layout.Values{"module": "m", "baseRev": "1", "classifier": "c", "ext": "jar"}, "m-1-c.jar"},
		{layout.Values{"module": "m", "classifier": "c", "ext": "jar"}, "m.jar"},
	} {
		got, err := l.Build(tt.v)
		if err != nil || got != tt.want {
			t.Errorf("%v: got %s %v want %s", tt.v, got, err, tt.want)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []string{
		"[module",
		"[nope]/[baseRev]",
		"([module]",
		"[module])",
		"[custom<abc]",
		"[<abc>]",
		"[x<(>]",
	}
	for _, p := range bad {
		if _, err := layout.Compile(
			layout.Definition{Name: "bad", ArtifactPathPattern: p},
		); !errors.Is(
			err,
			layout.ErrPattern,
		) {
			t.Errorf("%q: got %v", p, err)
		}
	}
	if _, err := layout.Compile(
		layout.Definition{
			Name:                  "bad",
			ArtifactPathPattern:   "[module]",
			DescriptorPathPattern: "[nope]",
		},
	); !errors.Is(
		err,
		layout.ErrPattern,
	) {
		t.Errorf("descriptor error not reported: %v", err)
	}
}

func TestOrgPath(t *testing.T) {
	if layout.OrgPath("org.apache.commons") != "org/apache/commons" ||
		layout.Org("org/apache/commons") != "org.apache.commons" {
		t.Error("org conversion")
	}
}

func TestHas(t *testing.T) {
	maven := mustLookup(t, "maven-2-default")
	for _, tok := range []string{"org", "orgPath", "module", "baseRev", "classifier", "ext"} {
		if !maven.Has(tok) {
			t.Errorf("maven lacks %s", tok)
		}
	}
	if maven.Has("type") {
		t.Error("maven has type")
	}
	if npm := mustLookup(t, "npm-default"); !npm.Has("org") || npm.Has("classifier") {
		t.Error("npm tokens")
	}
}

func TestPrefixes(t *testing.T) {
	tests := []struct {
		layout string
		v      layout.Values
		want   []string
	}{
		// The descriptor pattern shares the artifact pattern's start.
		{
			"maven-2-default",
			layout.Values{"org": "org.acme", "module": "lib"},
			[]string{"org/acme/lib/"},
		},
		{
			"maven-2-default",
			layout.Values{"orgPath": "org/*", "module": "lib", "baseRev": "1.0"},
			[]string{"org/*/lib/1.0"},
		},
		{"maven-2-default", layout.Values{"module": "lib"}, []string{""}},
		{
			"gradle-default",
			layout.Values{"org": "org.acme", "module": "lib", "baseRev": "2"},
			[]string{"org.acme/lib/2", "org.acme/lib/ivy-2"},
		},
		{"npm-default", layout.Values{"org": "@acme", "module": "ui"}, []string{"@acme/ui/ui-"}},
	}
	for _, tt := range tests {
		if got := mustLookup(t, tt.layout).Prefixes(tt.v); !cmp.Equal(got, tt.want) {
			t.Errorf("%s %v: %q, want %q", tt.layout, tt.v, got, tt.want)
		}
	}
}
