package app

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	artifactshttp "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/platform/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/platform/gen/grid"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	pfedgen "github.com/weaveplatform/artifactweave/internal/platform/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/platform/gen/worker"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

// keyedOperations is what the generated servers' keyed operations share
// (decision 0043, "Replay for keyed operations"): one replay over the
// router's store, and each spec's bindings by operation ID. Which operations
// are keyed is the generated operation table's to say, never this file's:
// an operation is wrapped when its spec declares the shared IdempotencyKey
// parameter, and a binding test fails until it has a binding here.
type keyedOperations struct {
	replay   idempotency.Replay
	bindings map[string]map[string]idempotency.Binding
}

// newKeyedOperations collects the bindings of every module that serves a
// keyed operation. With no store, the replay has none, and a keyed request
// answers as its binding's resource check does, or 501.
func newKeyedOperations(cfg RouterConfig, art artifactshttp.ArtifactoryAPI) keyedOperations {
	k := keyedOperations{bindings: map[string]map[string]idempotency.Binding{
		"artifactory": art.KeyedBindings(),
	}}
	if cfg.Store != nil {
		k.replay = idempotency.Replay{Store: cfg.Store.Replays(), Tx: cfg.Store}
	}
	return k
}

// strictKeyed returns the strict middleware of one spec's generated server,
// H and M being that package's StrictHandlerFunc and StrictMiddlewareFunc.
// Every server gets it, keyed operations or not, so that an operation whose
// spec comes to declare the key is wrapped without a router change. A
// service name the table does not use would wrap nothing while the gate
// still accepted the key, so it panics, which every router test reports.
func strictKeyed[
	H ~func(context.Context, http.ResponseWriter, *http.Request, any) (any, error),
	M ~func(H, string) H,
](k keyedOperations, service string) []M {
	if !slices.ContainsFunc(operations.Table, func(op operations.Operation) bool {
		return op.Service == service
	}) {
		panic(fmt.Sprintf("app: no operation in the table belongs to service %q", service))
	}
	return []M{idempotency.Strict[H](k.replay, service, operations.Table, k.bindings[service])}
}

// The strict middleware of each generated server, by the service name its
// rows carry in the operation table.

func (k keyedOperations) artifactory() []artgen.StrictMiddlewareFunc {
	return strictKeyed[artgen.StrictHandlerFunc, artgen.StrictMiddlewareFunc](k, "artifactory")
}

func (k keyedOperations) access() []accessgen.StrictMiddlewareFunc {
	return strictKeyed[accessgen.StrictHandlerFunc, accessgen.StrictMiddlewareFunc](k, "access")
}

func (k keyedOperations) event() []eventgen.StrictMiddlewareFunc {
	return strictKeyed[eventgen.StrictHandlerFunc, eventgen.StrictMiddlewareFunc](k, "event")
}

func (k keyedOperations) distribution() []distributiongen.StrictMiddlewareFunc {
	return strictKeyed[distributiongen.StrictHandlerFunc, distributiongen.StrictMiddlewareFunc](
		k, "distribution",
	)
}

func (k keyedOperations) worker() []workergen.StrictMiddlewareFunc {
	return strictKeyed[workergen.StrictHandlerFunc, workergen.StrictMiddlewareFunc](k, "worker")
}

func (k keyedOperations) pfed() []pfedgen.StrictMiddlewareFunc {
	return strictKeyed[pfedgen.StrictHandlerFunc, pfedgen.StrictMiddlewareFunc](k, "pfed")
}

func (k keyedOperations) grid() []gridgen.StrictMiddlewareFunc {
	return strictKeyed[gridgen.StrictHandlerFunc, gridgen.StrictMiddlewareFunc](k, "grid")
}

// keyGate is the Idempotency-Key gate over the generated operation table.
// The table always indexes: TestUnitIndexFindsEveryOperation builds the same
// index, so a failure here is a generator fault that every router test
// reports at once.
func keyGate() func(http.Handler) http.Handler {
	gate, err := idempotency.Gate(operations.Table)
	if err != nil {
		panic(err)
	}
	return gate
}
