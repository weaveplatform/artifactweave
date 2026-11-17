package pypihttp

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitPyPIVirtualWarehouseFallbackOnlyOnMissingAPI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{"missing", kernel.ErrNotFound, true},
		{"upstream missing", &source.HTTPError{Status: http.StatusNotFound}, true},
		{"upstream forbidden", &source.HTTPError{Status: http.StatusForbidden}, false},
		{"policy", &format.RejectionError{Status: http.StatusNotFound, Message: "blocked", Policy: true}, false},
		{"cache fill", errors.Join(new(source.CacheFillDenied), kernel.ErrNotFound), false},
		{"permission", source.ErrDenied, false},
		{"offline", source.ErrUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := warehouseMissing(tc.err); got != tc.fallback {
				t.Fatal(got)
			}
		})
	}
}

func TestUnitPyPIVirtualMemberRefusalClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		skip bool
	}{
		{"success", nil, false},
		{"remote401", &source.HTTPError{Status: 401}, true},
		{"remote403", &source.HTTPError{Status: 403}, true},
		{"remote409", &source.HTTPError{Status: 409}, false},
		{"mapped upstream", &apierror.Error{Status: 403, NoChallenge: true}, true},
		{"local forbidden", &apierror.Error{Status: 403}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if pypiPassedOver(tc.err) != tc.skip {
				t.Fatal(tc)
			}
		})
	}
}
