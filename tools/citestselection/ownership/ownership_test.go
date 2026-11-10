package ownership

import "testing"

func TestUnitOwnedTestDirAcceptsLegacyAndStagedLayouts(t *testing.T) {
	// "internal" + "/app" is the final root's app directory, in two parts:
	// written whole, it is also the legacy path that WEB-18 moved to
	// internal/app, which pkgmove's stale check reports until promotion
	// moves the composition root back there.
	for dir, want := range map[string]bool{
		"test/server":                               true,
		"test/server/access":                        true,
		"test/server/artifacts/npm":                 true,
		"test/server/support/harness":               true,
		"internal" + "/app":                         true,
		"internal/app":                              true,
		"internal/artifacts/ecosystem/npm/http":     true,
		"internal/artifacts/ecosystem/npm/http":     true,
		"internal/artifacts/ecosystem/npm/http/sub": true,
		"internal/artifacts/adapters/http":          true,
		"internal/access/adapters/http":             true,
		"internal/configuration/adapters/http/api":  true,
		"internal/app/apptest":                      false,
		"internal/artifacts/ecosystem/npm":          false,
		"internal/artifacts/ecosystem/npm/httpx":    false,
		"internal/artifacts/ecosystem/http":         false,
		"internal/artifacts/adapters":               false,
		"internal/artifacts/core/item":              false,
		"migration":                                 false,
		"internal/legacy/xyz":                       false,
		"migration/appx":                            false,
		"test/serverx":                              false,
		"test/acceptance":                           false,
		"pkg/app":                                   false,
		"tools/app":                                 false,
		"internal/app/":                             false,
		"test/server/../server/npm":                 false,
		"":                                          false,
	} {
		if got := OwnedTestDir(dir); got != want {
			t.Errorf("OwnedTestDir(%q) = %t, want %t", dir, got, want)
		}
	}
}
