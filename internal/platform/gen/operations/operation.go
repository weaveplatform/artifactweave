package operations

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Operation is one operation of the bundled specs and the classes it
// declares (decision 0043, "Rules for every operation"). A class the spec
// does not declare yet is the empty string.
type Operation struct {
	// Service is the spec, api/<Service>.openapi.yaml.
	Service string
	// Method is the HTTP method in upper case.
	Method string
	// Pattern is the chi route pattern the router serves the operation
	// under: the spec's server URL, which router.go mounts each generated
	// server at, and the spec path with oapi-codegen's parameter syntax.
	Pattern string
	// OperationID is the spec's operationId, which the generated strict
	// server passes its middleware.
	OperationID string
	// Handwritten marks an x-handwritten operation: a hand-written route
	// serves it, and the generated server does not register its pattern.
	Handwritten bool
	// Cost is x-cost: ordinary, expensive, exempt or recovery.
	Cost string
	// ExpensiveQuery is x-cost-expensive-query: the query parameters whose
	// presence makes an ordinary operation expensive.
	ExpensiveQuery []string
	// IdempotencyKey reports that the operation declares the shared
	// IdempotencyKey parameter, so the server honours Idempotency-Key on it.
	IdempotencyKey bool
	// Idempotency is x-idempotency: safe, natural, key or none.
	Idempotency string
	// Pagination is x-pagination: cursor, offset, page, native or complete,
	// or empty for an operation that returns no collection.
	Pagination string
}

// Index finds the operation a request addresses with chi's own routing over
// the table's patterns, so the lookup applies the router's precedence (a
// static segment beats a parameter) to the same escaped path.
type Index struct {
	mux *chi.Mux
	ops map[string]Operation
}

// NewIndex builds an Index over table. It fails when chi refuses a pattern,
// or when two rows share a method and pattern, which would make the lookup
// ambiguous.
func NewIndex(table []Operation) (_ *Index, err error) {
	x := &Index{mux: chi.NewMux(), ops: make(map[string]Operation, len(table))}
	defer func() {
		// chi reports a pattern it cannot route by panicking at registration.
		if p := recover(); p != nil {
			err = fmt.Errorf("operations: %v", p)
		}
	}()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, op := range table {
		key := op.Method + " " + op.Pattern
		if _, dup := x.ops[key]; dup {
			return nil, fmt.Errorf("operations: %s is listed twice", key)
		}
		x.ops[key] = op
		x.mux.Method(op.Method, op.Pattern, noop)
	}
	return x, nil
}

// Find returns the operation that method and the escaped path address, and
// false when no spec pattern matches them: a hand-written alias, a native
// protocol route or an unknown path.
func (x *Index) Find(method, path string) (Operation, bool) {
	pattern := x.mux.Find(chi.NewRouteContext(), method, path)
	op, ok := x.ops[method+" "+pattern]
	return op, ok
}

// FindRequest is Find for r, over the path chi routes r on: the raw path
// when the URL keeps one, so an escaped slash stays inside its segment.
func (x *Index) FindRequest(r *http.Request) (Operation, bool) {
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	return x.Find(r.Method, path)
}
