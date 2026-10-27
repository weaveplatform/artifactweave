package authz

import (
	"strings"
	"testing"
)

// The examples from Ant's pattern documentation, which Artifactory's
// include and exclude patterns follow, plus Artifactory's usual forms.
func TestUnitPatternMatch(t *testing.T) {
	for _, c := range []struct {
		pattern string
		yes     []string
		no      []string
	}{
		{"**", []string{"", "a", "a/b/c.txt"}, nil},
		{
			"**/CVS/*",
			[]string{"CVS/Repository", "org/apache/CVS/Entries", "org/apache/jakarta/tools/ant/CVS/Entries"},
			[]string{"org/apache/CVS/foo/bar/Entries", "CVS"},
		},
		{
			"org/apache/jakarta/**",
			[]string{"org/apache/jakarta/tools/ant/docs/index.html", "org/apache/jakarta/test.xml", "org/apache/jakarta"},
			[]string{"org/apache/xyz.java", "org/apache/jakartas/x"},
		},
		{
			"org/apache/**/CVS/*",
			[]string{"org/apache/CVS/Entries", "org/apache/jakarta/tools/ant/CVS/Entries"},
			[]string{"org/apache/CVS/foo/bar/Entries"},
		},
		{"**/test/**", []string{"test", "a/test/b", "a/b/test/c/d.txt", "test/x"}, []string{"a/testing/b", "a/b"}},
		{"*.java", []string{"Foo.java", ".java"}, []string{"a/Foo.java", "Foo.java5"}},
		{"?.java", []string{"x.java", "é.java"}, []string{".java", "xy.java"}},
		{"**/*-sources.*", []string{"a/b/lib-1.0-sources.jar", "lib-sources.zip"}, []string{"a/lib.jar"}},
		{"**/*.jar", []string{"x.jar", "org/acme/lib/x.jar"}, []string{"org/acme/x.jarx", "x.JAR"}},
		{"org/**/*.jar", []string{"org/x.jar", "org/a/b/x.jar"}, []string{"com/x.jar"}},
		{"dir/", []string{"dir", "dir/a", "dir/a/b"}, []string{"dirx/a"}},
		{"/abs/*", []string{"abs/x"}, []string{"abs/x/y"}},
		{"a/**/**/b", []string{"a/b", "a/x/b", "a/x/y/b"}, []string{"a/x/c"}},
		{"*a*b*", []string{"ab", "xaybz", "aab"}, []string{"ba", "a"}},
		{"", []string{"anything/at/all"}, nil},
	} {
		p := CompilePattern(c.pattern)
		for _, y := range c.yes {
			if !p.Match(y) {
				t.Errorf("%q should match %q", c.pattern, y)
			}
		}
		for _, n := range c.no {
			if p.Match(n) {
				t.Errorf("%q should not match %q", c.pattern, n)
			}
		}
	}
}

func TestUnitPatternMatchBelow(t *testing.T) {
	for _, c := range []struct {
		pattern, folder string
		expected        bool
	}{
		{"org/acme/**", "", true},
		{"org/acme/**", "org", true},
		{"org/acme/**", "org/acme", true},
		{"org/acme/**", "org/other", false},
		{"org/acme/lib.jar", "org/acme", true},
		{"org/acme/lib.jar", "org/acme/lib.jar", false},
		{"*/acme/*", "x", true},
		{"**/x", "a/b/c", true},
		{"a", "a/b", false},
	} {
		if got := CompilePattern(c.pattern).MatchBelow(c.folder); got != c.expected {
			t.Errorf("%q below %q = %v, want %v", c.pattern, c.folder, got, c.expected)
		}
	}
}

// naive is a direct recursive Ant matcher, exponential but obviously
// right, used to check the table-driven one.
func naive(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(path); i++ {
			if naive(pat[1:], path[i:]) {
				return true
			}
		}
		return false
	}
	return len(path) > 0 && naiveSegment([]rune(pat[0]), []rune(path[0])) &&
		naive(pat[1:], path[1:])
}

func naiveSegment(p, s []rune) bool {
	if len(p) == 0 {
		return len(s) == 0
	}
	switch p[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if naiveSegment(p[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return len(s) > 0 && naiveSegment(p[1:], s[1:])
	default:
		return len(s) > 0 && p[0] == s[0] && naiveSegment(p[1:], s[1:])
	}
}

func FuzzPatternMatch(f *testing.F) {
	for _, seed := range [][2]string{
		{"**", "a/b"},
		{"a/**/b", "a/x/b"},
		{"*a?", "ba/x"},
		{"**/*.jar", "x.jar"},
		{"a/", "a"},
		{"?é*", "éé"},
		{"**/**/a", "a"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		if len(pattern) > 24 || len(path) > 24 || strings.Contains(path, "//") {
			return
		}
		p := CompilePattern(pattern)
		got := p.Match(path)
		if want := naive(p.segs, splitPath(path)); got != want {
			t.Fatalf("Match(%q, %q) = %v, naive says %v", pattern, path, got, want)
		}
		if got && !CompilePattern("**").Match(path) {
			t.Fatalf("** must match %q", path)
		}
	})
}
