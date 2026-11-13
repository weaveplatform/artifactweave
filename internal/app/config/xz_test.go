package config

import "testing"

// xz.dictionary_budget defaults to xzfile's 1 GiB, takes a size from the
// file, the environment and a flag in that order of precedence, takes 0
// to turn the budget off, and refuses what is not a size.
func TestUnitXZDictionaryBudget(t *testing.T) {
	if got := Default().XZ.DictionaryBudget; got != 1<<30 {
		t.Fatalf("default %s, want 1GiB", got)
	}
	file := writeFile(t, "xz:\n  dictionary_budget: 256MiB\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.XZ.DictionaryBudget != 256<<20 {
		t.Fatalf("file: %+v %v", cfg.XZ, err)
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_XZ_DICTIONARY_BUDGET": "0"})
	cfg, err = Load(LoadOptions{File: file, Env: env})
	if err != nil || cfg.XZ.DictionaryBudget != 0 {
		t.Fatalf("env: %+v %v", cfg.XZ, err)
	}
	cfg, err = Load(LoadOptions{
		File: file, Env: env, Flags: map[string]string{"xz.dictionary_budget": "2GiB"},
	})
	if err != nil || cfg.XZ.DictionaryBudget != 2<<30 {
		t.Fatalf("flags: %+v %v", cfg.XZ, err)
	}
	for _, bad := range []string{"-1", "lots", "1TiB"} {
		if _, err := Load(
			LoadOptions{Flags: map[string]string{"xz.dictionary_budget": bad}},
		); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := Load(LoadOptions{Flags: map[string]string{"xz.dictionary": "1GiB"}}); err == nil {
		t.Error("an unknown xz key accepted")
	}
}
