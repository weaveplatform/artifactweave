package npm_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/token"
)

// TestAccNPMAuthLinesHandBackTheAPIKey: an API key authenticates a request
// that sends one whatever its Authorization header says, as 7.x's props
// filter, which comes before the Basic filter, does, so GET /api/npm/auth
// and its scoped form write the key into .npmrc, never a Basic or Bearer
// value beside it, which nothing verified. X-Api-Key, the key header's
// older name, is read as 7.x reads it, after a blank X-JFrog-Art-Api
// (AuthenticationFilterUtils.getApiKeyTokenKeyValue, 7.161.26; #1464).
func TestAccNPMAuthLinesHandBackTheAPIKey(t *testing.T) {
	w := newNPMAuthWorld(t)
	issued, err := w.tokens.Issue(context.Background(), token.Request{
		Username: "admin", IncludeReference: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := issued.ReferenceToken
	with := func(h map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for name, value := range h {
			out[name] = value
		}
		return out
	}
	const reg = "//reg.example/artifactory/api/npm/npm-local/"
	for name, headers := range map[string]map[string]string{
		"X-Api-Key":                      {"X-Api-Key": ref},
		"X-Api-Key after a blank header": {"X-JFrog-Art-Api": " ", "X-Api-Key": ref},
		"a key beside Basic":             with(basic("admin", "pw"), "X-JFrog-Art-Api", ref),
		"a key beside a wrong password":  with(basic("admin", "wrong"), "X-Api-Key", ref),
		"a key beside Bearer":            with(bearerAuth(issued.AccessToken), "X-JFrog-Art-Api", ref),
	} {
		rec := do(t, w.h, "GET", "/artifactory/api/npm/auth", headers, "")
		if want := "_authToken = " + ref + "\n"; rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: %d %q, want 200 %q", name, rec.Code, rec.Body, want)
		}
		rec = do(t, w.h, "GET", "/artifactory/api/npm/npm-local/auth/acme", headers, "")
		want := "@acme:registry=https:" + reg + "\n" + reg + ":_authToken=" + ref + "\n"
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s, scoped: %d %q, want 200 %q", name, rec.Code, rec.Body, want)
		}
	}
}
