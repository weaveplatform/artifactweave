package config

import (
	"errors"
	"testing"
)

func TestUnitNativeLoginConfig(t *testing.T) {
	if Default().NativeLogin.Enabled {
		t.Fatal("approval enabled by default")
	}
	env := map[string]string{"ARTIFACTWEAVE_NATIVE_LOGIN_ENABLED": "true", "ARTIFACTWEAVE_NATIVE_LOGIN_ORIGIN": "https://login.example"}
	cfg, err := Load(LoadOptions{Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	if err != nil || !cfg.Access().NativeLoginEnabled || cfg.Access().NativeLoginOrigin != "https://login.example" {
		t.Fatal(cfg.NativeLogin, err)
	}
	for _, origin := range []string{"", "http://evil.example", "https://example/path", "https://example?", "https://:443", "https://example:443", "https://example:08443", "https://bücher.example", "https://127.1", "https://2130706433", "https://0x7f000001", "https://[2001:0db8::1]", "https://[fe80::1%25en0]"} {
		if _, err := Load(LoadOptions{Flags: map[string]string{"native_login.enabled": "true", "native_login.origin": origin}}); !errors.Is(err, ErrInvalidValue) {
			t.Fatal(origin, err)
		}
	}
}
