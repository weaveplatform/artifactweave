package ownership

import "testing"

func TestUnitOwnedTestDirAcceptsServerAppAndAdapterDirs(t *testing.T) {
	for dir, want := range map[string]bool{
		"test/server":                               true,
		"test/server/access":                        true,
		"test/server/artifacts/npm":                 true,
		"test/server/support/harness":               true,
		"internal/app":                              true,
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
		"internal/legacy/xyz":                       false,
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
