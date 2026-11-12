package apierror

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// TestUnitProfileHeaders: the exact headers Write gives every profile, so
// the Swift registry's Content-Version (#1278) is seen to reach that profile
// and no other. A 404 carries only what a profile always sets; a 401 with a
// Retry-After adds the challenge and Retry-After every profile shares.
func TestUnitProfileHeaders(t *testing.T) {
	const (
		jsonType = "application/json"
		problem  = "application/problem+json; charset=utf-8"
	)
	contentTypes := map[Profile]string{
		ProfileArtifactory:        jsonType,
		ProfileAccess:             jsonType,
		ProfileOCI:                jsonType,
		ProfileXray:               jsonType,
		ProfileNPM:                jsonType,
		ProfileTerraformRegistry:  jsonType,
		ProfileTerraformBackend:   "application/vnd.api+json",
		ProfileJFrogDistribution:  jsonType,
		ProfileArtifactoryLegacy:  jsonType,
		ProfileImportExport:       jsonType,
		ProfilePlatformFederation: jsonType,
		ProfileGrid:               jsonType,
		ProfileText:               "text/plain; charset=utf-8",
		ProfileAPM:                problem,
		ProfileSwiftRegistry:      problem,
		ProfilePub:                "application/vnd.pub.v2+json",
	}
	// ProfilePub is the last profile, so the table names every one.
	if len(contentTypes) != int(ProfilePub)+1 {
		t.Fatalf("the table names %d profiles, want %d", len(contentTypes), ProfilePub+1)
	}
	for p := ProfileArtifactory; p <= ProfilePub; p++ {
		challenge := BasicChallenge
		if p == ProfilePub {
			challenge = `Bearer realm="pub"`
		}
		notFound := http.Header{"Content-Type": {contentTypes[p]}}
		unauthorized := http.Header{
			"Content-Type":     {contentTypes[p]},
			"Retry-After":      {"30"},
			"Www-Authenticate": {challenge},
		}
		if p == ProfileSwiftRegistry {
			notFound.Set("Content-Version", "1")
			unauthorized.Set("Content-Version", "1")
		}
		for _, c := range []struct {
			err  *Error
			want http.Header
		}{
			{New(http.StatusNotFound, "absent"), notFound},
			{&Error{
				Status:            http.StatusUnauthorized,
				Code:              "UNAUTHORIZED",
				Message:           "Authentication is required",
				RetryAfterSeconds: 30,
			}, unauthorized},
		} {
			rec := httptest.NewRecorder()
			Write(rec, p, c.err)
			if rec.Code != c.err.Status || !maps.EqualFunc(rec.Header(), c.want, slices.Equal) {
				t.Errorf("profile %d, %d: %d %v, want %v",
					p, c.err.Status, rec.Code, rec.Header(), c.want)
			}
		}
	}
}
