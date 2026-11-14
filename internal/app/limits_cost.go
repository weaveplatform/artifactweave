package app

import (
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// The x-cost classes of decision 0043, "Cost classes and how admission finds
// them". A table row holding any other value, such as a draft's
// TODO(author), is unclassified, and admission reads its requests as if no
// spec operation matched them.
const (
	costOrdinary  = "ordinary"
	costExpensive = "expensive"
	costExempt    = "exempt"
	costRecovery  = "recovery"
)

// costOperations is the index admission finds a request's spec operation
// in, built once per process over the generated table. A hand-written row
// takes the pattern the router serves its operation under: for the item
// operations, httpkit.ChiPatterns' repository key that never matches "api".
// The spec's plain {repoKey} would match every unknown two-segment API path
// instead, such as PUT /artifactory/api/foo, which the router answers 404
// and the path rules admit as expensive, not as an ordinary deploy. The
// wildcard pattern ChiPatterns adds for a trailing path parameter is left
// out: a deeper path keeps the path rules it was admitted under before,
// and TestAccAdmissionCostAgreesWithServedRoutes holds those to the spec.
var costOperations = sync.OnceValue(func() *operations.Index {
	rows := slices.Clone(operations.Table)
	for i, op := range rows {
		if op.Handwritten {
			rows[i].Pattern = httpkit.ChiPatterns(op.Pattern)[0]
		}
	}
	x, err := operations.NewIndex(rows)
	if err != nil {
		// make generate writes the table and the admission tests build this
		// index from it, so a pattern chi refuses fails them first.
		panic(fmt.Sprintf("server: operation table: %v", err))
	}
	return x
})

// cost is the x-cost class admission charges r under. A request a spec
// operation addresses takes the class the operation declares, made
// expensive by the presence of a query parameter its
// x-cost-expensive-query names. Any other request, which a hand-written
// alias, a native protocol route or no route at all answers (404 and 405
// included), keeps the class the path rules gave it before the spec
// declared classes: o.Health's probes, o.Recovery's recovery requests and
// expensiveRequest.
func (o limitOptions) cost(r *http.Request) string {
	if op, ok := o.Operations.FindRequest(r); ok {
		switch op.Cost {
		case costOrdinary:
			query := r.URL.Query()
			for _, name := range op.ExpensiveQuery {
				if query.Has(name) {
					return costExpensive
				}
			}
			return costOrdinary
		case costExpensive, costExempt, costRecovery:
			return op.Cost
		}
	}
	switch {
	case o.Health(r):
		return costExempt
	case o.Recovery(r):
		return costRecovery
	case expensiveRequest(r):
		return costExpensive
	}
	return costOrdinary
}
