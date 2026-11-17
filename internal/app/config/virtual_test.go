package config

import "testing"

// virtual.metadata_cache defaults to the cache's own bounds, takes them
// from the file, the environment and a flag in that order of precedence,
// hands them to the artifacts module, and refuses an empty cache, a value
// that is not a number or a size, and an unknown key.
func TestUnitVirtualMetadataCache(t *testing.T) {
	if got := Default().Virtual.MetadataCache; got.MaxEntries != 10000 || got.MaxSize != 256<<20 {
		t.Fatalf("default %+v", got)
	}
	file := writeFile(t, "virtual:\n  metadata_cache:\n    max_entries: 50\n    max_size: 1MiB\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.Virtual.MetadataCache.MaxEntries != 50 ||
		cfg.Virtual.MetadataCache.MaxSize != 1<<20 {
		t.Fatalf("file: %+v %v", cfg.Virtual, err)
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_VIRTUAL_METADATA_CACHE_MAX_ENTRIES": "60"})
	cfg, err = Load(LoadOptions{File: file, Env: env, Flags: map[string]string{
		"virtual.metadata_cache.max_size": "2MiB",
	}})
	if err != nil || cfg.Virtual.MetadataCache.MaxEntries != 60 ||
		cfg.Virtual.MetadataCache.MaxSize != 2<<20 {
		t.Fatalf("env and flags: %+v %v", cfg.Virtual, err)
	}
	if s := cfg.Artifacts(); s.VirtualCacheEntries != 60 || s.VirtualCacheBytes != 2<<20 {
		t.Fatalf("artifacts settings: %d %d", s.VirtualCacheEntries, s.VirtualCacheBytes)
	}
	for key, bad := range map[string]string{
		"virtual.metadata_cache.max_entries": "0",
		"virtual.metadata_cache.max_size":    "0",
		"virtual.metadata_cache.max_size ":   "1MiB",
		"virtual.metadata_cache":             "1",
	} {
		if _, err := Load(LoadOptions{Flags: map[string]string{key: bad}}); err == nil {
			t.Errorf("%s=%s accepted", key, bad)
		}
	}
	for key, bad := range map[string]string{
		"virtual.metadata_cache.max_entries": "many",
		"virtual.metadata_cache.max_size":    "-1",
	} {
		if _, err := Load(LoadOptions{Flags: map[string]string{key: bad}}); err == nil {
			t.Errorf("%s=%s accepted", key, bad)
		}
	}
}
