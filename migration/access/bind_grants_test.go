package access

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/model"
)

// admits is the target union AccessChange compares at both ends of a pair,
// so it must answer as the evaluator does for a file and for a folder above a
// permitted path.
func TestUnitMigrationPermissionUnionMatchesFileAndFolderSemantics(t *testing.T) {
	for _, tc := range []struct {
		name         string
		targets      []model.Target
		path         string
		folder, want bool
	}{
		{"absent grants", nil, "models/demo", false, false},
		{"default include", []model.Target{{}}, "models/demo", false, true},
		{"folder ancestor", []model.Target{{Include: []string{"models/demo/file"}}}, "models/demo", true, true},
		{"file ancestor", []model.Target{{Include: []string{"models/demo/file"}}}, "models/demo", false, false},
		{"excluded file", []model.Target{{Include: []string{"**"}, Exclude: []string{"models/**"}}}, "models/demo", false, false},
		{"empty exclusions are ignored", []model.Target{{Include: []string{"models/**"}, Exclude: []string{"", "  "}}}, "models/demo", false, true},
		{"whitespace is trimmed", []model.Target{{Include: []string{" models/** "}}}, "models/demo", false, true},
		{"empty include uses default", []model.Target{{Include: []string{" "}}}, "models/demo", false, true},
		{"target union", []model.Target{{Include: []string{"other/**"}}, {Include: []string{"models/**"}}}, "models/demo", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admits(tc.targets, tc.path, tc.folder); got != tc.want {
				t.Fatalf("permission applicability: got %v want %v", got, tc.want)
			}
		})
	}
}
