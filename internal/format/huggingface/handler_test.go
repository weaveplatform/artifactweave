package huggingface

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitAdmissionPreservesCurrentLayoutAndRepositoryPolicy(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})
	rev := "0123456789012345678901234567890123456789"
	good := "models--acme--demo/snapshots/" + rev + "/config.json"
	for _, path := range []string{good, "models--acme--demo/refs/main"} {
		w := format.Write{Op: format.OpDeploy, Path: path}
		got, err := h.Admit(t.Context(), format.Env{}, c, w)
		if err != nil || got.Path != path {
			t.Fatalf("current-layout write %q: %+v %v", path, got, err)
		}
	}
	for _, path := range []string{"acme/demo/resolve/main/config.json", "models--acme--demo/snapshots/../config.json"} {
		if _, err := h.Admit(t.Context(), format.Env{}, c,
			format.Write{Op: format.OpDeploy, Path: path}); err == nil {
			t.Errorf("accepted generic or traversal path %q", path)
		}
	}
	// Cache publication and checksum sidecars do not introduce a new client
	// pathname; their existing source/item policy still applies.
	for _, w := range []format.Write{
		{Op: format.OpDeploy, Path: "legacy/cache", Cached: true},
		{Op: format.OpChecksum, Path: "legacy/checksum"},
	} {
		if _, err := h.Admit(t.Context(), format.Env{}, c, w); err != nil {
			t.Fatalf("existing cache/checksum path %q: %v", w.Path, err)
		}
	}
	blacked := c
	black := true
	blacked.BlackedOut = &black
	if _, err := h.Admit(t.Context(), format.Env{}, blacked,
		format.Write{Op: format.OpDeploy, Path: good}); err == nil {
		t.Fatal("blacked-out repository admitted model")
	}
	patterned := c
	include := "datasets--*"
	patterned.IncludesPattern = &include
	if _, err := h.Admit(t.Context(), format.Env{}, patterned,
		format.Write{Op: format.OpDeploy, Path: good}); err == nil {
		t.Fatal("include pattern admitted model outside allowed path")
	}
	if h.Classify(c, good) != format.ClassUnique ||
		h.Classify(c, "models--acme--demo/refs/main") != format.ClassMetadata ||
		!Serves(repo.HuggingFaceML) || Serves(repo.NPM) {
		t.Fatal("Hugging Face identity/classification drift")
	}
}

func TestUnitLifecycleIgnoresNonSnapshotAndFencesReindex(t *testing.T) {
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML})
	env := format.Env{Store: inmem.New()}
	for _, it := range []types.Item{{Folder: true}, {Name: "refs/main"}, {Name: "legacy"}} {
		if err := h.Deployed(t.Context(), env, c, it); err != nil {
			t.Fatalf("non-snapshot deploy: %v", err)
		}
	}
	if err := h.Deleted(t.Context(), env, c, []types.Item{{Name: "legacy"}}); err != nil {
		t.Fatalf("non-snapshot delete: %v", err)
	}
	if err := h.Deleting(t.Context(), env, c, types.Item{}); err != nil {
		t.Fatalf("pre-delete hook: %v", err)
	}
	stop := errors.New("source changed during reindex")
	env.CheckReindex = func(context.Context) error { return stop }
	if err := h.Reindex(t.Context(), env, c); !errors.Is(err, stop) {
		t.Fatalf("source fence did not stop reindex: %v", err)
	}
}
