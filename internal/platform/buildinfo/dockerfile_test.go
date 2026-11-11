package buildinfo

import (
	"fmt"
	goversion "go/version"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

var (
	// golangStage captures the reference, tag and digest, of each stage
	// built FROM the golang image.
	golangStage   = regexp.MustCompile(`(?m)^FROM\s+golang:(\S+)`)
	pinnedRelease = regexp.MustCompile(`^(\d+\.\d+\.\d+)(?:-[a-z0-9.]+)?@sha256:[0-9a-f]{64}$`)
	// toolchainRelease reads the release from runtime.Version, which can
	// follow it with a vendor suffix ("go1.27.4-bigcorp") or the experiments
	// the toolchain was built with ("go1.27.4 X:jsonv2"). A prerelease
	// ("go1.28rc1") or a development build ("devel go1.28-...") names none.
	toolchainRelease = regexp.MustCompile(`^go(\d+\.\d+\.\d+)(?:[- ]|$)`)
)

// The image's binary carries the standard library of the Go that builds it,
// and nothing else compares that Go with the one CI tests and scans. CI's
// unit jobs and govulncheck run on the release setup-go resolves from
// go.mod's go line, so an image left on an older release lacks fixes those
// runs already have (#1208). In those jobs runtime.Version is that release,
// so this test fails exactly when the Dockerfile falls behind it. An image
// ahead of the toolchain passes: setup-go takes a runner's cached release
// before the newest, and a developer's Go may lag too. Raising the go line
// to a new minor fails here until the image reaches that minor, which it
// must anyway: the golang image sets GOTOOLCHAIN=local, so it could not
// build the module.
func TestUnitDockerfileGoNotBehindToolchain(t *testing.T) {
	release, ok := goRelease(runtime.Version())
	if !ok {
		t.Skipf("%s is not a Go release; the Dockerfile builds only with released Go",
			runtime.Version())
	}
	for _, problem := range dockerfileGoProblems(repopath.Read(t, "Dockerfile"), release) {
		t.Error(problem)
	}
}

// goRelease returns the release that toolchain, a runtime.Version string,
// names, such as "go1.27.4", or false when it names none.
func goRelease(toolchain string) (string, bool) {
	m := toolchainRelease.FindStringSubmatch(toolchain)
	if m == nil {
		return "", false
	}
	return "go" + m[1], true
}

// dockerfileGoProblems reports a dockerfile without a golang stage, and each
// golang stage that does not name a full Go release pinned by digest or whose
// release is older than release.
func dockerfileGoProblems(dockerfile []byte, release string) []string {
	stages := golangStage.FindAllSubmatch(dockerfile, -1)
	if len(stages) == 0 {
		return []string{"Dockerfile has no FROM golang stage to read the Go release from"}
	}
	var problems []string
	for _, stage := range stages {
		ref := string(stage[1])
		tag, _, _ := strings.Cut(ref, "@")
		switch m := pinnedRelease.FindStringSubmatch(ref); {
		case m == nil:
			problems = append(problems, fmt.Sprintf(
				"Dockerfile: golang:%s does not name a full Go release pinned by its index digest,"+
					" such as golang:1.27.4-bookworm@sha256:<digest>", ref))
		case goversion.Compare("go"+m[1], release) < 0:
			problems = append(problems, fmt.Sprintf(
				"Dockerfile builds with go%s (golang:%s), older than the %s that runs these tests;"+
					" merge Dependabot's docker update for /, or move the stage to the golang"+
					" image of %s or later, pinned by the index digest that"+
					" docker buildx imagetools inspect prints", m[1], tag, release, release))
		}
	}
	return problems
}

func TestUnitDockerfileGoRuleDetectsViolations(t *testing.T) {
	const digest = "@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea"
	stage := func(tag string) string { return "FROM golang:" + tag + digest + " AS build\n" }
	for _, c := range []struct {
		name, dockerfile, release string
		// want holds a substring of each problem, in order.
		want []string
	}{
		{"same release", stage("1.27.4-bookworm"), "go1.27.4", nil},
		{"newer patch", stage("1.27.5-bookworm"), "go1.27.4", nil},
		{"newer minor", stage("1.28.0-bookworm"), "go1.27.9", nil},
		{"patches compare as numbers", stage("1.27.10-bookworm"), "go1.27.9", nil},
		{"no variant", stage("1.27.4"), "go1.27.4", nil},
		{
			// A Windows checkout can end each line with CR LF.
			"CRLF line endings",
			"# syntax=docker/dockerfile:1\r\nFROM golang:1.27.4-bookworm" + digest + "\r\nWORKDIR /src\r\n",
			"go1.27.4", nil,
		},
		{
			"older patch", stage("1.27.2-bookworm"), "go1.27.4",
			[]string{"go1.27.2 (golang:1.27.2-bookworm), older than the go1.27.4"},
		},
		{"older minor", stage("1.27.9-bookworm"), "go1.28.0", []string{"older than the go1.28.0"}},
		{
			"every golang stage", stage("1.27.4-bookworm") + stage("1.27.3-bookworm"), "go1.27.4",
			[]string{"go1.27.3"},
		},
		{
			"minor-only tag", stage("1.27-bookworm"), "go1.27.4",
			[]string{"golang:1.27-bookworm@sha256:4fa7c968"},
		},
		{
			"no digest", "FROM golang:1.27.4-bookworm AS build\n", "go1.27.4",
			[]string{"does not name a full Go release pinned"},
		},
		{
			"no golang stage", "FROM gcr.io/distroless/static-debian12:nonroot" + digest + "\n",
			"go1.27.4",
			[]string{"no FROM golang stage"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := dockerfileGoProblems([]byte(c.dockerfile), c.release)
			if len(got) != len(c.want) {
				t.Fatalf("problems %q, want %d", got, len(c.want))
			}
			for i, want := range c.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %q does not contain %q", got[i], want)
				}
			}
		})
	}
}

func TestUnitGoReleaseNamesOnlyReleases(t *testing.T) {
	for toolchain, want := range map[string]string{
		"go1.27.4":          "go1.27.4",
		"go1.27.10":         "go1.27.10",
		"go1.27.4 X:jsonv2": "go1.27.4",
		"go1.27.4-bigcorp":  "go1.27.4",
		"go1.28rc1":         "",
		"go1.28":            "",
		"devel go1.28-0123456789 Tue Nov 10 12:00:00 2026 +0000": "",
	} {
		if got, ok := goRelease(toolchain); got != want || ok != (want != "") {
			t.Errorf("goRelease(%q) = %q, %t; want %q", toolchain, got, ok, want)
		}
	}
}
