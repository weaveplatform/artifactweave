package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/server/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/server/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/server/gen/grid"
	pfedgen "github.com/weaveplatform/artifactweave/internal/server/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/server/gen/worker"
	"github.com/weaveplatform/artifactweave/pkg/store"
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
	// signWarn throttles the log line for a failing download-redirect
	// signer; nil, in services built only to reach the store, logs each.
	signWarn *throttle
	// folderSlots holds a token for each folder archive download running;
	// nil, in services built only to reach the store, limits nothing.
	folderSlots chan struct{}
	// instanceID holds the instance id the database last gave the health
	// probes; nil, in services built only to reach the store, keeps none.
	instanceID *atomic.Pointer[string]
}

type accessService struct {
	accessgen.UnimplementedStrict
	cfg Config
	// idTokens verifies the subject tokens of the OIDC token exchange and
	// caches each provider's signing keys for the life of the handler.
	idTokens *idtoken.Verifier
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

// GetSystemLicense answers administrators only, as documented
// (getlicenseinfo.json).
func (artifactoryService) GetSystemLicense(
	ctx context.Context,
	_ artgen.GetSystemLicenseRequestObject,
) (artgen.GetSystemLicenseResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	return artgen.GetSystemLicense200JSONResponse{
		Type:         "artifactweave",
		ValidThrough: "never",
		LicensedTo:   "artifactweave",
	}, nil
}

// GetServiceId answers administrators only, as documented
// (getserviceid-1.md), with the stored instance id. The token service
// derives its jfac@ issuer from the same record, so the jfrt@<id>:admin
// scope jf rt atc --grant-admin builds from this body names this instance.
func (s artifactoryService) GetServiceId(
	ctx context.Context,
	_ artgen.GetServiceIdRequestObject,
) (artgen.GetServiceIdResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	id, err := s.cfg.Store.Instance().Get(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "instance identity", err)
	}
	// The body is the bare id, without a trailing newline: jf rt atc
	// appends ":admin" to it as read, so a newline would split the scope.
	return artgen.GetServiceId200TextResponse(id.InstanceID), nil
}

// GetSystemReadiness answers OK while the database and the binary store
// answer, and 500 with the reason otherwise (getreadinessprobe.md:
// "Returns HTTP error code with reason if not working properly").
func (s artifactoryService) GetSystemReadiness(
	ctx context.Context,
	_ artgen.GetSystemReadinessRequestObject,
) (artgen.GetSystemReadinessResponseObject, error) {
	if err := s.probeArtifactory(ctx); err != nil {
		return nil, s.probeFailed(ctx, err)
	}
	return artgen.GetSystemReadiness200JSONResponse{Code: "OK"}, nil
}

// GetSystemLiveness answers OK while the process serves requests. It
// probes neither store: an orchestrator restarts a container whose
// liveness fails, and a restart brings back no database or bucket.
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

// probeFunc probes the stores one service needs.
type probeFunc func(artifactoryService, context.Context) *probeError

// ping answers the plain text OK while probe passes, and 500 with the
// reason as plain text otherwise, as the 6.20 PingResource answers a failed
// storage or Access check. Artifactory's ping probes the database and the
// binary store, Access's the database alone.
func (s artifactoryService) ping(probe probeFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := probe(s, r.Context()); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, s.probeFailed(r.Context(), err).Message)
			return
		}
		_, _ = io.WriteString(w, "OK")
	}
}

// readiness answers the router's and Access's readiness probes as
// Artifactory's readiness answers: {"code":"OK"} while probe passes, and 500
// with the reason in the path's error envelope otherwise.
func (s artifactoryService) readiness(probe probeFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := probe(s, r.Context()); err != nil {
			apierror.Write(w, surfaceFor(r.URL.Path), s.probeFailed(r.Context(), err))
			return
		}
		liveness(w, r)
	}
}

// liveness answers the router's and Access's liveness probes. They probe no
// store, for the reason GetSystemLiveness gives.
func liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"code":"OK"}`+"\n")
}

// sinkBodyLimit bounds the telemetry sinks' bodies. The sinks answer
// without credentials, so without a bound anyone could hold a connection
// open for as long as they kept sending (#431). The reports the JFrog CLI
// and the Terraform providers post are well under a kilobyte; 1 MiB leaves
// ample room for a larger one without letting the read run on.
const sinkBodyLimit = 1 << 20

// sinkHandler drains and discards telemetry posted by the JFrog CLI and the
// Terraform providers (usage reports, jfconnect metrics). It runs behind
// bodyLimit(sinkBodyLimit). Reading a body within the limit to EOF lets the
// connection be reused; a body without a declared length that runs past the
// limit stops the read there and answers 413, as bodyLimit answers a
// declared one, and the server then closes the connection rather than
// drain the rest.
func sinkHandler(w http.ResponseWriter, r *http.Request) {
	_, err := io.Copy(io.Discard, r.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		apierror.Write(w, surfaceFor(r.URL.Path), bodyTooLarge(tooLarge.Limit))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Router health states. A service whose probe fails is UNHEALTHY, and the
// router, which works itself, is UNHEALTHY_PEER while any of its services
// is not healthy.
const (
	stateHealthy       = "HEALTHY"
	stateUnhealthy     = "UNHEALTHY"
	stateUnhealthyPeer = "UNHEALTHY_PEER"
	healthyMessage     = "OK"
)

// healthEntry is the router's or one service's entry in the router health
// document; the router's has no service_id.
type healthEntry struct {
	ServiceID string `json:"service_id,omitempty"`
	NodeID    string `json:"node_id"`
	State     string `json:"state"`
	Message   string `json:"message"`
}

// routerHealth answers the JFrog router's aggregate health document: 200
// while both services are healthy, 503 otherwise. One process is the
// router and both services, so all three share a node. Access (jfac) needs
// the database; Artifactory (jfrt) needs the database and the binary store.
func (s artifactoryService) routerHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	node := nodeID()
	router := healthEntry{NodeID: node, State: stateHealthy, Message: healthyMessage}
	rt, ac := router, router
	id, err := s.probeDatabase(ctx)
	if err != nil {
		ac.State, ac.Message = stateUnhealthy, s.probeFailed(ctx, err).Message
		rt.State, rt.Message = ac.State, ac.Message
	} else if err := s.probeBinaries(ctx); err != nil {
		rt.State, rt.Message = stateUnhealthy, s.probeFailed(ctx, err).Message
	}
	if id == "" {
		id = s.lastInstanceID()
	}
	// The token issuer derives jfac@ from the same id (decision 0005), so
	// both services carry the instance's UUID.
	uuid := strings.TrimPrefix(id, "jfrt@")
	rt.ServiceID, ac.ServiceID = "jfrt@"+uuid, "jfac@"+uuid
	status := http.StatusOK
	if rt.State != stateHealthy || ac.State != stateHealthy {
		router.State, status = stateUnhealthyPeer, http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Router   healthEntry   `json:"router"`
		Services []healthEntry `json:"services"`
	}{router, []healthEntry{rt, ac}})
}

// nodeID names this process's node in the router health document: its
// host name, the id a node gets when none is configured.
var nodeID = sync.OnceValue(func() string {
	host, _ := os.Hostname()
	return host
})

// probeTimeout bounds one probe of a store. Orchestrators count a probe
// failed after a second or so; without a bound a database that stopped
// answering would leave a blocked request behind every probe.
const probeTimeout = 5 * time.Second

// probeDigest is the SHA-256 of empty content. The binary store probe asks
// whether the store holds it only to make the store answer; either answer
// shows the store is reachable with the configured credentials.
const probeDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// The reasons a failed probe gives. Each names the part that failed and
// nothing more: the probes answer without credentials, and a store's own
// error can carry a database host and user, a file path or a bucket name.
// The cause itself goes to the access log (probeFailed).
const (
	reasonDatabase    = "The database is not available"
	reasonBinaryStore = "The binary store is not available"
)

// probeError is a failed probe: the reason a caller is told and the cause
// only the log sees.
type probeError struct {
	reason string
	cause  error
}

func (e *probeError) Error() string { return e.reason + ": " + e.cause.Error() }
func (e *probeError) Unwrap() error { return e.cause }

// probeFailed leaves err's cause for the access log and returns the 500 a
// caller gets, which carries the reason alone.
func (artifactoryService) probeFailed(ctx context.Context, err *probeError) *apierror.Error {
	noteCause(ctx, err)
	return apierror.New(http.StatusInternalServerError, err.reason)
}

// probeArtifactory probes what Artifactory needs: the database, then the
// binary store.
func (s artifactoryService) probeArtifactory(ctx context.Context) *probeError {
	if _, err := s.probeDatabase(ctx); err != nil {
		return err
	}
	return s.probeBinaries(ctx)
}

// probeAccess probes what Access needs: the database.
func (s artifactoryService) probeAccess(ctx context.Context) *probeError {
	_, err := s.probeDatabase(ctx)
	return err
}

// probeDatabase reads the instance identity, which every token and peer
// call relies on, and returns its id. A store without one has still
// answered. A handler built without a store, as only tests build one, has
// nothing to probe.
func (s artifactoryService) probeDatabase(ctx context.Context) (string, *probeError) {
	if s.cfg.Store == nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	id, err := s.cfg.Store.Instance().Get(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", nil
	case err != nil:
		return "", &probeError{reason: reasonDatabase, cause: err}
	}
	if s.instanceID != nil {
		s.instanceID.Store(&id.InstanceID)
	}
	return id.InstanceID, nil
}

// probeBinaries asks the hot binary store for one blob.
func (s artifactoryService) probeBinaries(ctx context.Context) *probeError {
	if s.cfg.Blobs == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := s.cfg.Blobs.Exists(ctx, probeDigest); err != nil {
		return &probeError{reason: reasonBinaryStore, cause: err}
	}
	return nil
}

// lastInstanceID is the id the database last gave a probe, or "" before it
// has given one. The id never changes once created, so router health
// names the services by it while the database is down.
func (s artifactoryService) lastInstanceID() string {
	if s.instanceID == nil {
		return ""
	}
	if id := s.instanceID.Load(); id != nil {
		return *id
	}
	return ""
}
