package config

import "testing"

func TestUnitCranArchiveMover(t *testing.T) {
	if Default().Cran.ArchiveMoverEnabled {
		t.Fatal("migration must default off")
	}
	file := writeFile(t, "cran:\n  archive_mover_enabled: true\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || !cfg.Cran.ArchiveMoverEnabled {
		t.Fatalf("file: %+v %v", cfg.Cran, err)
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_CRAN_ARCHIVE_MOVER_ENABLED": "false"})
	cfg, err = Load(LoadOptions{File: file, Env: env})
	if err != nil || cfg.Cran.ArchiveMoverEnabled {
		t.Fatalf("env: %+v %v", cfg.Cran, err)
	}
	cfg, err = Load(
		LoadOptions{
			File:  file,
			Env:   env,
			Flags: map[string]string{"cran.archive_mover_enabled": "true"},
		},
	)
	if err != nil || !cfg.Cran.ArchiveMoverEnabled {
		t.Fatalf("flags: %+v %v", cfg.Cran, err)
	}
	if _, err := Load(
		LoadOptions{Flags: map[string]string{"cran.archive_mover_enabled": "yes"}},
	); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}
