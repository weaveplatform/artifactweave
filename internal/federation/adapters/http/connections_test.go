package federationhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type connections struct{ err error }

func (c connections) Connections(context.Context, string) ([]federated.Connection, error) {
	return []federated.Connection{{Mode: "Receiving_Only", Status: "HEALTHY"}}, c.err
}

func TestUnitConnectionStatesAdapter(t *testing.T) {
	req := artgen.GetFederationConnectionDetailsRequestObject{RepoKey: "fed"}
	if _, err := (federationhttp.FederatedAPI{}).GetFederationConnectionDetails(t.Context(), req); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Fatal(err)
	}
	a := federationhttp.FederatedAPI{Connections: connections{}}
	response, err := a.GetFederationConnectionDetails(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := response.VisitGetFederationConnectionDetailsResponse(w); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"Receiving_Only"`) || !strings.Contains(w.Body.String(), `"status":"HEALTHY"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	a.Connections = connections{err: federated.ErrNoConnectionBaseURL}
	response, err = a.GetFederationConnectionDetails(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	if err := response.VisitGetFederationConnectionDetailsResponse(w); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != `{"memberConnections":[]}` {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, failure := range []error{kernel.ErrForbidden, kernel.ErrStore} {
		a.Connections = connections{err: failure}
		if _, err := a.GetFederationConnectionDetails(t.Context(), req); err == nil {
			t.Fatal("service error lost")
		}
	}
}
