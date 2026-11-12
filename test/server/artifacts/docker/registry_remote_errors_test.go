package docker_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

// TestAccRegistryRemoteUpstreamErrors: a remote Docker repository answers its
// upstream's 401, 403, 429 and 502 for a manifest with the same status in
// Artifactory's envelope, under no registry code (decision 0031, #1248), and
// never with the upstream's body. The 401 carries 7.x's message, which
// names the remote (RTFACT-30467). The upstream's 401 challenges for Basic
// after the remote sent its credentials, so it refuses them, as nvcr.io
// refused 7.x's in RTFACT-30467. The 429 keeps the upstream's Retry-After,
// and the 401 carries the registry's own Bearer challenge, as it did before.
func TestAccRegistryRemoteUpstreamErrors(t *testing.T) {
	for _, c := range []struct {
		status     int
		header     http.Header
		body       string
		retryAfter string
	}{
		{
			status: 401, header: http.Header{"Www-Authenticate": {`Basic realm="upstream"`}},
			body: `{"errors":[{"message":"Invalid username/password configured for Remote Docker repository: remote","status":401}]}`,
		},
		{status: 403, body: `{"errors":[{"message":"Forbidden","status":403}]}`},
		{
			status: 429, header: http.Header{"Retry-After": {"7"}}, retryAfter: "7",
			body: `{"errors":[{"message":"Too Many Requests","status":429}]}`,
		},
		{status: 502, body: `{"errors":[{"message":"Bad Gateway","status":502}]}`},
	} {
		t.Run(strconv.Itoa(c.status), func(t *testing.T) {
			w := registryWorld(t)
			w.up.Set("/v2/team/image/manifests/latest", fakeupstream.Resource{
				Status: c.status, Header: c.header,
				Body: []byte(`{"errors":[{"code":"DENIED","message":"upstream detail"}]}`),
			})
			got := w.request("GET", "/v2/remote/team/image/manifests/latest", "reader", "")
			remoteStatus(t, got, c.status)
			if got.Body.String() != c.body+"\n" ||
				got.Header().Get("Content-Type") != "application/json" ||
				got.Header().Get("Retry-After") != c.retryAfter {
				t.Fatalf("%s %v, want %s", got.Body, got.Header(), c.body)
			}
			challenge := got.Header().Get("WWW-Authenticate")
			if (c.status == 401) != strings.HasPrefix(challenge, `Bearer realm="`) {
				t.Fatalf("challenge %q on %d", challenge, c.status)
			}
		})
	}
}
