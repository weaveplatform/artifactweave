package model_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitRunStatusTerminal(t *testing.T) {
	for s, want := range map[types.RunStatus]bool{
		types.RunCompleted: true, types.RunCompletedWithErrors: true, types.RunStopped: true, types.RunFailed: true,
		types.RunInProgress: false, types.RunStopping: false, types.RunWarmed: false,
	} {
		if s.Terminal() != want {
			t.Errorf("%s.Terminal() != %v", s, want)
		}
	}
}

func TestUnitArchive(t *testing.T) {
	e := types.ArchiveExecution{Total: 200, Completed: 50, Failed: 50}
	if e.Progress() != 50 || (types.ArchiveExecution{}).Progress() != 0 {
		t.Error("Progress")
	}
	ok := types.RestoreRequest{
		Type:       types.RestoreArtifact,
		Items:      []string{"libs/a.jar"},
		ToOriginal: true,
	}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	manifest := types.RestoreRequest{
		Type:          types.RestorePackage,
		ManifestPaths: []string{"m"},
		NewPath:       "restored/",
	}
	if err := manifest.Validate(); err != nil {
		t.Error(err)
	}
	bad := []types.RestoreRequest{
		{Type: "X", Items: []string{"a"}, ToOriginal: true},
		{Type: types.RestoreArtifact, ToOriginal: true},
		{
			Type:       types.RestoreArtifact,
			Items:      make([]string, types.MaxRestoreItems+1),
			ToOriginal: true,
		},
		{Type: types.RestoreArtifact, Items: []string{"a"}},
	}
	for _, r := range bad {
		if err := r.Validate(); !errors.Is(err, types.ErrInvalidRestore) {
			t.Errorf("%+v: %v", r.Type, err)
		}
	}
}
