package kernel_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitStorageQuotaKeepsExistingErrorEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile apierror.Profile
		body    string
	}{
		{"artifactory", apierror.ProfileArtifactory, `{"errors":[{"message":"project storage quota exceeded","status":403}]}`},
		{"access", apierror.ProfileAccess, `{"errors":[{"code":"FORBIDDEN","message":"project storage quota exceeded"}]}`},
		{"oci", apierror.ProfileOCI, `{"errors":[{"code":"DENIED","message":"project storage quota exceeded"}]}`},
		{"pub", apierror.ProfilePub, `{"error":{"code":"FORBIDDEN","message":"project storage quota exceeded"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			apierror.Write(rec, tc.profile, fmt.Errorf("private operation context: %w", kernel.ErrStorageQuota))
			if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != tc.body || rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("quota refusal changed envelope, exposed private wrapping or challenged credentials", rec.Code, rec.Body.String(), rec.Header())
			}
		})
	}
}
