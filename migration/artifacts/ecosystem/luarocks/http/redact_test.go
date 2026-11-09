package luarockshttp_test

import (
	"testing"

	luarockshttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/luarocks/http"
)

func TestUnitRedactAPIKey(t *testing.T) {
	for path, want := range map[string]string{
		"/artifactory/api/luarocks/rocks/api/1/secret/upload": "/artifactory/api/luarocks/rocks/api/1/[redacted]/upload",
		"/artifactory/api/luarocks/rocks/api/tool_version":    "/artifactory/api/luarocks/rocks/api/tool_version",
		"/artifactory/api/luarocks/rocks/manifest":            "/artifactory/api/luarocks/rocks/manifest",
		"/artifactory/api/npm/r/-/user/token/secret":          "/artifactory/api/npm/r/-/user/token/secret",
	} {
		if got := luarockshttp.RedactAPIKey(path); got != want {
			t.Errorf("RedactAPIKey(%s) = %s", path, got)
		}
	}
}
