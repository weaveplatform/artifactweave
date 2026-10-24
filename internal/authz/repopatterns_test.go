package authz

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitRepoPatterns(t *testing.T) {
	ptr := func(s string) *string { return &s }
	for _, c := range []struct {
		include, exclude *string
		path             string
		folder, want     bool
	}{
		{path: "a/b.jar", want: true},
		{include: ptr(""), path: "a/b.jar", want: true},
		{include: ptr("org/**, com/acme/**"), path: "com/acme/x.jar", want: true},
		{include: ptr("org/**"), path: "com/x.jar"},
		{include: ptr("org/acme/**"), path: "org", folder: true, want: true},
		{include: ptr("org/acme/**"), path: "org", want: false},
		{exclude: ptr("**/*.tmp"), path: "a/b.tmp"},
		{include: ptr("**"), exclude: ptr(" , secret/**"), path: "secret/k", want: false},
	} {
		got := PatternsOf(repo.Config{IncludesPattern: c.include, ExcludesPattern: c.exclude}).
			Accepts(c.path, c.folder)
		if got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
