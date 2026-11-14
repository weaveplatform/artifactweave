package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnitWriteEnvelopes(t *testing.T) {
	cases := []struct {
		name     string
		profile  Profile
		err      error
		status   int
		expected string
	}{
		{
			"artifactory",
			ProfileArtifactory,
			New(404, "not found"),
			404,
			`{"errors":[{"message":"not found","status":404}]}`,
		},
		{
			"access",
			ProfileAccess,
			New(409, "exists"),
			409,
			`{"errors":[{"code":"CONFLICT","message":"exists"}]}`,
		},
		{
			"oci",
			ProfileOCI,
			&Error{
				Status:  404,
				Code:    "BLOB_UNKNOWN",
				Message: "blob unknown",
				Detail:  map[string]string{"digest": "sha256:abc"},
			},
			404,
			`{"errors":[{"code":"BLOB_UNKNOWN","detail":{"digest":"sha256:abc"},"message":"blob unknown"}]}`,
		},
		{
			"oci no detail",
			ProfileOCI,
			&Error{Status: 400, Code: "NAME_INVALID", Message: "bad name"},
			400,
			`{"errors":[{"code":"NAME_INVALID","message":"bad name"}]}`,
		},
		{
			"xray",
			ProfileXray,
			NotSupported("Xray"),
			501,
			`{"error":"Xray is not supported by artifactweave"}`,
		},
		{"npm", ProfileNPM, New(401, "unauthorized"), 401, `{"error":"unauthorized"}`},
		{
			"terraform",
			ProfileTerraformRegistry,
			New(404, "no such provider"),
			404,
			`{"errors":["no such provider"]}`,
		},
		{
			"distribution",
			ProfileJFrogDistribution,
			&Error{Status: 404, Code: "NOT_FOUND", Message: "no bundle", Detail: "x"},
			404,
			`{"detail":"x","errors":[{"message":"no bundle","status":404}],"message":"no bundle","status_code":404}`,
		},
		{
			"distribution no detail",
			ProfileJFrogDistribution,
			New(400, "bad"),
			400,
			`{"errors":[{"message":"bad","status":400}],"message":"bad","status_code":400}`,
		},
		{
			"artifactory legacy",
			ProfileArtifactoryLegacy,
			New(400, "policy is running"),
			400,
			`{"error":"Bad Request","errors":[{"message":"policy is running","status":400}],"message":"policy is running"}`,
		},
		{
			"import and export",
			ProfileImportExport,
			New(501, "not implemented"),
			501,
			`{"error":"not implemented","errors":[{"message":"not implemented","status":501}]}`,
		},
		{
			"platform federation",
			ProfilePlatformFederation,
			New(409, "exists"),
			409,
			`{"code":409,"message":"exists"}`,
		},
		// The /pfed envelope has no details field, so Detail is not sent.
		{
			"platform federation details",
			ProfilePlatformFederation,
			&Error{Status: 400, Code: "INVALID", Message: "bad", Detail: []string{"name"}},
			400,
			`{"code":400,"message":"bad"}`,
		},
		{"grid", ProfileGrid, New(409, "exists"), 409, `{"code":"CONFLICT","message":"exists"}`},
		{
			"grid details",
			ProfileGrid,
			&Error{Status: 400, Code: "INVALID", Message: "bad", Detail: []string{"name"}},
			400,
			`{"code":"INVALID","details":["name"],"message":"bad"}`,
		},
		{
			"unknown error",
			ProfileArtifactory,
			errors.New("boom"),
			500,
			`{"errors":[{"message":"internal error","status":500}]}`,
		},
		{
			"wrapped",
			ProfileAccess,
			errors.Join(errors.New("ctx"), New(403, "denied")),
			403,
			`{"errors":[{"code":"FORBIDDEN","message":"denied"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Write(rec, tc.profile, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content type %q", ct)
			}
			var got, want any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.expected), &want); err != nil {
				t.Fatal(err)
			}
			if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
				t.Fatalf("body %s, want %s", rec.Body.String(), tc.expected)
			}
		})
	}
}

func TestUnitWriteText(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, ProfileText, New(http.StatusGone, "gone"))
	if rec.Code != http.StatusGone ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
		rec.Body.String() != "gone\n" {
		t.Fatalf(
			"unexpected text response: %d %q %q",
			rec.Code,
			rec.Header().Get("Content-Type"),
			rec.Body.String(),
		)
	}
}

// TestUnitDefaultCodes: in the Access, Grid and Pub envelopes, each status
// that a handler, the middleware or a remote's upstream can send through them
// carries a code of its own beside the status and message as given: its JAX-RS
// name, INTERNAL_ERROR for 500 and for a status Go has no text for,
// UNSUPPORTED for 501, and for a status JAX-RS has no name for, the letters
// of Go's text alone (#1248).
func TestUnitDefaultCodes(t *testing.T) {
	codes := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "BAD_REQUEST"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
		{http.StatusConflict, "CONFLICT"},
		{http.StatusRequestEntityTooLarge, "REQUEST_ENTITY_TOO_LARGE"},
		{http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE"},
		{http.StatusTeapot, "IM_A_TEAPOT"},
		{http.StatusTooManyRequests, "TOO_MANY_REQUESTS"},
		{http.StatusInternalServerError, "INTERNAL_ERROR"},
		{http.StatusNotImplemented, "UNSUPPORTED"},
		{http.StatusBadGateway, "BAD_GATEWAY"},
		{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{http.StatusGatewayTimeout, "GATEWAY_TIMEOUT"},
		{http.StatusHTTPVersionNotSupported, "HTTP_VERSION_NOT_SUPPORTED"},
		{599, "INTERNAL_ERROR"},
	}
	profiles := []struct {
		name        string
		profile     Profile
		contentType string
		body        string
	}{
		{
			"access",
			ProfileAccess,
			"application/json",
			`{"errors":[{"code":"%s","message":"refused"}]}`,
		},
		{"grid", ProfileGrid, "application/json", `{"code":"%s","message":"refused"}`},
		{
			"pub",
			ProfilePub,
			"application/vnd.pub.v2+json",
			`{"error":{"code":"%s","message":"refused"}}`,
		},
	}
	for _, p := range profiles {
		for _, c := range codes {
			t.Run(fmt.Sprintf("%s %d", p.name, c.status), func(t *testing.T) {
				rec := httptest.NewRecorder()
				Write(rec, p.profile, New(c.status, "refused"))
				want := fmt.Sprintf(p.body, c.code) + "\n"
				if rec.Code != c.status || rec.Header().Get("Content-Type") != p.contentType ||
					rec.Body.String() != want {
					t.Fatalf("%d %q %s, want %d %q %s",
						rec.Code, rec.Header().Get("Content-Type"), rec.Body,
						c.status, p.contentType, want)
				}
			})
		}
	}
	if New(404, "m").Error() != "m" {
		t.Fatal("Error() should return the message")
	}
}

// TestUnitOCICodes: in the OCI envelope, an error built with New for a 401,
// 403 or 429 carries the distribution specification's code, which Docker's
// client reads, in place of its JAX-RS name, and so does one with no code. A
// code a registry handler chose is kept, and a status the specification
// names no single code for keeps its name. Rendering never changes the
// error, so the Access envelope still reads FORBIDDEN after it (#1248).
func TestUnitOCICodes(t *testing.T) {
	for _, c := range []struct {
		err  *Error
		code string
	}{
		{New(http.StatusUnauthorized, "refused"), "UNAUTHORIZED"},
		{New(http.StatusForbidden, "refused"), "DENIED"},
		{New(http.StatusTooManyRequests, "refused"), "TOOMANYREQUESTS"},
		{&Error{Status: http.StatusForbidden, Message: "refused"}, "DENIED"},
		{&Error{Status: http.StatusTooManyRequests, Message: "refused"}, "TOOMANYREQUESTS"},
		{&Error{Status: http.StatusForbidden, Code: "UNAUTHORIZED", Message: "refused"}, "UNAUTHORIZED"},
		{&Error{Status: http.StatusNotFound, Code: "NAME_UNKNOWN", Message: "refused"}, "NAME_UNKNOWN"},
		{New(http.StatusBadRequest, "refused"), "BAD_REQUEST"},
		{New(http.StatusInternalServerError, "refused"), "INTERNAL_ERROR"},
		{New(http.StatusBadGateway, "refused"), "BAD_GATEWAY"},
		{New(http.StatusServiceUnavailable, "refused"), "SERVICE_UNAVAILABLE"},
	} {
		t.Run(fmt.Sprintf("%d %s", c.err.Status, c.code), func(t *testing.T) {
			before := *c.err
			rec := httptest.NewRecorder()
			Write(rec, ProfileOCI, c.err)
			want := `{"errors":[{"code":"` + c.code + `","message":"refused"}]}` + "\n"
			if rec.Code != c.err.Status || rec.Body.String() != want {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, c.err.Status, want)
			}
			if *c.err != before {
				t.Fatalf("rendering changed the error: %+v, was %+v", *c.err, before)
			}
		})
	}
	shared := New(http.StatusForbidden, "refused")
	Write(httptest.NewRecorder(), ProfileOCI, shared)
	rec := httptest.NewRecorder()
	Write(rec, ProfileAccess, shared)
	if got := rec.Body.String(); got != `{"errors":[{"code":"FORBIDDEN","message":"refused"}]}`+"\n" {
		t.Fatalf("access envelope after OCI: %s", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnitUnauthorizedChallenges(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, ProfileArtifactory, New(http.StatusUnauthorized, "Bad credentials"))
	if got := rec.Header().Get("WWW-Authenticate"); got != BasicChallenge {
		t.Fatalf("challenge %q", got)
	}
	// A handler that challenges for another scheme keeps its own.
	rec = httptest.NewRecorder()
	rec.Header().Set("WWW-Authenticate", `Bearer realm="https://reg/v2/token"`)
	Write(rec, ProfileOCI, New(http.StatusUnauthorized, "token required"))
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="https://reg/v2/token"` {
		t.Fatalf("challenge replaced: %q", got)
	}
	rec = httptest.NewRecorder()
	Write(rec, ProfileArtifactory, New(http.StatusForbidden, "Forbidden"))
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("a 403 challenged: %q", got)
	}
	// A 403 that asks for a challenge gets the profile's, as 7.x's entry
	// point answers a refused API key; an upstream's refusal gets none.
	for _, c := range []struct {
		err  *Error
		want string
	}{
		{&Error{Status: http.StatusForbidden, Message: "Bad credentials", Challenge: true}, BasicChallenge},
		{&Error{Status: http.StatusUnauthorized, Message: "up", NoChallenge: true}, ""},
		{&Error{Status: http.StatusForbidden, Message: "up", NoChallenge: true, Challenge: true}, ""},
	} {
		rec = httptest.NewRecorder()
		Write(rec, ProfileArtifactory, c.err)
		if got := rec.Header().Get("WWW-Authenticate"); got != c.want || rec.Code != c.err.Status {
			t.Errorf("%+v: %d %q, want %q", c.err, rec.Code, got, c.want)
		}
	}
}

// TestUnitPubChallenge: under ProfilePub a 401 and a 403 carry the pub
// repository specification's challenge, Bearer realm="pub" with the
// error's message, which dart pub prints (#1279). The message is a
// quoted-string of printable ASCII: quotes and backslashes are escaped, a
// newline becomes a space and any other character a question mark. A 404,
// an upstream's refusal and a challenge a handler set first are left alone.
func TestUnitPubChallenge(t *testing.T) {
	for _, c := range []struct {
		err    *Error
		preset string
		want   string
	}{
		{
			err:  New(http.StatusUnauthorized, "Authentication is required"),
			want: `Bearer realm="pub", message="Authentication is required"`,
		},
		{
			err:  New(http.StatusForbidden, "Forbidden"),
			want: `Bearer realm="pub", message="Forbidden"`,
		},
		{
			err:  &Error{Status: http.StatusForbidden, Message: "Bad credentials", Challenge: true},
			want: `Bearer realm="pub", message="Bad credentials"`,
		},
		{
			err:  New(http.StatusForbidden, "User is Locked.\nContact System Administrator to Unlock The Account."),
			want: `Bearer realm="pub", message="User is Locked. Contact System Administrator to Unlock The Account."`,
		},
		{
			err:  New(http.StatusUnauthorized, `say "hi" \ café`),
			want: `Bearer realm="pub", message="say \"hi\" \\ caf?"`,
		},
		{err: New(http.StatusNotFound, "absent")},
		{err: &Error{Status: http.StatusForbidden, Message: "upstream", NoChallenge: true}},
		{err: &Error{Status: http.StatusUnauthorized, Message: "upstream", NoChallenge: true}},
		{
			err:    New(http.StatusUnauthorized, "token required"),
			preset: `Bearer realm="other"`,
			want:   `Bearer realm="other"`,
		},
	} {
		rec := httptest.NewRecorder()
		if c.preset != "" {
			rec.Header().Set("WWW-Authenticate", c.preset)
		}
		Write(rec, ProfilePub, c.err)
		if got := rec.Header().Values("WWW-Authenticate"); rec.Code != c.err.Status ||
			strings.Join(got, "|") != c.want {
			t.Errorf("%d %q: %d %q, want %q", c.err.Status, c.err.Message, rec.Code, got, c.want)
		}
	}
}

func TestUnitTerraformBackendEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(
		rec,
		ProfileTerraformBackend,
		New(http.StatusConflict, "Workspace app is locked by User alice"),
	)
	if rec.Code != http.StatusConflict ||
		rec.Header().Get("Content-Type") != "application/vnd.api+json" ||
		rec.Body.String() != `{"errors":[{"detail":"Workspace app is locked by User alice","status":"409","title":"Conflict"}]}`+"\n" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
