package idempotency

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	// appendOnlyRefusal is the key gate's message while build append is
	// the only operation that accepts a key, the text it has given since
	// the key was introduced.
	appendOnlyRefusal = "Idempotency-Key is supported only once, on build append"
	// ruleRefusal names the rule instead, once any other operation accepts
	// a key; it changes only answers to requests that send one (decision
	// 0043, "A key where none is accepted").
	ruleRefusal = "Idempotency-Key is supported only once, on an operation whose API description declares it"
)

// Gate returns middleware that answers 400 to a request carrying
// Idempotency-Key unless it carries exactly one and table's operation for
// its method and path declares the shared IdempotencyKey parameter. The
// router runs it ahead of authentication and admission, so the 400 uses the
// path's error envelope, is charged to no allowance and carries no rate
// header, and no handler that would ignore the key can run with it. A hand-
// written alias, a native protocol route and an unknown path match no row,
// and a 501 stub declares no parameter, so each answers 400 too. It fails
// as operations.NewIndex does.
func Gate(table []operations.Operation) (func(http.Handler) http.Handler, error) {
	index, err := operations.NewIndex(table)
	if err != nil {
		return nil, fmt.Errorf("idempotency: key gate: %w", err)
	}
	refusal := refusalFor(table)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if values, present := r.Header[Header]; present {
				op, found := index.FindRequest(r)
				// chi matches an empty segment as an empty parameter, which
				// names no resource; the gate has always refused it.
				accepted := found && op.IdempotencyKey && len(values) == 1 &&
					!strings.Contains(r.URL.EscapedPath(), "//")
				if !accepted {
					apierror.Write(w, httpkit.ErrorProfile(r), httpkit.BadRequestf("%s", refusal))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// refusalFor keeps build append's wording while it is the only operation
// that accepts a key, and names the rule as soon as that is no longer so,
// without anyone having to remember to change it.
func refusalFor(table []operations.Operation) string {
	var keyed []string
	for _, op := range table {
		if op.IdempotencyKey {
			keyed = append(keyed, op.OperationID)
		}
	}
	if len(keyed) == 1 && keyed[0] == "appendBuild" {
		return appendOnlyRefusal
	}
	return ruleRefusal
}
