package dockerhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// pullRequest drops every value of each header a pull ignores from a copy,
// keeps If-None-Match and If-Modified-Since, which a pull answers with 304,
// and leaves the request it was given as it was sent. A request without
// any of them is returned as it is.
func TestUnitPullRequest(t *testing.T) {
	kept := []string{"If-None-Match", "If-Modified-Since", "Accept"}
	plain := httptest.NewRequest(http.MethodGet, "/v2/docker-local/app/manifests/v1", nil)
	for _, k := range kept {
		plain.Header.Set(k, k)
	}
	if got := pullRequest(plain); got != plain {
		t.Fatal("a request without an ignored header was copied")
	}
	for _, name := range ignoredOnPull {
		r := httptest.NewRequest(http.MethodHead, "/v2/docker-local/app/manifests/v1", nil)
		r.Header.Add(name, "a")
		r.Header.Add(name, "b")
		for _, k := range kept {
			r.Header.Set(k, k)
		}
		got := pullRequest(r)
		if got == r || len(got.Header.Values(name)) != 0 {
			t.Fatalf("%s: %v", name, got.Header)
		}
		for _, k := range kept {
			if got.Header.Get(k) != k {
				t.Fatalf("%s: %s dropped: %v", name, k, got.Header)
			}
		}
		if len(r.Header.Values(name)) != 2 {
			t.Fatalf("%s: the caller's request changed: %v", name, r.Header)
		}
	}
}
