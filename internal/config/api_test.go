package config

import (
	"errors"
	"testing"
	"time"
)

func TestUnitAPILimitsConfig(t *testing.T) {
	file := writeFile(t, "api:\n  principal_per_minute: 12\n  expensive_timeout: 3m\n")
	cfg, err := Load(LoadOptions{File: file, Env: envOf(map[string]string{
		"ARTIFACTWEAVE_API_PRINCIPAL_PER_MINUTE": "13",
	}), Flags: map[string]string{"api.principal_per_minute": "14"}})
	if err != nil || cfg.API.PrincipalPerMinute != 14 || cfg.API.ExpensiveTimeout != 3*time.Minute {
		t.Fatalf("API policy precedence: %+v, %v", cfg.API, err)
	}
	cfg, err = Load(LoadOptions{Flags: map[string]string{
		"api.enabled": "false", "api.authentication_per_minute": "1000", "api.global_per_minute": "900",
		"api.principal_per_minute": "100", "api.project_per_minute": "500", "api.repository_per_minute": "400",
		"api.expensive_per_minute": "50", "api.expensive_principal_per_minute": "10",
		"api.max_concurrent": "12", "api.expensive_concurrent": "4", "api.expensive_principal_concurrent": "1",
		"api.max_keys": "64", "api.budget_timeout": "250ms", "api.expensive_timeout": "1m",
	}})
	if err != nil || cfg.API.Enabled || cfg.API.MaxKeys != 64 || cfg.API.ExpensivePrincipalConcurrent != 1 ||
		cfg.API.AuthenticationPerMinute != 1000 || cfg.API.BudgetTimeout != 250*time.Millisecond {
		t.Fatalf("API flag policy: %+v, %v", cfg.API, err)
	}
	for key, value := range map[string]string{
		"api.global_per_minute": "bad", "api.principal_per_minute": "0", "api.max_concurrent": "-1",
		"api.max_keys": "100000", "api.expensive_timeout": "0s", "api.budget_timeout": "1h",
	} {
		if _, err := Load(LoadOptions{Flags: map[string]string{key: value}}); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("invalid %s=%s: %v", key, value, err)
		}
	}
	if _, err := Load(LoadOptions{Flags: map[string]string{"api.misspelled": "1"}}); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown policy: %v", err)
	}
}
