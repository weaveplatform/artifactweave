package layout_test

import (
	"errors"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
)

// The configuration descriptor refuses a layout with Compile's error text, so
// Checker must answer exactly as Compile does: the bundled layouts, some of
// which use tokens that only Lookup drops, a custom layout, and each kind of
// malformed pattern.
func TestUnitLayoutCheckerMatchesCompile(t *testing.T) {
	defs := append(
		layout.Bundled(),
		layout.Definition{
			Name:                             "custom",
			ArtifactPathPattern:              "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]",
			DistinctiveDescriptorPathPattern: true,
			DescriptorPathPattern:            "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev]).pom",
			FolderIntegrationRevisionRegExp:  "SNAPSHOT",
			FileIntegrationRevisionRegExp:    "SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))",
		},
		layout.Definition{
			Name:                "custom token",
			ArtifactPathPattern: "[org]/[build<\\d+>]/[module].[ext]",
		},
		layout.Definition{Name: "unknown token", ArtifactPathPattern: "[nope]/[baseRev]"},
		layout.Definition{Name: "unterminated", ArtifactPathPattern: "[module"},
		layout.Definition{Name: "stray parenthesis", ArtifactPathPattern: "[module])"},
		layout.Definition{
			Name:                  "bad descriptor",
			ArtifactPathPattern:   "[module]",
			DescriptorPathPattern: "[nope]",
		},
		layout.Definition{
			Name:                            "bad integration expression",
			ArtifactPathPattern:             "[module]-[folderItegRev]",
			FolderIntegrationRevisionRegExp: "(",
		},
	)
	var checker layout.Checker
	compiled, refused := 0, 0
	for _, d := range defs {
		_, want := layout.Compile(d)
		got := checker.Check(artifactsapi.LayoutDefinition(d))
		switch {
		case want == nil:
			compiled++
			if got != nil {
				t.Errorf("%s: Check = %v, but it compiles", d.Name, got)
			}
		default:
			refused++
			if got == nil || got.Error() != want.Error() || !errors.Is(got, layout.ErrPattern) {
				t.Errorf("%s: Check = %v, want %v", d.Name, got, want)
			}
		}
	}
	if compiled == 0 || refused == 0 {
		t.Fatalf(
			"%d compiled and %d refused: the cases no longer cover both answers",
			compiled,
			refused,
		)
	}
}
