package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const repoRoot = "../.."

func put(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// release writes a repository whose manifest, CHANGELOG and one marked
// README line say the given versions.
func release(t *testing.T, manifest, heading, marker string) string {
	t.Helper()
	root := t.TempDir()
	put(t, root, manifestFile, `{".": "`+manifest+`"}`)
	put(t, root, changelogFile, "# Changelog\n\n"+heading+"\n\n### Features\n\n## [0.3.0](x/compare/v0.2.1...v0.3.0) (2026-11-16)\n")
	put(t, root, configFile, `{"packages": {".": {"extra-files": [{"type": "generic", "path": "deploy/README.md"}, "docs/README.md"]}}}`)
	put(t, root, "deploy/README.md", "x\nsource = \"git::https://h/r.git//m?ref="+marker+"\" # x-release-please-version\nversion = \"~> 5.0.1\"\n")
	put(t, root, "docs/README.md", "no markers\n")
	return root
}

func TestUnitReleaseVersionAgreement(t *testing.T) {
	const good = "## [0.4.0](https://github.com/weaveplatform/artifactweave/compare/v0.3.0...v0.4.0) (2026-11-24)"
	for _, tc := range []struct {
		name                      string
		manifest, heading, marker string
		title, tag                string
		want                      []string // substrings of the error; none means success
	}{
		{name: "title agrees", manifest: "0.4.0", heading: good, marker: "v0.4.0", title: "chore(main): release 0.4.0"},
		{name: "tag agrees", manifest: "0.4.0", heading: good, marker: "v0.4.0", tag: "v0.4.0"},
		{name: "first release heading", manifest: "0.1.0", heading: "## 0.1.0 (2026-10-19)", marker: "v0.1.0", tag: "v0.1.0"},
		{
			// #1624: the title was edited to 0.4.0 over a 0.3.1 release.
			name: "title edited by hand", manifest: "0.3.1", marker: "v0.3.1", title: "chore(main): release 0.4.0",
			heading: "## [0.3.1](https://github.com/weaveplatform/artifactweave/compare/v0.3.0...v0.3.1) (2026-11-24)",
			want: []string{
				"the pull request title says 0.4.0",
				".release-please-manifest.json says 0.3.1",
				"CHANGELOG.md's newest heading says 0.3.1",
				"compare link ends at v0.3.1",
				"deploy/README.md:2 says v0.3.1",
				"Release-As",
			},
		},
		{
			name: "tag over another version", manifest: "0.3.1", heading: good, marker: "v0.4.0", tag: "v0.4.0",
			want: []string{"tag v0.4.0 says 0.4.0", ".release-please-manifest.json says 0.3.1"},
		},
		{
			name: "heading renamed without its link", manifest: "0.4.0", marker: "v0.4.0", tag: "v0.4.0",
			heading: "## [0.4.0](https://github.com/weaveplatform/artifactweave/compare/v0.3.0...v0.3.1) (2026-11-24)",
			want:    []string{"compare link ends at v0.3.1"},
		},
		{
			name: "unparsable title", manifest: "0.4.0", heading: good, marker: "v0.4.0", title: "chore(main): release v0.4.0",
			want: []string{`is not "chore(main): release <version>"`},
		},
		{name: "unparsable tag", manifest: "0.4.0", heading: good, marker: "v0.4.0", tag: "0.4.0", want: []string{"is not v<version>"}},
		{name: "neither", manifest: "0.4.0", heading: good, marker: "v0.4.0", want: []string{"exactly one"}},
		{
			name: "both", manifest: "0.4.0", heading: good, marker: "v0.4.0", title: "chore(main): release 0.4.0", tag: "v0.4.0",
			want: []string{"exactly one"},
		},
		{
			name: "heading without a version", manifest: "0.4.0", heading: "## Unreleased", marker: "v0.4.0", tag: "v0.4.0",
			want: []string{`newest heading "## Unreleased" names no version`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := release(t, tc.manifest, tc.heading, tc.marker)
			var out bytes.Buffer
			err := run(&out, root, tc.title, tc.tag)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "all say "+tc.manifest) {
					t.Fatalf("output %q", out.String())
				}
				return
			}
			if err == nil {
				t.Fatal("mismatch passed")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q:\n%v", w, err)
				}
			}
		})
	}
}

func TestUnitReleaseVersionUnreadableInputs(t *testing.T) {
	for _, tc := range []struct {
		name, path, content, want string
		remove                    bool
	}{
		{name: "no manifest", path: manifestFile, remove: true, want: manifestFile},
		{name: "malformed manifest", path: manifestFile, content: "{", want: manifestFile},
		{name: "manifest without the root package", path: manifestFile, content: `{"pkg": "0.4.0"}`, want: `no "." package`},
		{name: "no CHANGELOG", path: changelogFile, remove: true, want: changelogFile},
		{name: "CHANGELOG without a release", path: changelogFile, content: "# Changelog\n", want: "has no release heading"},
		{name: "no config", path: configFile, remove: true, want: configFile},
		{name: "malformed config", path: configFile, content: "[", want: configFile},
		{name: "extra-files entry without a path", path: configFile, content: `{"packages": {".": {"extra-files": [{"type": "generic"}]}}}`, want: "has no path"},
		{name: "missing extra file", path: "deploy/README.md", remove: true, want: "deploy/README.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := release(t, "0.4.0", "## [0.4.0](x/compare/v0.3.0...v0.4.0) (2026-11-24)", "v0.4.0")
			full := filepath.Join(root, filepath.FromSlash(tc.path))
			if tc.remove {
				if err := os.Remove(full); err != nil {
					t.Fatal(err)
				}
			} else {
				put(t, root, tc.path, tc.content)
			}
			err := run(&bytes.Buffer{}, root, "", "v0.4.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

// The repository itself must agree with its own manifest, so the release
// pull request release-please builds from it starts consistent.
func TestUnitRepositoryReleaseVersionAgrees(t *testing.T) {
	version, err := manifestVersion(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(&bytes.Buffer{}, repoRoot, "", "v"+version); err != nil {
		t.Fatal(err)
	}
}

// Before 1.0, a feature must bump the minor version and an incompatible
// change must stay below 1.0. release-please 17.6.0's DefaultVersioningStrategy
// bumps the patch for a feature when bump-patch-for-minor-pre-major is true,
// which released P12's features as 0.3.1 (#1947).
func TestUnitReleasePleaseBumpsMinorForFeatures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, configFile))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Packages map[string]map[string]any `json:"packages"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	root := config.Packages["."]
	if root["bump-minor-pre-major"] != true {
		t.Error("bump-minor-pre-major must be true: an incompatible change before 1.0 bumps the minor version, not the major")
	}
	if v, ok := root["bump-patch-for-minor-pre-major"]; ok && v != false {
		t.Errorf("bump-patch-for-minor-pre-major is %v: a feature before 1.0 must bump the minor version", v)
	}
}

type workflow struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestUnitReleaseWorkflowsRunTheVersionCheck(t *testing.T) {
	title := readWorkflow(t, "pr-title-validation.yml")
	pr, _ := title.On["pull_request"].(map[string]any)
	if types, _ := pr["types"].([]any); !containsAny(types, "edited") {
		t.Error("PR Title Validation must rerun when a title is edited")
	}
	job, ok := title.Jobs["release-version"]
	if !ok {
		t.Fatal("PR Title Validation has no release-version job")
	}
	if !strings.Contains(job.If, "github.event.pull_request.head.repo.full_name == github.repository") ||
		!strings.Contains(job.If, "github.event.pull_request.head.ref == 'release-please--branches--main'") ||
		strings.Contains(job.If, "title") {
		t.Errorf("release-version must select the release branch by identity, not title: %q", job.If)
	}
	var titleChecked bool
	for _, s := range job.Steps {
		if strings.Contains(s.Run, `go run ./tools/releaseversion -title "$PR_TITLE"`) &&
			s.Env["PR_TITLE"] == "${{ github.event.pull_request.title }}" {
			titleChecked = true
		}
	}
	if !titleChecked {
		t.Error("release-version must check the pull request title")
	}

	steps := readWorkflow(t, "release.yml").Jobs["goreleaser"].Steps
	check, build := -1, -1
	for i, s := range steps {
		if strings.Contains(s.Run, `go run ./tools/releaseversion -tag "$GITHUB_REF_NAME"`) &&
			strings.Contains(s.Run, `gh release view "$GITHUB_REF_NAME"`) {
			check = i
		}
		if strings.HasPrefix(s.Uses, "goreleaser/goreleaser-action@") {
			build = i
		}
	}
	if check < 0 || build < 0 || check > build {
		t.Errorf("the release build must check the tag and its release before goreleaser (check %d, build %d)", check, build)
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
