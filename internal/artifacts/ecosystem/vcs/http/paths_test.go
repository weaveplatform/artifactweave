package vcshttp

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func TestUnitVCSStashProviderPaths(t *testing.T) {
	t.Parallel()
	c := repo.Config{URL: "https://stash.example/bitbucket/", VCSGitProvider: repo.Stash}
	refs, err := GitRefsURL(c, "PRJ", "project")
	if err != nil ||
		refs.String() != "https://stash.example/bitbucket/scm/PRJ/project.git/info/refs?service=git-upload-pack" {
		t.Fatalf("refs URL = %v, %v", refs, err)
	}
	for _, ext := range []string{"tar.gz", "zip"} {
		t.Run(ext, func(t *testing.T) {
			u, err := ArchiveURL(c, "PRJ", "project", "feature/one", ext)
			want := "https://stash.example/bitbucket/rest/api/1.0/projects/PRJ/repos/project/archive?at=feature%2Fone&format=" + ext
			if err != nil || u.String() != want {
				t.Fatalf("archive URL = %v, %v; want %s", u, err, want)
			}
		})
	}
	if _, err := GitRefsURL(c, "team/subgroup", "project"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("nested Stash project accepted: %v", err)
	}
	if _, err := ArchiveURL(
		c,
		"team/subgroup",
		"project",
		"main",
		"zip",
	); !errors.Is(
		err,
		ErrInvalidPath,
	) {
		t.Fatalf("nested Stash archive accepted: %v", err)
	}
}

func TestUnitVCSOldStashRetainsPluginServletContract(t *testing.T) {
	c := repo.Config{URL: "https://stash.example/context", VCSGitProvider: repo.OldStash}
	u, err := GitRefsURL(c, "PRJ", "project")
	if err != nil ||
		u.String() != "https://stash.example/context/scm/PRJ/project.git/info/refs?service=git-upload-pack" {
		t.Fatalf("OLDSTASH refs = %v %v", u, err)
	}
	for _, ext := range []string{"zip", "tar.gz"} {
		t.Run(ext, func(t *testing.T) {
			u, err := ArchiveURL(c, "PRJ", "project", "release/one", ext)
			if err != nil ||
				u.String() != "https://stash.example/context/plugins/servlet/archive/projects/PRJ/repos/project?at=release%2Fone&format="+ext {
				t.Fatalf("OLDSTASH archive = %v %v", u, err)
			}
		})
	}
}
