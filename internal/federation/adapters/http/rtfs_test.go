package federationhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

type rtfsAuthority struct {
	federationhttp.FederatedOperations
	err error
}

func (a rtfsAuthority) Administrator(context.Context) error { return a.err }

type (
	rtfsStubs  struct{ artgen.UnimplementedStrict }
	rtfsServer struct {
		federationhttp.FederatedAPI
		rtfsStubs
	}
)

// The paths are the RTFS-only rows of research 40 section 7. Exercise the
// generated router so missing operations and accidental 501s fail here.
func TestUnitRTFSResourcesKeepMigrationAndInternalBoundaries(t *testing.T) {
	cases := []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "status", 404, "Not Found"},
		{"GET", "validate/fed", 404, "Not Found"},
		{"GET", "repos", 404, "Not Found"},
		{"GET", "baseUrlAndRepos", 404, "Not Found"},
		{"POST", "properties/fed", 404, "Not Found"},
		{"GET", "property/fed/file.json", 404, "Not Found"},
		{"GET", "extendedInfo/fed", 404, "Not Found"},
		{"GET", "ready/fed", 404, "Not Found"},
		{"GET", "repoExists/fed", 404, "Not Found"},
		{"POST", "metadata", 404, "Not Found"},
		{"PUT", "metadata", 404, "Not Found"},
		{"POST", "metadata/invalidate", 404, "Not Found"},
		{"PUT", "negotiation/disable", 404, "Not Found"},
		{"POST", "repos/enable", 404, "Not Found"},
		{"POST", "dryfullsync/repo/file.json", 404, "Not Found"},
		{"POST", "fullsync/repo/file.json", 404, "Not Found"},
		{"GET", "fullsync/repo/file.json", 404, "Not Found"},
		{"DELETE", "fullsync/repo/file.json", 404, "Not Found"},
		{"POST", "fullsync/systemRepo", 404, "Not Found"},
		{"GET", "federatedRepoConfigInfo", 404, "Not Found"},
		{"GET", "federatedConfigInfo/fed", 404, "Not Found"},
		{"POST", "disableRemoteMember", 404, "Not Found"},
		{"POST", "member/enable", 404, "Not Found"},
		{"GET", "lastIdInSystem", 404, "Not Found"},
		{"POST", "task/submit", 404, "Not Found"},
		{"GET", "binary/provider/v2/check", 404, "Not Found"},
		{"POST", "queue/remaining", 404, "Not Found"},
		{"PUT", "queue/stop", 404, "Not Found"},
		{"PUT", "queue/deregister", 404, "Not Found"},
		{"POST", "queue/reset", 404, "Not Found"},
		{"GET", "queue/state", 404, "Not Found"},
		{"PUT", "queue/state/ACTIVE", 404, "Not Found"},
		{"GET", "tenant/move/status", 404, "Not Found"},
		{"PUT", "snapshotFederatedRepos", 404, "Not Found"},
		{"GET", "migration/status/verbose", 404, "Not Found"},
		{"PUT", "binaries/rtfs", 404, "Not Found"},
		{"GET", "binaries/rtfs", 404, "Not Found"},
		{"POST", "grid/topology/events", 404, "Not Found"},
		{"PUT", "rtfs", 400, "Access denied: This endpoint is restricted to migration tool calls only."},
		{"PUT", "migration", 400, "Access denied: This endpoint is restricted to migration tool calls only."},
		{"PUT", "migration/status/verbose", 400, "Access denied: This endpoint is restricted to migration tool calls only."},
	}
	for _, authority := range []error{nil, kernel.ErrForbidden, kernel.ErrUnauthenticated} {
		api := federationhttp.FederatedAPI{Operations: rtfsAuthority{err: authority}}
		router := chi.NewRouter()
		artgen.HandlerWithOptions(artgen.NewStrictHandlerWithOptions(rtfsServer{FederatedAPI: api}, nil, artgen.StrictHTTPServerOptions{
			ResponseErrorHandlerFunc: httpkit.WriteErr,
		}), artgen.ChiServerOptions{BaseURL: "/artifactory", BaseRouter: router})
		for _, tc := range cases {
			t.Run(tc.method+" "+tc.path, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, "/artifactory/api/federation/"+tc.path, strings.NewReader("not JSON"))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.body) {
					t.Fatalf("authority %v: got %d %s, want %d containing %q", authority, rec.Code, rec.Body.String(), tc.status, tc.body)
				}
			})
		}
	}
}

func TestUnitRTFSStateRequiresCurrentAdministrator(t *testing.T) {
	for _, failure := range []error{nil, kernel.ErrForbidden, kernel.ErrUnauthenticated, errors.New("private backend cause")} {
		api := federationhttp.FederatedAPI{Operations: rtfsAuthority{err: failure}}
		connection, err := api.GetFederationServiceConnection(t.Context(), artgen.GetFederationServiceConnectionRequestObject{})
		migration, other := api.GetFederationMigrationStatus(t.Context(), artgen.GetFederationMigrationStatusRequestObject{})
		switch {
		case failure == nil:
			if connection != artgen.GetFederationServiceConnection200TextResponse("true") ||
				migration != artgen.GetFederationMigrationStatus200TextResponse("MIGRATION_COMPLETED") ||
				err != nil ||
				other != nil {
				t.Fatalf("state: %v %v, migration: %v %v", connection, err, migration, other)
			}
		case errors.Is(failure, kernel.ErrForbidden), errors.Is(failure, kernel.ErrUnauthenticated):
			if !errors.Is(err, failure) || !errors.Is(other, failure) {
				t.Fatalf("authority lost: %v %v", err, other)
			}
		default:
			requireStatus(t, err, http.StatusInternalServerError)
			requireStatus(t, other, http.StatusInternalServerError)
			if strings.Contains(err.Error(), "private backend cause") {
				t.Fatal("private cause disclosed")
			}
		}
	}
	api := federationhttp.FederatedAPI{}
	_, err := api.GetFederationServiceConnection(t.Context(), artgen.GetFederationServiceConnectionRequestObject{})
	requireStatus(t, err, http.StatusNotImplemented)
	_, err = api.GetFederationMigrationStatus(t.Context(), artgen.GetFederationMigrationStatusRequestObject{})
	requireStatus(t, err, http.StatusNotImplemented)
}
