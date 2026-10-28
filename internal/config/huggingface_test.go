package config

import "testing"

func TestUnitHuggingFaceXetConfigurationPrecedence(t *testing.T) {
	if Default().HuggingFace.XetEnabled {
		t.Fatal("Xet must be opt-in")
	}
	file := writeFile(t, "huggingface:\n  xet_enabled: true\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || !cfg.HuggingFace.XetEnabled {
		t.Fatalf("file: %+v %v", cfg.HuggingFace, err)
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_HUGGINGFACE_XET_ENABLED": "false"})
	cfg, err = Load(LoadOptions{File: file, Env: env})
	if err != nil || cfg.HuggingFace.XetEnabled {
		t.Fatalf("env: %+v %v", cfg.HuggingFace, err)
	}
	cfg, err = Load(
		LoadOptions{
			File:  file,
			Env:   env,
			Flags: map[string]string{"huggingface.xet_enabled": "true"},
		},
	)
	if err != nil || !cfg.HuggingFace.XetEnabled {
		t.Fatalf("flags: %+v %v", cfg.HuggingFace, err)
	}
	if _, err := Load(
		LoadOptions{Flags: map[string]string{"huggingface.xet_enabled": "maybe"}},
	); err == nil {
		t.Fatal("invalid boolean accepted")
	}
	if _, err := Load(
		LoadOptions{File: writeFile(t, "huggingface:\n  shards: true\n")},
	); err == nil {
		t.Fatal("unknown field accepted")
	}
}
