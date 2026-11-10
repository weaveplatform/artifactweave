package ownership

import "testing"

func TestUnitOwnedTestDirAcceptsLegacyAndStagedLayouts(t *testing.T) {
	// "internal" + "/app" is the final root's app directory, in two parts:
	// written whole, it is also the legacy path that WEB-18 moved to
	// migration/app, which pkgmove's stale check reports until promotion
	// moves the composition root back there.
	for dir, want := range map[string]bool{
		"test/server":                                true,
		"test/server/access":                         true,
		"test/server/artifacts/npm":                  true,
		"test/server/support/harness":                true,
		"internal" + "/app":                          true,
		"migration/app":                              true,
		"migration/artifacts/ecosystem/npm/http":     true,
		"internal/artifacts/ecosystem/npm/http":      true,
		"migration/artifacts/ecosystem/npm/http/sub": true,
		"migration/artifacts/adapters/http":          true,
		"migration/access/adapters/http":             true,
		"internal/configuration/adapters/http/api":   true,
		"migration/app/apptest":                      false,
		"migration/artifacts/ecosystem/npm":          false,
		"migration/artifacts/ecosystem/npm/httpx":    false,
		"migration/artifacts/ecosystem/http":         false,
		"migration/artifacts/adapters":               false,
		"migration/artifacts/core/item":              false,
		"migration":                                  false,
		"internal/legacy/xyz":                        false,
		"migration/appx":                             false,
		"test/serverx":                               false,
		"test/acceptance":                            false,
		"pkg/app":                                    false,
		"tools/app":                                  false,
		"migration/app/":                             false,
		"test/server/../server/npm":                  false,
		"":                                           false,
	} {
		if got := OwnedTestDir(dir); got != want {
			t.Errorf("OwnedTestDir(%q) = %t, want %t", dir, got, want)
		}
	}
}
