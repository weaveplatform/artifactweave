package httpkit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

func prefix(p string) func(string) bool {
	return func(path string) bool { return strings.HasPrefix(path, p) }
}

// rules puts a narrow rule before a broader one with the same prefix, as
// the server's npm operations come before npm's registry.
var rules = []httpkit.ProfileRule{
	{Match: prefix("/api/npm/auth"), Profile: apierror.ProfileArtifactory},
	{Match: prefix("/api/npm/"), Profile: apierror.ProfileNPM},
	{Match: prefix("/api/go/"), Profile: apierror.ProfileText},
}

func TestUnitProfileForFirstMatchWins(t *testing.T) {
	for path, want := range map[string]apierror.Profile{
		"/api/npm/auth":     apierror.ProfileArtifactory,
		"/api/npm/pkg":      apierror.ProfileNPM,
		"/api/go/mod/@v":    apierror.ProfileText,
		"/api/unclaimed/x":  apierror.ProfileArtifactory,
		"":                  apierror.ProfileArtifactory,
		"/api/go-not-a-dir": apierror.ProfileArtifactory,
	} {
		if got := httpkit.ProfileFor(rules, path); got != want {
			t.Errorf("ProfileFor(%q) = %v, want %v", path, got, want)
		}
	}
	if got := httpkit.ProfileFor(nil, "/api/npm/pkg"); got != apierror.ProfileArtifactory {
		t.Errorf("no rules: %v", got)
	}
}

func TestUnitErrorProfileReadsTheRequestsRules(t *testing.T) {
	bare := httptest.NewRequest(http.MethodGet, "/api/npm/pkg", nil)
	if got := httpkit.ErrorProfile(bare); got != apierror.ProfileArtifactory {
		t.Fatalf("request without rules: %v", got)
	}
	var seen apierror.Profile
	h := httpkit.ErrorProfiles(
		rules,
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = httpkit.ErrorProfile(r)
		}),
	)
	h.ServeHTTP(httptest.NewRecorder(), bare)
	if seen != apierror.ProfileNPM {
		t.Fatalf("through ErrorProfiles: %v", seen)
	}
	direct := bare.WithContext(httpkit.WithProfileRules(context.Background(), rules))
	if got := httpkit.ErrorProfile(direct); got != apierror.ProfileNPM {
		t.Fatalf("WithProfileRules: %v", got)
	}
}

func TestUnitContextValues(t *testing.T) {
	bare := context.Background()
	if httpkit.RequestIDFrom(bare) != "" || httpkit.OriginalContentType(bare) != "" ||
		httpkit.Accepts(bare, "application/json") || httpkit.RawBody(bare) != nil ||
		httpkit.RawSearchQuery(bare) != "" {
		t.Fatal("a bare context carries a value")
	}
	// Without the access log's slots these are no-ops, not panics.
	httpkit.NoteCause(bare, errors.New("lost"))
	httpkit.NoteUser(bare, "admin")

	ctx := httpkit.WithRequestID(bare, "rid")
	ctx = httpkit.WithOriginalContentType(ctx, "application/vnd.x+json")
	ctx = httpkit.WithAccept(ctx, "application/vnd.x+json, */*")
	ctx = httpkit.WithRawBody(ctx, []byte("{}"))
	ctx = httpkit.WithSearchQuery(ctx, "a=1&b")
	var cause error
	var user string
	ctx = httpkit.WithUser(httpkit.WithCause(ctx, &cause), &user)
	httpkit.NoteCause(ctx, errors.New("disk"))
	httpkit.NoteUser(ctx, "deployer")
	switch {
	case httpkit.RequestIDFrom(ctx) != "rid":
		t.Fatal("request id")
	case httpkit.OriginalContentType(ctx) != "application/vnd.x+json":
		t.Fatal("original content type")
	case !httpkit.Accepts(ctx, "application/vnd.x+json") || httpkit.Accepts(ctx, "text/html"):
		t.Fatal("accepts")
	case string(httpkit.RawBody(ctx)) != "{}":
		t.Fatal("raw body")
	case httpkit.RawSearchQuery(ctx) != "a=1&b":
		t.Fatal("raw search query")
	case cause == nil || cause.Error() != "disk":
		t.Fatalf("cause %v", cause)
	case user != "deployer":
		t.Fatalf("user %q", user)
	}
}

func TestUnitWriteErrNotesOnlyUntypedCauses(t *testing.T) {
	var cause error
	r := httptest.NewRequest(http.MethodGet, "/api/go/x", nil)
	r = r.WithContext(httpkit.WithCause(httpkit.WithProfileRules(r.Context(), rules), &cause))

	rec := httptest.NewRecorder()
	httpkit.WriteErr(rec, r, apierror.New(http.StatusNotFound, "no such module"))
	if rec.Code != http.StatusNotFound || cause != nil {
		t.Fatalf("typed: %d, cause %v", rec.Code, cause)
	}
	// ProfileText, the rule's profile, answers in plain text.
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("typed: content type %q", rec.Header().Get("Content-Type"))
	}

	rec = httptest.NewRecorder()
	httpkit.WriteErr(rec, r, errors.New("sqlite: disk I/O error"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "sqlite") {
		t.Fatalf("untyped: %d %s", rec.Code, rec.Body.String())
	}
	if cause == nil || cause.Error() != "sqlite: disk I/O error" {
		t.Fatalf("untyped: cause %v", cause)
	}
}

func TestUnitWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	httpkit.WriteJSON(rec, http.StatusCreated, map[string]int{"n": 1})
	if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/json" ||
		rec.Body.String() != "{\"n\":1}\n" {
		t.Fatalf("%d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestUnitEscapePathAndFormatTime(t *testing.T) {
	if got := httpkit.EscapePath("a b/c%d/é"); got != "a%20b/c%25d/%C3%A9" {
		t.Fatalf("EscapePath = %q", got)
	}
	at := time.Date(2026, 1, 2, 4, 4, 5, 6_000_000, time.FixedZone("CET", 3600))
	if got := httpkit.FormatTime(at); got != "2026-01-02T03:04:05.006Z" {
		t.Fatalf("FormatTime = %q", got)
	}
}

func TestUnitChiPatterns(t *testing.T) {
	got := httpkit.ChiPatterns("/artifactory/{repoKey}/{itemPath}")
	want := []string{
		"/artifactory/{repoKey:" + httpkit.NotAPI + "}/{itemPath}",
		"/artifactory/{repoKey:" + httpkit.NotAPI + "}/*",
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("item route: %q", got)
	}
	if got := httpkit.ChiPatterns("/artifactory/api/system/ping"); len(got) != 1 ||
		got[0] != "/artifactory/api/system/ping" {
		t.Fatalf("fixed route: %q", got)
	}
}

func TestUnitPointers(t *testing.T) {
	var none *int
	if httpkit.Deref(none) != 0 || *httpkit.PtrTo(3) != 3 ||
		httpkit.Deref(httpkit.PtrTo("x")) != "x" {
		t.Fatal("Deref or PtrTo")
	}
	if httpkit.DerefSlice(nil) != nil {
		t.Fatal("DerefSlice(nil)")
	}
	s := []string{"a"}
	if got := httpkit.DerefSlice(&s); len(got) != 1 || got[0] != "a" {
		t.Fatalf("DerefSlice = %q", got)
	}
}
