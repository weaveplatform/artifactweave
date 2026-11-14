package config

import "testing"

func TestUnitDebianMetadataValidationConfigurationPrecedence(t *testing.T) {
	if !Default().Debian.MetadataValidation {
		t.Fatal("metadata validation must be on by default, as in 7.x")
	}
	if Default().Artifacts().Formats.Debian.DisableMetadataValidation {
		t.Fatal("the default configuration turned the handler's validation off")
	}
	file := writeFile(t, "debian:\n  metadata_validation: false\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.Debian.MetadataValidation {
		t.Fatalf("file: %+v %v", cfg.Debian, err)
	}
	if !cfg.Artifacts().Formats.Debian.DisableMetadataValidation {
		t.Fatal("metadata_validation: false did not reach the handler's settings")
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_DEBIAN_METADATA_VALIDATION": "true"})
	cfg, err = Load(LoadOptions{File: file, Env: env})
	if err != nil || !cfg.Debian.MetadataValidation {
		t.Fatalf("env: %+v %v", cfg.Debian, err)
	}
	cfg, err = Load(LoadOptions{
		File: file, Env: env, Flags: map[string]string{"debian.metadata_validation": "false"},
	})
	if err != nil || cfg.Debian.MetadataValidation {
		t.Fatalf("flags: %+v %v", cfg.Debian, err)
	}
	if _, err := Load(
		LoadOptions{Flags: map[string]string{"debian.metadata_validation": "maybe"}},
	); err == nil {
		t.Fatal("invalid boolean accepted")
	}
	if _, err := Load(
		LoadOptions{File: writeFile(t, "debian:\n  validation: false\n")},
	); err == nil {
		t.Fatal("unknown field accepted")
	}
}
