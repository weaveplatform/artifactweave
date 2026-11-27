package api_test

import (
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"testing"
)

func TestUnitNativeOrigin(t *testing.T) {
	for _, s := range []string{"https://registry.example", "https://registry.example:8443", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if !api.ValidNativeOrigin(s) {
			t.Error("refused", s)
		}
	}
	for _, s := range []string{"", "https://:443", "https://example?", "https://example/", "https://user@example", "https://example#fragment", "https://example?query=1", "http://example", "//example", "javascript:foo", "https://EXAMPLE", "https://example:", "https://example:999999", "https://%65xample", "https://example/../other"} {
		if api.ValidNativeOrigin(s) {
			t.Error("accepted", s)
		}
	}
}
