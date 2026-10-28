package config

import "testing"

func TestUnitCondaShardsConfigurationPrecedence(t *testing.T) {
	if Default().Conda.ShardsEnabled {
		t.Fatal("shards must be opt-in")
	}
	file := writeFile(t, "conda:\n  shards_enabled: true\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || !cfg.Conda.ShardsEnabled {
		t.Fatalf("file: %+v %v", cfg.Conda, err)
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_CONDA_SHARDS_ENABLED": "false"})
	cfg, err = Load(LoadOptions{File: file, Env: env})
	if err != nil || cfg.Conda.ShardsEnabled {
		t.Fatalf("env: %+v %v", cfg.Conda, err)
	}
	cfg, err = Load(
		LoadOptions{File: file, Env: env, Flags: map[string]string{"conda.shards_enabled": "true"}},
	)
	if err != nil || !cfg.Conda.ShardsEnabled {
		t.Fatalf("flags: %+v %v", cfg.Conda, err)
	}
	if _, err := Load(
		LoadOptions{Flags: map[string]string{"conda.shards_enabled": "maybe"}},
	); err == nil {
		t.Fatal("invalid boolean accepted")
	}
	if _, err := Load(LoadOptions{File: writeFile(t, "conda:\n  shards: true\n")}); err == nil {
		t.Fatal("unknown field accepted")
	}
}
