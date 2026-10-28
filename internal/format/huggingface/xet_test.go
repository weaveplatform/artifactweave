package huggingface_test

import (
	"strings"
	"testing"

	api "github.com/weaveplatform/artifactweave/internal/format"
	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitXetPrivateLayoutBoundaries(t *testing.T) {
	h := format.Handler{}
	c := repo.Config{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML}
	for _, kind := range []string{"xorbs", "files", "sha256", "stages"} {
		path := "models--acme--demo/.xet/" + kind + "/" + strings.Repeat("a", 64)
		if !format.IsXetPath(path) || !format.ValidXetPath(path) {
			t.Fatal("valid private path rejected")
		}
		write := api.Write{Path: path, Op: api.OpDeploy}
		if _, err := h.Admit(t.Context(), api.Env{}, c, write); err == nil {
			t.Fatal("generic private write accepted")
		}
		if _, err := h.Admit(format.XetWrite(t.Context()), api.Env{}, c, write); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Resolve(t.Context(), api.Env{}, c, api.Download{Path: path}); err == nil {
			t.Fatal("raw private read accepted")
		}
		item := types.Item{
			Path: strings.TrimSuffix(path, "/"+strings.Repeat("a", 64)),
			Name: strings.Repeat("a", 64),
		}
		if err := h.GuardTransferSource(t.Context(), api.Env{}, c, item); err == nil {
			t.Fatal("private copy accepted")
		}
	}
	for _, path := range []string{"bad", "models--acme--demo/.xet/nope/" + strings.Repeat("a", 64), "models--acme--demo/.xet/files/bad", "bad/.xet/files/" + strings.Repeat("a", 64)} {
		if format.ValidXetPath(path) {
			t.Fatal("invalid private layout accepted")
		}
	}
	path := "models--acme--demo/snapshots/" + strings.Repeat("b", 40) + "/config.json"
	if got, e := h.Resolve(
		t.Context(),
		api.Env{},
		c,
		api.Download{Path: path},
	); e != nil ||
		got != path {
		t.Fatal("snapshot read changed")
	}
	if e := h.GuardTransferSource(
		t.Context(),
		api.Env{},
		c,
		types.Item{Name: "config.json"},
	); e != nil {
		t.Fatal("ordinary transfer refused")
	}
}
