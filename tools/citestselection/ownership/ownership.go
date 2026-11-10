package ownership

import (
	"path"
	"strings"
)

// OwnedTestDir reports whether the tests in dir, a slash-separated directory
// relative to the repository root, need a reviewed owner: server assembly
// tests, the application tests and the HTTP adapter tests. P9d builds the
// target tree under migration/ and then renames it to internal/ (decision
// 0044), so each staged pattern, <root>/app among them, accepts both roots. A
// directory that is not in clean form, such as "test/server/artifacts/npm/",
// is never owned.
func OwnedTestDir(dir string) bool {
	// An unclean inventory key such as "test/server/artifacts/npm/" could
	// never equal a test file's directory, so its tests would stay unmapped.
	if dir != path.Clean(dir) {
		return false
	}
	if dir == "test/server" || strings.HasPrefix(dir, "test/server/") {
		return true
	}
	parts := strings.Split(dir, "/")
	if parts[0] != "migration" && parts[0] != "internal" {
		return false
	}
	switch {
	case len(parts) == 2:
		return parts[1] == "app"
	case len(parts) >= 5 && parts[1] == "artifacts" && parts[2] == "ecosystem":
		return parts[4] == "http"
	case len(parts) >= 4:
		return parts[2] == "adapters" && parts[3] == "http"
	}
	return false
}
