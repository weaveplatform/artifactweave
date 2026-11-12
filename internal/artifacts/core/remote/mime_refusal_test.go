package remote_test

import (
	"errors"
	"net/http"
	"testing"

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
