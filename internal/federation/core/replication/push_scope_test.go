package replication

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitPushScopeKeepsReconciliationInsideSelectedPaths(t *testing.T) {
	for _, tc := range []struct {
		name, root, include, exclude string
		selected, untouched          []string
	}{
		{"whole repository", "", "", "", []string{"one.pkg", "@scope/name/file.tgz", "space name/é.pkg"}, []string{"", ".", "/one.pkg", "one.pkg/", "a//b", "a/../outside", "a\\b", "\xff"}},
		{"exact requested item", "org/one.pkg", "", "", []string{"org/one.pkg"}, []string{"org/two.pkg", "org/one.pkg.other", "other/one.pkg"}},
		{"subtree and exclusions", "/org/pkg/", "org/**", "**/*.asc", []string{"org/pkg/one.jar", "org/pkg/deep/two.jar"}, []string{"org/pkg/one.jar.asc", "org/pkg2/one.jar", "org/other/one.jar", "org/pkg/../secret", "Org/pkg/one.jar"}},
		{"single segment wildcard", "org", "org/*.jar", "", []string{"org/one.jar"}, []string{"org/deep/one.jar", "org/one.zip"}},
		{"unicode single character", "org", "org/**/f?le.jar", "org/private/**", []string{"org/pkg/fîle.jar"}, []string{"org/pkg/fiiile.jar", "org/private/fîle.jar"}},
		{"exclude wins", ".", "**", "**", nil, []string{"one.pkg", "org/pkg/one.jar"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, err := newPushScope(tc.root, tc.include, tc.exclude)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range tc.selected {
				if !scope.matches(path) {
					t.Fatal("selected source/destination path was lost", path)
				}
			}
			for _, path := range tc.untouched {
				if scope.matches(path) {
					t.Fatal("unselected or malformed destination could enter reconciliation", path)
				}
			}
		})
	}
}

func TestUnitPushScopeRejectsInvalidInput(t *testing.T) {
	for _, root := range []string{"../outside", "a/../outside", "a//b", "a/./b", "a\x00b", "\xff"} {
		if _, err := newPushScope(root, "", ""); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid reconciliation root accepted", root, err)
		}
	}
	for _, pattern := range []string{"\xff", "a\x00b"} {
		if _, err := newPushScope("", pattern, ""); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid include accepted", err)
		}
		if _, err := newPushScope("", "", pattern); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid exclude accepted", err)
		}
	}
}
