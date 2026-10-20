package config

import (
	"errors"
	"testing"
)

func TestHelm(t *testing.T) {
	if !Default().Helm.RelativeURLs {
		t.Fatal("relative URLs must default on")
	}
	file := writeFile(t, "helm:\n  relative_urls: false\n")
	cfg, err := Load(LoadOptions{File: file})
	if err != nil || cfg.Helm.RelativeURLs {
		t.Fatalf("file: %+v %v", cfg.Helm, err)
	}
	env := envOf(map[string]string{"ARTIFACTWEAVE_HELM_RELATIVE_URLS": "true"})
	cfg, err = Load(LoadOptions{File: file, Env: env})
	if err != nil || !cfg.Helm.RelativeURLs {
		t.Fatalf("env: %+v %v", cfg.Helm, err)
	}
	cfg, err = Load(
		LoadOptions{File: file, Env: env, Flags: map[string]string{"helm.relative_urls": "false"}},
	)
	if err != nil || cfg.Helm.RelativeURLs {
		t.Fatalf("flags: %+v %v", cfg.Helm, err)
	}
	if _, err := Load(
		LoadOptions{Flags: map[string]string{"helm.relative_urls": "yes"}},
	); err == nil {
		t.Fatal("accepted invalid boolean")
	}
	if _, err := Load(
		LoadOptions{File: writeFile(t, "helm:\n  unknown: true\n")},
	); !errors.Is(
		err,
		ErrUnknownKey,
	) {
		t.Fatalf("unknown key: %v", err)
	}
}
