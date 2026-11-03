package huggingface

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitMigrationStreamingHonorsCancellationAndReadFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := migrationReader{ctx: ctx, reader: strings.NewReader("unread")}
	if _, err := reader.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled migration kept reading: %v", err)
	}
	reader = migrationReader{ctx: t.Context(), reader: failedJSONBody{}}
	if _, err := reader.Read(make([]byte, 8)); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("storage read failure ignored: %v", err)
	}
}

func TestUnitMigrationPermissionUnionMatchesFileAndFolderSemantics(t *testing.T) {
	for _, tc := range []struct {
		name         string
		targets      []types.Target
		path         string
		folder, want bool
	}{
		{"absent grants", nil, "models/demo", false, false},
		{"default include", []types.Target{{}}, "models/demo", false, true},
		{"folder ancestor", []types.Target{{Include: []string{"models/demo/file"}}}, "models/demo", true, true},
		{"file ancestor", []types.Target{{Include: []string{"models/demo/file"}}}, "models/demo", false, false},
		{"excluded file", []types.Target{{Include: []string{"**"}, Exclude: []string{"models/**"}}}, "models/demo", false, false},
		{"target union", []types.Target{{Include: []string{"other/**"}}, {Include: []string{"models/**"}}}, "models/demo", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := migrationAllows(tc.targets, tc.path, tc.folder); got != tc.want {
				t.Fatalf("permission applicability: got %v want %v", got, tc.want)
			}
		})
	}
}
