package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestUnitPyPI covers the pypi block (#537): every option off by default, as
// in 7.x, and the file, environment and flag forms in their precedence,
// with booleans read strictly.
func TestUnitPyPI(t *testing.T) {
	if d := Default().PyPI; d != (PyPI{}) {
		t.Fatalf("default %+v", d)
	}
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(file, []byte("pypi:\n  enforce_layout: true\n  simple_json: true\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.PyPI != (PyPI{EnforceLayout: true, SimpleJSON: true}) {
		t.Fatalf("file: %+v, %v", cfg.PyPI, err)
	}
	env := map[string]string{
		"ARTIFACTWEAVE_PYPI_ENFORCE_NAMING_NORMALIZATION": "true",
		"ARTIFACTWEAVE_PYPI_SIMPLE_JSON":                  "false",
	}
	cfg, err = Load(LoadOptions{File: file, Env: envOf(env)})
	want := PyPI{EnforceLayout: true, EnforceNamingNormalization: true}
	if err != nil || cfg.PyPI != want {
		t.Fatalf("env: %+v, %v", cfg.PyPI, err)
	}
	cfg, err = Load(LoadOptions{
		File: file, Env: envOf(env),
		Flags: map[string]string{"pypi.core_metadata": "true", "pypi.enforce_layout": "false"},
	})
	want = PyPI{EnforceNamingNormalization: true, CoreMetadata: true}
	if err != nil || cfg.PyPI != want {
		t.Fatalf("flags: %+v, %v", cfg.PyPI, err)
	}
	strict := LoadOptions{Flags: map[string]string{"pypi.simple_json": "yes"}}
	if _, err := Load(strict); err == nil {
		t.Error("a boolean that is not one was accepted")
	}
	if _, err := Load(LoadOptions{File: writeFile(t, "pypi:\n  quiet_period: 60\n")}); !errors.Is(
		err, ErrUnknownKey,
	) {
		t.Errorf("an unknown key: %v", err)
	}
}
