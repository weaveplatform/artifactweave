package api_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

func TestUnitNativeOrigin(t *testing.T) {
	for _, s := range []string{"https://registry.example", "https://registry.example:8443", "https://xn--bcher-kva.example", "https://[2001:db8::1]:8443", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if !api.ValidNativeOrigin(s) {
			t.Error("refused", s)
		}
	}
	for _, s := range []string{"", "https://:443", "https://example?", "https://example/", "https://user@example", "https://example#fragment", "https://example?query=1", "http://example", "//example", "javascript:foo", "https://EXAMPLE", "https://example:", "https://example:999999", "https://%65xample", "https://example/../other", "https://login.example:443", "http://localhost:80", "https://login.example:08443", "https://bücher.example", "https://127.1", "https://2130706433", "https://0x7f000001", "https://127.0.0.01", "https://127.0.0.1.", "https://[2001:0db8::1]", "https://[::ffff:127.0.0.1]", "https://[fe80::1%25en0]", "https://::1"} {
		if api.ValidNativeOrigin(s) {
			t.Error("accepted", s)
		}
	}
}
