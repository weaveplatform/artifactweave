package repo_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
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
		got := repo.PatternsOf(repo.Config{IncludesPattern: c.include, ExcludesPattern: c.exclude}).
			Accepts(c.path, c.folder)
		if got != c.expected {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// A request's path passes the patterns at the root and on a system path, as
// 7.161.26's RepoBase.accepts lets them through.
func TestUnitRepoPatternsAcceptsRequest(t *testing.T) {
	p := repo.PatternsOf(
		repo.Config{IncludesPattern: new("org/**"), ExcludesPattern: new("**/*.index")},
	)
	for _, c := range []struct {
		path             string
		folder, expected bool
	}{
		{path: "", expected: true},
		{path: "releases/p2.index", expected: true},
		{path: ".index/nexus-maven-repository-index.gz", expected: true},
		{path: "org/a.jar", expected: true},
		{path: "org", folder: true, expected: true},
		{path: "com/a.jar"},
		{path: "com/index.gz"},
	} {
		if got := p.AcceptsRequest(c.path, c.folder); got != c.expected {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
