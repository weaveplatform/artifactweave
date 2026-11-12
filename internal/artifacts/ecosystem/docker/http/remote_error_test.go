package dockerhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitRemoteErrorEnvelopes: a remote's failure to fetch, whether its
// upstream's 401, 403, 429 or 5xx relayed or an upstream that is unreachable,
// unusable or slow, answers in Artifactory's envelope under no registry code
// (#1248), the 401 with 7.x's message naming the remote (RTFACT-30467) and
// a challenge that stops containerd's retries (#1277). The caller's own
// refusal answers 7.x's 403 UNAUTHORIZED, as Require does (#1276), and a
// request the source cannot serve or an operation it does not support keeps
// its code. No refusal writes a code into kernel's shared errors.
func TestUnitRemoteErrorEnvelopes(t *testing.T) {
	forbidden, unauthenticated := *kernel.ErrForbidden, *kernel.ErrUnauthenticated
	for _, c := range []struct {
		name       string
		err        error
		anonymous  bool
		status     int
		body       string
		retryAfter string
		challenge  string
	}{
		{
			name: "upstream 401", err: &source.HTTPError{Status: 401}, status: 401,
			body: `{"errors":[{"message":"Invalid username/password configured for Remote Docker repository: remote","status":401}]}`,
			// No base URL resolver runs here, so the realm is relative and the
			// service empty; the server suite checks the full value.
			challenge: `Bearer realm="/artifactory/api/docker/remote/v2/token",service="",` +
				`scope="repository:remote/image:pull",error="invalid_request"`,
		},
		{
			name: "upstream 403", err: &source.HTTPError{Status: 403}, status: 403,
			body: `{"errors":[{"message":"Forbidden","status":403}]}`,
		},
		{
			name: "upstream 429", err: &source.HTTPError{Status: 429, RetryAfter: 7 * time.Second},
			status: 429, retryAfter: "7",
			body: `{"errors":[{"message":"Too Many Requests","status":429}]}`,
		},
		{
			name: "upstream 502", err: &source.HTTPError{Status: 502}, status: 502,
			body: `{"errors":[{"message":"Bad Gateway","status":502}]}`,
		},
		{
			name: "upstream 503", err: &source.HTTPError{Status: 503}, status: 503,
			body: `{"errors":[{"message":"Service Unavailable","status":503}]}`,
		},
		{
			name: "unreachable", err: source.ErrUnavailable, status: 502,
			body: `{"errors":[{"message":"Bad Gateway","status":502}]}`,
		},
		{
			name: "unusable answer", err: source.ErrInvalidResult, status: 502,
			body: `{"errors":[{"message":"Bad Gateway","status":502}]}`,
		},
		{
			name: "cut short", err: context.Canceled, status: 502,
			body: `{"errors":[{"message":"Bad Gateway","status":502}]}`,
		},
		{
			name: "timeout", err: context.DeadlineExceeded, status: 504,
			body: `{"errors":[{"message":"Gateway Timeout","status":504}]}`,
		},
		{
			name: "superseded", err: source.ErrSuperseded, status: 503,
			body: `{"errors":[{"message":"Service Unavailable","status":503}]}`,
		},
		{
			name: "caller denied", err: source.ErrDenied, status: 403,
			body: `{"errors":[{"code":"UNAUTHORIZED","message":"The client does not have permission for manifest: Download request for repo:path 'remote-cache:image/latest/manifest.json' is forbidden for user: 'reader'."}]}`,
		},
		{
			name: "anonymous caller denied", err: source.ErrDenied, anonymous: true, status: 403,
			body: `{"errors":[{"code":"UNAUTHORIZED","message":"The client does not have permission for manifest: Download request for repo:path 'remote-cache:image/latest/manifest.json' is forbidden for user: 'anonymous'."}]}`,
		},
		{
			name: "invalid request", err: source.ErrInvalidRequest, status: 400,
			body: `{"errors":[{"code":"BAD_REQUEST","message":"Bad Request"}]}`,
		},
		{
			name: "unsupported", err: source.ErrUnsupported, status: 501,
			body: `{"errors":[{"code":"UNSUPPORTED","message":"Not Implemented"}]}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := kernel.Principal{Name: "reader"}
			if c.anonymous {
				p = kernel.Principal{Name: kernel.AnonymousUser, Anonymous: true}
			}
			r := httptest.NewRequest(http.MethodGet, "/v2/remote/image/manifests/latest", nil)
			r = r.WithContext(kernel.WithPrincipal(r.Context(), p))
			w := httptest.NewRecorder()
			q := Request{
				Kind:      KindManifest,
				RepoKey:   "remote",
				Repo:      repo.Config{Key: "remote", Class: repo.Remote},
				Name:      "image",
				Reference: "latest",
			}
			(&Registry{}).remoteError(w, r, q, c.err)
			if w.Code != c.status || w.Body.String() != c.body+"\n" ||
				w.Header().Get("Content-Type") != "application/json" ||
				w.Header().Get("Retry-After") != c.retryAfter ||
				w.Header().Get("WWW-Authenticate") != c.challenge {
				t.Fatalf("%d %s %v, want %d %s", w.Code, w.Body, w.Header(), c.status, c.body)
			}
		})
	}
	if *kernel.ErrForbidden != forbidden || *kernel.ErrUnauthenticated != unauthenticated {
		t.Fatalf("a refusal changed kernel's shared errors: %+v, %+v",
			*kernel.ErrForbidden, *kernel.ErrUnauthenticated)
	}
}
