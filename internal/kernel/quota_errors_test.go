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

func TestUnitDiskQuotaAnswersAs7x(t *testing.T) {
	rec := httptest.NewRecorder()
	apierror.Write(rec, apierror.ProfileArtifactory, fmt.Errorf("private volume context: %w", kernel.ErrDiskQuota))
	want := `{"errors":[{"message":"Datastore disk usage is too high. Contact your Artifactory administrator to add ` +
		`additional storage space or change the disk quota limits.","status":413}]}`
	if rec.Code != http.StatusRequestEntityTooLarge || strings.TrimSpace(rec.Body.String()) != want || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("disk quota refusal", rec.Code, rec.Body.String(), rec.Header())
	}
	rec = httptest.NewRecorder()
	apierror.Write(rec, apierror.ProfileArtifactory, fmt.Errorf("statfs failed: %w", kernel.ErrStorageCapacity))
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "statfs") {
		t.Fatal("unmeasurable capacity", rec.Code, rec.Body.String())
	}
	for _, err := range []error{kernel.ErrStorageQuota, kernel.ErrDiskQuota, fmt.Errorf("wrapped: %w", kernel.ErrStorageCapacity)} {
		if !kernel.StorageRefusal(err) {
			t.Error("not a storage refusal", err)
		}
	}
	if kernel.StorageRefusal(kernel.ErrStore) || kernel.StorageRefusal(nil) {
		t.Error("an unrelated failure counted as a storage refusal")
	}
}
