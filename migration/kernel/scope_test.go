package kernel_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/migration/kernel"
)

func TestUnitParseScope(t *testing.T) {
	cases := []struct {
		in        string
		want      []kernel.ScopeEntry
		canonical string
	}{
		{"applied-permissions/user", []kernel.ScopeEntry{{Kind: kernel.ScopeUser}}, ""},
		{"applied-permissions/admin", []kernel.ScopeEntry{{Kind: kernel.ScopeAdmin}}, ""},
		{
			`applied-permissions/groups:"g1","g 2"`,
			[]kernel.ScopeEntry{{Kind: kernel.ScopeGroups, Names: []string{"g1", "g 2"}}},
			`applied-permissions/groups:g1,"g 2"`,
		},
		{
			`applied-permissions/roles:proj:developer,"Project Admin"`,
			[]kernel.ScopeEntry{
				{
					Kind:    kernel.ScopeRoles,
					Project: "proj",
					Names:   []string{"developer", "Project Admin"},
				},
			},
			"",
		},
		{
			"artifact:npm-local/**:r,w",
			[]kernel.ScopeEntry{
				{
					Kind:    kernel.ScopeArtifact,
					Target:  "npm-local",
					Path:    "**",
					Actions: kernel.ScopeRead | kernel.ScopeWrite,
				},
			},
			"",
		},
		{
			"artifact:*:*",
			[]kernel.ScopeEntry{
				{Kind: kernel.ScopeArtifact, Target: "*", Actions: kernel.ScopeAll},
			},
			"",
		},
		{
			"artifact:libs:m,s,x,a,d,w,r",
			[]kernel.ScopeEntry{
				{
					Kind:   kernel.ScopeArtifact,
					Target: "libs",
					Actions: kernel.ScopeRead | kernel.ScopeWrite |
						kernel.ScopeDelete | kernel.ScopeAnnotate | kernel.ScopeExecute | kernel.ScopeScan | kernel.ScopeManage,
				},
			},
			"artifact:libs:r,w,d,a,x,s,m",
		},
		{
			"repo:libs:r",
			[]kernel.ScopeEntry{
				{Kind: kernel.ScopeRepo, Target: "libs", Actions: kernel.ScopeRead},
			},
			"",
		},
		{
			"build:artifactory-build-info/app/**:r",
			[]kernel.ScopeEntry{{
				Kind: kernel.ScopeBuild, Target: "artifactory-build-info", Path: "app/**",
				Actions: kernel.ScopeRead,
			}},
			"",
		},
		{
			"project:proj/members/users:r",
			[]kernel.ScopeEntry{
				{
					Kind:    kernel.ScopeProject,
					Project: "proj",
					Target:  "members/users",
					Actions: kernel.ScopeRead,
				},
			},
			"",
		},
		{
			"system:metrics:r",
			[]kernel.ScopeEntry{
				{Kind: kernel.ScopeSystem, Target: "metrics", Actions: kernel.ScopeRead},
			},
			"",
		},
		{
			"system:webhooks@proj/https://x.jfrog.io:r,w,d,x",
			[]kernel.ScopeEntry{
				{
					Kind:      kernel.ScopeSystem,
					Target:    "webhooks",
					Qualifier: "proj/https://x.jfrog.io",
					Actions:   kernel.ScopeRead | kernel.ScopeWrite | kernel.ScopeDelete | kernel.ScopeExecute,
				},
			},
			"",
		},
		{
			"  applied-permissions/groups:readers   artifact:npm-local/**:r,w ",
			[]kernel.ScopeEntry{
				{Kind: kernel.ScopeGroups, Names: []string{"readers"}},
				{
					Kind:    kernel.ScopeArtifact,
					Target:  "npm-local",
					Path:    "**",
					Actions: kernel.ScopeRead | kernel.ScopeWrite,
				},
			},
			"applied-permissions/groups:readers artifact:npm-local/**:r,w",
		},
	}
	for _, c := range cases {
		got, err := kernel.ParseScope(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if diff := cmp.Diff(c.want, got.Entries); diff != "" {
			t.Errorf("%q (-want +got):\n%s", c.in, diff)
		}
		want := c.canonical
		if want == "" {
			want = c.in
		}
		if got.String() != want {
			t.Errorf("%q renders %q, want %q", c.in, got.String(), want)
		}
	}
}

func TestUnitParseScopeErrors(t *testing.T) {
	for _, in := range []string{
		"", "   ", strings.Repeat("a", kernel.MaxScopeLength+1),
		"applied-permissions/everyone", "applied-permissions/groups:", `applied-permissions/groups:"open`,
		`applied-permissions/groups:"a"b`, `applied-permissions/groups:a,,b`, `applied-permissions/groups:a,`,
		`applied-permissions/groups:a"b`, "applied-permissions/roles:proj", "applied-permissions/roles::dev",
		`applied-permissions/roles:p"q:dev`, "applied-permissions/roles:p:",
		`artifact:"unterminated:r`, "artifact:libs", "artifact:libs:", "artifact:libs:q", "artifact:libs:rw",
		"artifact::r", "build::r", "repo::r", "project:proj:r", "project:proj/:r", "project:/area:r", "system:@x:r",
		"widget:x:r", "nocolons", "artifact:\xff:r",
	} {
		if _, err := kernel.ParseScope(in); !errors.Is(err, kernel.ErrInvalidScope) {
			t.Errorf("%q: want ErrInvalidScope, got %v", in, err)
		}
	}
}

func TestUnitScopeHelpers(t *testing.T) {
	if kernel.UserScope().String() != "applied-permissions/user" {
		t.Error("UserScope")
	}
	admin, _ := kernel.ParseScope("artifact:x:r applied-permissions/admin")
	if !admin.IsAdmin() || kernel.UserScope().IsAdmin() {
		t.Error("IsAdmin")
	}
	if !kernel.ScopeAll.Covers(kernel.ScopeRead) || kernel.ScopeRead.Covers(kernel.ScopeAll) ||
		!(kernel.ScopeRead | kernel.ScopeWrite).Covers(
			kernel.ScopeRead,
		) || kernel.ScopeRead.Covers(kernel.ScopeWrite) {
		t.Error("Covers")
	}
}

func TestUnitScopeNarrows(t *testing.T) {
	cases := []struct {
		child, parent string
		want          bool
	}{
		{"artifact:libs:r", "applied-permissions/admin", true},
		{"applied-permissions/admin", "applied-permissions/user", false},
		{"applied-permissions/user", "applied-permissions/user", true},
		{"applied-permissions/user", "applied-permissions/groups:g", false},
		{"applied-permissions/groups:a", `applied-permissions/groups:a,b`, true},
		{"applied-permissions/groups:c", `applied-permissions/groups:a,b`, false},
		{"applied-permissions/roles:p:dev", "applied-permissions/roles:p:dev,ops", true},
		{"applied-permissions/roles:q:dev", "applied-permissions/roles:p:dev", false},
		{"artifact:libs:r", "applied-permissions/user", true},
		{"repo:libs:r", "applied-permissions/groups:g", true},
		{"project:p/roles:r", "applied-permissions/roles:p:dev", true},
		{"system:metrics:r", "applied-permissions/user", false},
		{"system:metrics:r", "system:metrics:*", true},
		{"system:info/licenses:r", "system:info:r", true},
		{"system:info:r", "system:info/licenses:r", false},
		{"system:webhooks@p/u:r", "system:webhooks:r,w", true},
		{"system:webhooks@p/u:r", "system:webhooks@q/u:r", false},
		{"system:metrics:w", "system:metrics:r", false},
		{"artifact:libs/org/a.jar:r", "artifact:libs/org/**:r,w", true},
		{"artifact:libs/org:r", "artifact:libs/org/**:r", true},
		{"artifact:libs/other/a.jar:r", "artifact:libs/org/**:r", false},
		{"artifact:libs:r", "artifact:libs/org/**:r", false},
		{"artifact:libs/x:r", "artifact:libs/y:r", false},
		{"artifact:libs/x:r", "artifact:libs/x:r", true},
		{"artifact:libs:r", "artifact:*:r", true},
		{"artifact:libs/a:r", "artifact:*/**:r", true},
		{"artifact:libs:r", "artifact:other:r", false},
		{"artifact:libs:w", "artifact:libs:r", false},
		{"artifact:libs:*", "artifact:libs:r,w,d,a,x,s,m", false},
		{"artifact:libs:r", "repo:libs:r", false},
		{"build:artifactory-build-info/app/**:r", "build:artifactory-build-info/**:r", true},
		{"build:artifactory-build-info/**:r", "build:artifactory-build-info/app/**:r", false},
		{"build:artifactory-build-info:r", "artifact:artifactory-build-info:r", false},
		{"build:artifactory-build-info:r", "applied-permissions/user", true},
		{"repo:libs:r", "repo:*:r", true},
		{"repo:libs:r", "repo:other:r", false},
		{"project:p/members/users:r", "project:p/members:r", true},
		{"project:p/roles:r", "project:q/roles:r", false},
		{"artifact:a:r artifact:b:r", "artifact:a:r", false},
	}
	for _, c := range cases {
		child, err := kernel.ParseScope(c.child)
		if err != nil {
			t.Fatalf("%q: %v", c.child, err)
		}
		parent, err := kernel.ParseScope(c.parent)
		if err != nil {
			t.Fatalf("%q: %v", c.parent, err)
		}
		if got := child.Narrows(parent); got != c.want {
			t.Errorf("%q narrows %q = %v, want %v", c.child, c.parent, got, c.want)
		}
	}
}

// FuzzParseScope checks that anything the parser accepts renders to a
// canonical string that parses back to the same scope, never grows past the
// length limit, and narrows itself.
func FuzzParseScope(f *testing.F) {
	for _, s := range []string{
		"applied-permissions/user", `applied-permissions/groups:"a b",c`, "applied-permissions/roles:p:dev",
		"artifact:libs/a/**:r,w", "artifact:*:*", "repo:x:r", "project:p/roles:r",
		"system:webhooks@p/https://h:r", `artifact:"a b":r`, "applied-permissions/admin artifact:x:r",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		sc, err := kernel.ParseScope(s)
		if err != nil {
			return
		}
		out := sc.String()
		if len(out) > kernel.MaxScopeLength {
			t.Fatalf("%q renders %d characters", s, len(out))
		}
		again, err := kernel.ParseScope(out)
		if err != nil {
			t.Fatalf("%q renders %q which does not parse: %v", s, out, err)
		}
		if diff := cmp.Diff(sc, again); diff != "" {
			t.Fatalf("%q -> %q changed (-first +second):\n%s", s, out, diff)
		}
		if !sc.Narrows(sc) {
			t.Fatalf("%q does not narrow itself", s)
		}
	})
}
