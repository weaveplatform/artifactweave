package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	accessgen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/access"
	artgen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/artifactory"
	distributiongen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/distribution"
	eventgen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/event"
	gridgen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/grid"
	pfedgen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/pfed"
	workergen "github.com/weaveplatform/weaveplatform-artifact-registry/internal/server/gen/worker"
)

// versionMediaType is the vendor type Artifactory advertises for the version
// document; clients that send it in Accept get it back.
const versionMediaType = "application/vnd.org.jfrog.artifactory.system.Version+json"

// artifactoryService and accessService implement the generated strict
// interfaces. Each embeds its generated UnimplementedStrict so an operation
// that exists in the spec but has no implementation yet answers 501 instead
// of breaking the build; services override what they implement. The
// compatibility version drives client behaviour (decision 0005), so it is
// configuration, not the build version.
type artifactoryService struct {
	artgen.UnimplementedStrict
	cfg Config
}

type accessService struct {
	accessgen.UnimplementedStrict
	cfg Config
}

// eventService serves /event (webhook subscriptions). Nothing is implemented
// until the webhooks phase; the embedded stub answers 501 for every operation.
type eventService struct {
	eventgen.UnimplementedStrict
}

// The services brought into scope in P1b (research 16) start with every
// operation stubbed; their phases replace the stubs.
type (
	distributionService struct {
		distributiongen.UnimplementedStrict
	}
	workerService struct{ workergen.UnimplementedStrict }
	pfedService   struct{ pfedgen.UnimplementedStrict }
	gridService   struct{ gridgen.UnimplementedStrict }
)

var (
	_ artgen.StrictServerInterface    = artifactoryService{}
	_ accessgen.StrictServerInterface = accessService{}
)

func (s artifactoryService) GetSystemVersion(
	_ context.Context,
	req artgen.GetSystemVersionRequestObject,
) (artgen.GetSystemVersionResponseObject, error) {
	license := "artifactweave"
	body := artgen.SystemVersion{
		Version:  s.cfg.CompatVersion,
		Revision: s.cfg.Revision,
		Addons:   []string{},
		License:  &license,
	}
	if req.Params.Accept != nil && strings.Contains(*req.Params.Accept, versionMediaType) {
		return artgen.GetSystemVersion200ApplicationVndOrgJfrogArtifactorySystemVersionPlusJSONResponse(
			body,
		), nil
	}
	return artgen.GetSystemVersion200JSONResponse(body), nil
}

func (artifactoryService) GetSystemLicense(
	context.Context,
	artgen.GetSystemLicenseRequestObject,
) (artgen.GetSystemLicenseResponseObject, error) {
	return artgen.GetSystemLicense200JSONResponse{
		Type:         "artifactweave",
		ValidThrough: "never",
		LicensedTo:   "artifactweave",
	}, nil
}

func (artifactoryService) GetSystemReadiness(
	context.Context,
	artgen.GetSystemReadinessRequestObject,
) (artgen.GetSystemReadinessResponseObject, error) {
	return artgen.GetSystemReadiness200JSONResponse{Code: "OK"}, nil
}

func (artifactoryService) GetSystemLiveness(
	context.Context,
	artgen.GetSystemLivenessRequestObject,
) (artgen.GetSystemLivenessResponseObject, error) {
	return artgen.GetSystemLiveness200JSONResponse{Code: "OK"}, nil
}

func (s accessService) GetAccessVersion(
	context.Context,
	accessgen.GetAccessVersionRequestObject,
) (accessgen.GetAccessVersionResponseObject, error) {
	return accessgen.GetAccessVersion200JSONResponse{
		Name:    s.cfg.CompatVersion,
		Version: s.cfg.CompatVersion,
	}, nil
}

// Hand-written system routes. They are plain text or body sinks that a strict
// JSON server cannot express well, and the spec marks them x-handwritten.

func pingHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "OK")
}

// sinkHandler drains and discards telemetry posted by the JFrog CLI and the
// Terraform providers (usage reports, jfconnect metrics). Reading the body to
// EOF lets the connection be reused.
func sinkHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.WriteHeader(http.StatusOK)
}

// routerHealth mimics the JFrog router's aggregate health document. There is
// one process, so both logical services report the same state.
func routerHealth(w http.ResponseWriter, _ *http.Request) {
	type svc struct {
		ServiceID string `json:"service_id"`
		State     string `json:"state"`
	}
	doc := struct {
		Router   map[string]string `json:"router"`
		Services []svc             `json:"services"`
	}{
		Router: map[string]string{"state": "HEALTHY"},
		Services: []svc{
			{ServiceID: "jfrt@artifactweave", State: "HEALTHY"},
			{ServiceID: "jfac@artifactweave", State: "HEALTHY"},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
