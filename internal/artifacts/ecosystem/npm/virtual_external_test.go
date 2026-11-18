package npm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUnitExternalDependency(t *testing.T) {
	for _, tc := range []struct {
		name, spec, file, target string
		versioned                bool
	}{
		{"gkt", "https://tgz.pm2.io/gkt-1.0.0.tgz", "gkt-gkt-1.0.0.tgz", "https://tgz.pm2.io/gkt-1.0.0.tgz", false},
		{"file-saver", "github:eligrey/FileSaver.js#1.3.8", "eligrey-FileSaver.js-1.3.8.tar.gz", "https://github.com/eligrey/FileSaver.js/archive/1.3.8.tar.gz", false},
		{"file-saver", "eligrey/FileSaver.js#1.3.8", "eligrey-FileSaver.js-1.3.8.tar.gz", "https://github.com/eligrey/FileSaver.js/archive/1.3.8.tar.gz", false},
		{"file-saver", "git+ssh://git@github.com:eligrey/FileSaver.js.git#1.3.8", "eligrey-FileSaver.js-1.3.8.tar.gz", "https://github.com/eligrey/FileSaver.js/archive/1.3.8.tar.gz", false},
		{"file-saver", "git+ssh://git@github.com/eligrey/FileSaver.js.git#1.3.8", "eligrey-FileSaver.js-1.3.8.tar.gz", "https://github.com/eligrey/FileSaver.js/archive/1.3.8.tar.gz", false},
		{"file-saver", "git+https://git@github.com/eligrey/FileSaver.js.git#1.3.8", "eligrey-FileSaver.js-1.3.8.tar.gz", "https://github.com/eligrey/FileSaver.js/archive/1.3.8.tar.gz", false},
		{"file-saver", "https://github.com/eligrey/FileSaver.js.git#1.3.8", "eligrey-FileSaver.js-1.3.8.tar.gz", "https://github.com/eligrey/FileSaver.js/archive/1.3.8.tar.gz", false},
		{"dep", "https://github.com/owner/archive.git.tgz", "owner-archive.git.tgz-HEAD.tar.gz", "https://github.com/owner/archive.git.tgz/archive/HEAD.tar.gz", false},
		{"dep", "https://github.com/owner/repo", "owner-repo-HEAD.tar.gz", "https://github.com/owner/repo/archive/HEAD.tar.gz", false},
		{"dep", "https://github.com/owner/repo/releases/download/v1.0/archive.git.tgz", "dep-archive.git.tgz", "https://github.com/owner/repo/releases/download/v1.0/archive.git.tgz", false},
		{"dep", "github:owner/repo", "owner-repo-HEAD.tar.gz", "https://github.com/owner/repo/archive/HEAD.tar.gz", false},
		{"dep", "owner/repo#feature/fix", "owner-repo-feature-fix.tar.gz", "https://github.com/owner/repo/archive/feature/fix.tar.gz", false},
		{"dep", "https://cdn.example/dep.tgz", "dep-dep.tgz", "https://cdn.example/dep.tgz", true},
		{"dep", "https://cdn.example/20.5.0.0/native.tgz?download=1", "dep-native-20.5.0.0.tgz", "https://cdn.example/20.5.0.0/native.tgz?download=1", true},
		{"dep", "https://cdn.example/1.2.3/dep-1.2.3.tar.gz", "dep-dep-1.2.3.tar.gz", "https://cdn.example/1.2.3/dep-1.2.3.tar.gz", true},
		{"@scope/dep", "https://cdn.example/dep.tgz", "scope-dep-dep.tgz", "https://cdn.example/dep.tgz", false},
	} {
		t.Run(tc.name+"_"+tc.spec, func(t *testing.T) {
			got, ok := ExternalDependency(tc.name, tc.spec, tc.versioned)
			if !ok || got.File != tc.file || got.URL != tc.target || got.Spec != externalTag(tc.file)+"-art-external" {
				t.Fatalf("got%+v/%v", got, ok)
			}
		})
	}
	for _, spec := range []string{"^1.0.0", "file:../dep", "npm:alias@1.0.0", "git+ssh://user@git.example/team/dep.git#v1", "github:../dep#x", "github:owner/repo#../x", "owner/repo#", "https://user:secret@cdn.example/a.tgz", "https://cdn.example/a.tgz#fragment", "https://cdn.example/", "git+https://git:secret@github.com/owner/repo.git#x", "owner/repo#semver:^1.0.0", "https://cdn.example/" + strings.Repeat("x", 300)} {
		t.Run("unchanged_"+spec, func(t *testing.T) {
			if got, ok := ExternalDependency("dep", spec, false); ok {
				t.Fatal(got)
			}
		})
	}

	for _, spec := range []string{"https://cdn.example/archive+meta@host.tgz", "github:owner/repo#1.0.0+build", "https://cdn.example/name_2b_.tgz"} {
		got, ok := ExternalDependency("@scope/lib", spec, false)
		if !ok || strings.ContainsAny(got.Spec, "+@/%") {
			t.Fatal("invalid dist-tag", got, ok)
		}
	}
	if _, ok := ExternalDependency("../bad", "https://cdn.example/a.tgz", false); ok {
		t.Fatal("invalid package")
	}
}

func TestUnitRewriteDependencies(t *testing.T) {
	original := json.RawMessage(
		`{"name":"parent","dependencies":{"a":"url","b":1},"devDependencies":{"a":"url"},"optionalDependencies":{"a":"url"},"peerDependencies":{"a":"url"},"dist":{"signatures":[{"sig":"keep"}]},"unknown":true}`,
	)
	got, err := RewriteDependencies(original, func(_, spec string) (string, error) { return spec + "-tag", nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "url-tag") != 3 || !strings.Contains(string(got), `"peerDependencies":{"a":"url"}`) ||
		!strings.Contains(string(got), `"signatures":[{"sig":"keep"}]`) ||
		strings.Contains(string(original), "url-tag") {
		t.Fatal(string(got), string(original))
	}
	fail := errors.New("persist failed")
	if _, err = RewriteDependencies(original, func(_, _ string) (string, error) { return "", fail }); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	if _, err = RewriteDependencies(json.RawMessage("{"), func(_, s string) (string, error) { return s, nil }); err == nil {
		t.Fatal("invalid manifest")
	}
}
