package terraformhttp

import (
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func TestUnitGitHubShorthandCombinesPathAndExplicitSelector(t *testing.T) {
	t.Parallel()
	c := repo.Config{URL: "https://api.github.com"}
	base := "https://registry.terraform.io/v1/modules/ns/name/aws/1.0.0/download"
	for _, tc := range []struct {
		name, raw, ref, subdir string
	}{
		{"repository", "github.com/org/repo", "HEAD", "*"},
		{"dot-git", "github.com/org/repo.git", "HEAD", "*"},
		{"path", "github.com/org/repo/modules/demo?ref=v1.0", "v1.0", "*/modules/demo"},
		{"dot-git-path", "github.com/org/repo.git/modules/demo?ref=v1.0", "v1.0", "*/modules/demo"},
		{"explicit-selector", "github.com/org/repo//modules/demo?ref=v1.0", "v1.0", "*/modules/demo"},
		{"combined-selector", "github.com/org/repo/modules//demo?ref=v1.0&depth=1", "v1.0", "*/modules/demo"},
		{"forced-git", "git::github.com/org/repo/modules/demo?ref=v1.0", "v1.0", "*/modules/demo"},
		{"branch-ref", "github.com/org/repo/modules/demo?ref=feature%2Fnative", "feature/native", "*/modules/demo"},
		{"qualified-tag-ref", "github.com/org/repo/modules/demo?ref=refs%2Ftags%2Fv1.0", "refs/tags/v1.0", "*/modules/demo"},
		{"commit", "github.com/org/repo/modules/demo?ref=" + strings.Repeat("a", 40), strings.Repeat("a", 40), "*/modules/demo"},
		{"commit-depth-keeps-snapshot-semantics", "github.com/org/repo/modules/demo?depth=1&ref=" + strings.Repeat("a", 40), strings.Repeat("a", 40), "*/modules/demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseModuleSource(tc.raw, base, c)
			if err != nil {
				t.Fatal(err)
			}
			if got.Archive != "tar.gz" || got.Target != "https://github.com/org/repo/archive/"+url.PathEscape(tc.ref)+".tar.gz" ||
				got.Subdir != tc.subdir || got.Path != "org/repo/tags/"+tc.ref+"/repo-"+tc.ref+".tar.gz" {
				t.Fatalf("shorthand did not preserve package/ref/selector identity: %+v", got)
			}
		})
	}
}

func TestUnitGitHubShorthandRejectsUnsafeAndUnqualifiedForms(t *testing.T) {
	t.Parallel()
	c := repo.Config{URL: "https://api.github.com"}
	base := "https://registry.terraform.io/v1/modules/ns/name/aws/1.0.0/download"
	for _, tc := range []struct{ name, raw string }{
		{"missing-repository", "github.com/org/"},
		{"missing-owner", "github.com//repo"},
		{"empty-repository", "github.com/org/.git/modules/demo"},
		{"parent-owner", "github.com/../repo/modules/demo"},
		{"parent-repository", "github.com/org/../modules/demo"},
		{"path-traversal", "github.com/org/repo/modules/../demo"},
		{"selector-traversal", "github.com/org/repo/modules//../demo"},
		{"encoded-path-traversal", "github.com/org/repo/modules/%2e%2e/demo"},
		{"encoded-separator", "github.com/org%2frepo/modules/demo"},
		{"double-encoded-selector", "github.com/org/repo/modules/%252e%252e/demo"},
		{"malformed-encoding", "github.com/org/repo/modules/%zz"},
		{"malformed-selector", "github.com/org/repo/modules//[bad"},
		{"fragment", "github.com/org/repo/modules/demo#fragment"},
		{"userinfo", "git::https://user:pass@github.com/org/repo//modules/demo"},
		{"http-force", "http::github.com/org/repo/modules/demo"},
		{"https-force", "https::github.com/org/repo/modules/demo"},
		{"duplicate-ref", "github.com/org/repo/modules/demo?ref=v1&ref=v2"},
		{"unsupported-query", "github.com/org/repo/modules/demo?token=secret"},
		{"absolute-git-extra-path", "git::https://github.com/org/repo/modules/demo"},
		{"other-schemeless-host", "gitlab.com/org/repo/modules/demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, err := parseModuleSource(tc.raw, base, c); err == nil {
				t.Fatalf("unsafe or unqualified shorthand admitted: %+v", got)
			}
		})
	}
}

func TestUnitGitHubShorthandRetainsDestinationPolicy(t *testing.T) {
	t.Parallel()
	base := "https://registry.terraform.io/v1/modules/ns/name/aws/1.0.0/download"
	raw := "github.com/org/repo/modules/demo?ref=v1"
	on := true
	for _, tc := range []struct {
		name    string
		config  repo.Config
		allowed bool
	}{
		{"unconfigured", repo.Config{URL: "https://storage.example"}, false},
		{"disabled-external", repo.Config{URL: "https://storage.example", ExternalDependenciesPatterns: []string{"https://github.com/org/**"}}, false},
		{"wrong-owner", repo.Config{URL: "https://storage.example", ExternalDependenciesEnabled: &on, ExternalDependenciesPatterns: []string{"https://github.com/other/**"}}, false},
		{"allowed-owner", repo.Config{URL: "https://storage.example", ExternalDependenciesEnabled: &on, ExternalDependenciesPatterns: []string{"https://github.com/org/**"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseModuleSource(raw, base, tc.config)
			if (err == nil) != tc.allowed {
				t.Fatalf("destination policy changed: allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}

func TestUnitGitHubSnapshotRefGrammarRejectsRevisionExpressions(t *testing.T) {
	t.Parallel()
	c := repo.Config{URL: "https://api.github.com"}
	base := "https://registry.terraform.io/v1/modules/ns/name/aws/1.0.0/download"
	for _, ref := range []string{"HEAD~1", "HEAD^", "main:README", "main..tag", "main@{1}", "@", "refs/heads/.hidden", "main.lock", "refs/tags/name.", "name with space", "name?", "name*", "name[", "name\t", "name\x7f", "/main", "main/", "refs//main"} {
		t.Run(url.QueryEscape(ref), func(t *testing.T) {
			if _, err := parseModuleSource("github.com/org/repo?ref="+url.QueryEscape(ref), base, c); err == nil {
				t.Fatal("unqualified snapshot ref was admitted")
			}
		})
	}
	for _, ref := range []string{"HEAD", "main", "v1.0", "feature/native", "refs/heads/main", "refs/tags/v1.0", strings.Repeat("a", 40), "tag+plus", "release@team", "feature/日本語"} {
		t.Run("valid-"+url.QueryEscape(ref), func(t *testing.T) {
			got, err := parseModuleSource("github.com/org/repo?ref="+url.QueryEscape(ref), base, c)
			if err != nil || got.Target != "https://github.com/org/repo/archive/"+url.PathEscape(ref)+".tar.gz" {
				t.Fatal("valid snapshot identity was changed", err)
			}
		})
	}
}
