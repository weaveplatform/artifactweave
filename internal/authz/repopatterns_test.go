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
		folder, expected bool
	}{
		{path: "a/b.jar", expected: true},
		{include: ptr(""), path: "a/b.jar", expected: true},
		{include: ptr("org/**, com/acme/**"), path: "com/acme/x.jar", expected: true},
		{include: ptr("org/**"), path: "com/x.jar"},
		{include: ptr("org/acme/**"), path: "org", folder: true, expected: true},
		{include: ptr("org/acme/**"), path: "org", expected: false},
		{exclude: ptr("**/*.tmp"), path: "a/b.tmp"},
		{include: ptr("**"), exclude: ptr(" , secret/**"), path: "secret/k", expected: false},
	} {
		got := PatternsOf(repo.Config{IncludesPattern: c.include, ExcludesPattern: c.exclude}).
			Accepts(c.path, c.folder)
		if got != c.expected {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
