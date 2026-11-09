package model_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/retention/api"
	"github.com/weaveplatform/artifactweave/migration/retention/core/model"
)

func TestUnitRunStatusTerminal(t *testing.T) {
	for s, want := range map[api.RunStatus]bool{
		api.RunCompleted: true, api.RunCompletedWithErrors: true, api.RunStopped: true, api.RunFailed: true,
		api.RunInProgress: false, api.RunStopping: false, api.RunWarmed: false,
	} {
		if s.Terminal() != want {
			t.Errorf("%s.Terminal() != %v", s, want)
		}
	}
}

func TestUnitArchive(t *testing.T) {
	e := model.ArchiveExecution{Total: 200, Completed: 50, Failed: 50}
	if e.Progress() != 50 || (model.ArchiveExecution{}).Progress() != 0 {
		t.Error("Progress")
	}
	ok := model.RestoreRequest{
		Type:       model.RestoreArtifact,
		Items:      []string{"libs/a.jar"},
		ToOriginal: true,
	}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	manifest := model.RestoreRequest{
		Type:          model.RestorePackage,
		ManifestPaths: []string{"m"},
		NewPath:       "restored/",
	}
	if err := manifest.Validate(); err != nil {
		t.Error(err)
	}
	bad := []model.RestoreRequest{
		{Type: "X", Items: []string{"a"}, ToOriginal: true},
		{Type: model.RestoreArtifact, ToOriginal: true},
		{
			Type:       model.RestoreArtifact,
			Items:      make([]string, model.MaxRestoreItems+1),
			ToOriginal: true,
		},
		{Type: model.RestoreArtifact, Items: []string{"a"}},
	}
	for _, r := range bad {
		if err := r.Validate(); !errors.Is(err, model.ErrInvalidRestore) {
			t.Errorf("%+v: %v", r.Type, err)
		}
	}
}
