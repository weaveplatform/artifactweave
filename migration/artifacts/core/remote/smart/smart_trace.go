package smart

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
)

// SmartOriginHeader carries the Smart Remote nodes a request has passed
// through, as Artifactory's own Smart Remote sends it. SmartTrace reads up to
// 8 nodes from it, and Smart's peer requests send it with this node added,
// so that a chain of Smart remotes that loops back is refused.
const SmartOriginHeader = "X-Artifactory-Originated"

type smartTraceKey struct{}

// SmartTrace carries only loop-prevention evidence. It grants no authority and
// is ignored by ordinary requests. A client can use it only to deny its own
// Smart request, never to authenticate or widen a repository scope.
func SmartTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var trace []string
		for _, raw := range r.Header.Values(SmartOriginHeader) {
			for node := range strings.SplitSeq(raw, ",") {
				trace = append(trace, node)
				if len(trace) >= 8 {
					break
				}
			}
			if len(trace) >= 8 {
				break
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), smartTraceKey{}, trace)))
	})
}

func smartEnter(ctx context.Context, identity string) (context.Context, error) {
	trace, _ := ctx.Value(smartTraceKey{}).([]string)
	if len(trace) >= 8 || slices.Contains(trace, identity) {
		return ctx, source.ErrUnavailable
	}
	for _, node := range trace {
		if node == "" || len(node) > 256 ||
			strings.ContainsFunc(node, func(r rune) bool { return r <= ' ' || r >= 127 }) {
			return ctx, source.ErrInvalidRequest
		}
	}
	trace = append(slices.Clone(trace), identity)
	return context.WithValue(ctx, smartTraceKey{}, trace), nil
}

func smartHeaders(req *http.Request) {
	trace, _ := req.Context().Value(smartTraceKey{}).([]string)
	if !remote.SmartActive(req.Context()) {
		return
	}
	req.Header.Del(SmartOriginHeader)
	for _, node := range trace {
		req.Header.Add(SmartOriginHeader, node)
	}
}
