package remote_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// TestAccPolicyMIMERefusal: a resource with ClientMIME is refused 409 with
// 7.x's message for a blocked label, on a cached, an uncacheable (Vary),
// a transient and a HEAD read, and the URL it names is the one asked for
// before a redirect (#1376). Without ClientMIME, as on a format route, the
// refusal stays a 502 integrity failure.
func TestAccPolicyMIMERefusal(t *testing.T) {
	w := policyFixture(t)
	html := http.Header{"Content-Type": {"text/html; charset=utf-8"}}
	page := []byte("<html>login</html>")
	w.up.Set("/a.jar", fakeupstream.Resource{Body: page, Header: html})
	w.up.Set("/vary.jar", fakeupstream.Resource{
		Body: page, Header: http.Header{"Content-Type": {"text/html"}, "Vary": {"*"}},
	})
	w.up.Set("/moved.jar", fakeupstream.Resource{Status: http.StatusFound, Redirect: "/a.jar"})
	wantRefusal := func(q source.Request, upstreamPath string) {
		t.Helper()
		_, err := w.read(q)
		var refused *source.MIMERefusal
		var rejection *format.RejectionError
		want := "Error fetching " + w.up.URL + upstreamPath + ": Mismatching mime types found in request: " +
			"[application/java-archive] and response: text/html, and this repo blocks text/html on mismatch; " +
			"Path: 'remote:" + q.Resource.Path + "'"
		if !errors.As(err, &refused) || !errors.As(err, &rejection) ||
			rejection.Status != http.StatusConflict ||
			rejection.Message != want {
			t.Fatalf("%s %s: %v, want 409 %q", q.Resource.Method, q.Resource.Path, err, want)
		}
	}
	for _, tc := range []struct {
		path, upstream string
		head, store    bool
	}{
		{"a.jar", "/a.jar", false, true},
		{"vary.jar", "/vary.jar", false, true},
		{"a.jar", "/a.jar", false, false},
		{"a.jar", "/a.jar", true, true},
		{"moved.jar", "/moved.jar", false, true},
	} {
		q := w.request(tc.path, source.ResourceKindArtifact)
		q.Resource.ClientMIME = &source.ClientMIME{}
		if tc.head {
			q.Resource.Method = http.MethodHead
		}
		q.Repo.StoreArtifactsLocally = &tc.store
		wantRefusal(q, tc.upstream)
	}

	q := w.request("a.jar", source.ResourceKindArtifact)
	_, err := w.read(q)
	var failure *source.HTTPError
	if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
		failure.Kind != source.FailureKindIntegrity || errors.As(err, new(*source.MIMERefusal)) {
		t.Fatalf("format route: %v, want a 502 integrity failure", err)
	}

	// A HEAD whose label the client accepts, or that has a format hint,
	// relays it unchecked.
	q.Resource.Method = http.MethodHead
	q.Resource.ClientMIME = &source.ClientMIME{Accept: []string{"text/html"}}
	if _, err = w.read(q); err != nil {
		t.Fatalf("HEAD with Accept text/html: %v", err)
	}
	q.Resource.ClientMIME.Accept = nil
	q.Resource.ExpectedContentType = "application/java-archive"
	if _, err = w.read(q); err != nil {
		t.Fatalf("HEAD with a format hint: %v", err)
	}
	if !errors.Is(item.RefuseLabel(w.c, "a.jar", "text/html", nil), item.ErrRemoteMIME) {
		t.Fatal("RefuseLabel no longer refuses the label this test serves")
	}
}

// TestAccPolicyMIMERefusalServesExpiredEntry: a refused label on a read
// whose cached file has expired serves that file, GET and HEAD, with the
// refusal on the result for the source service to remember, and makes the
// entry fresh again, as 7.x's RemoteRepoBase.internalGetInfo serves an
// expired file for a rejected resource and getInfo unexpires it. hardFail
// does not stop it. A format route's read (no ClientMIME) still fails 502.
func TestAccPolicyMIMERefusalServesExpiredEntry(t *testing.T) {
	w := policyFixture(t)
	resource := func(label, body string) fakeupstream.Resource {
		return fakeupstream.Resource{Body: []byte(body), Header: http.Header{
			"Content-Type": {label}, "Cache-Control": {"max-age=10"},
		}}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		path := strings.ToLower(method) + ".jar"
		w.up.Set("/"+path, resource("application/java-archive", "jar bytes"))
		q := w.request(path, source.ResourceKindArtifact)
		q.Resource.ClientMIME = &source.ClientMIME{}
		q.Repo.HardFail = ptr(true)
		w.mustRead(q, "jar bytes")
		w.clock.Advance(11 * time.Second)
		w.up.Set("/"+path, resource("text/html", "<html>login</html>"))
		q.Resource.Method = method
		before := w.up.Count("", "/"+path)
		result, err := w.read(q)
		if err != nil || result.Refused == nil || result.Refused.Status != http.StatusConflict ||
			!strings.Contains(
				result.Refused.Message,
				"and this repo blocks text/html on mismatch",
			) {
			t.Fatalf(
				"%s expired %s: %+v, %v; want the cached file and the refusal",
				method,
				path,
				result,
				err,
			)
		}
		if w.up.Count("", "/"+path) == before {
			t.Fatalf("%s expired %s: no upstream request", method, path)
		}
		if result.Item == nil || result.Item.MimeType != "application/java-archive" {
			t.Fatalf("%s expired %s: item %+v, want the cached file", method, path, result.Item)
		}
		if method == http.MethodGet && w.bytes(result) != "jar bytes" {
			t.Fatalf("GET expired %s: body %q", path, w.bytes(result))
		}

		// Unexpired: the next read within max-age is a hit.
		before = w.up.Count("", "/"+path)
		q.Resource.Method = http.MethodGet
		if again := w.mustRead(q, "jar bytes"); again.Refused != nil {
			t.Fatalf("%s: a hit carries a refusal", path)
		}
		if w.up.Count("", "/"+path) != before {
			t.Fatalf("%s: the served entry was not made fresh again", path)
		}
	}

	w.up.Set("/format.jar", resource("application/java-archive", "jar bytes"))
	q := w.request("format.jar", source.ResourceKindArtifact)
	w.mustRead(q, "jar bytes")
	w.clock.Advance(11 * time.Second)
	w.up.Set("/format.jar", resource("text/html", "<html>login</html>"))
	var failure *source.HTTPError
	if _, err := w.read(q); !errors.As(err, &failure) || failure.Status != http.StatusBadGateway {
		t.Fatalf("format route on an expired entry: %v, want 502", err)
	}
}
