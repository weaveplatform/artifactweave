package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

const (
	// OriginatedHeader carries the nodes a repository request has traversed.
	OriginatedHeader = "X-Artifactory-Originated"
	// OriginHeader is the legacy origin marker sent alongside OriginatedHeader.
	OriginHeader = "Origin-Artifactory"
)

// 7.x accepts a JVM VMID as its host id. An opaque process identity has the
// same lifecycle and distinguishes nodes sharing a database without exposing
// host names. Its representation is an inference for P19 (#1542).
var originHost = func() string {
	sum := sha256.Sum256([]byte(rand.Text()))
	return hex.EncodeToString(sum[:12])
}()

type (
	originKey   struct{}
	originTrace struct {
		values  []string
		present bool
	}
)

// OriginHostID returns this process's opaque repository-request identity.
func OriginHostID() string { return originHost }

// OriginTrace records origin markers as loop evidence, never as authority.
// Both markers identify an Artifactory request (7.161.26,
// HttpArtifactoryRequest.isFromAnotherArtifactory).
func OriginTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, originated := r.Header[http.CanonicalHeaderKey(OriginatedHeader)]
		_, legacy := r.Header[http.CanonicalHeaderKey(OriginHeader)]
		trace := originTrace{present: originated || legacy}
		for _, line := range r.Header.Values(OriginatedHeader) {
			for value := range strings.SplitSeq(line, ",") {
				trace.values = append(trace.values, strings.TrimSpace(value))
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), originKey{}, trace)))
	})
}

// NoOriginUpstream is R7's restriction for the requested virtual repository.
// A nested virtual's own flag does not change its parent's request.
func NoOriginUpstream(ctx context.Context, v repo.Config) bool {
	trace, _ := ctx.Value(originKey{}).(originTrace)
	return trace.present && (v.ArtifactoryRequestsCanRetrieveRemoteArtifacts == nil || !*v.ArtifactoryRequestsCanRetrieveRemoteArtifacts)
}

// RecursiveOrigin reports a download returning to this node after another
// hop (7.161.26 HttpArtifactoryRequest.isRecursive).
func RecursiveOrigin(ctx context.Context) bool {
	trace, _ := ctx.Value(originKey{}).(originTrace)
	return len(trace.values) > 1 && slices.Contains(trace.values, originHost)
}

// SetOriginHeaders installs reserved headers after credential and outbound
// hooks (7.161.26 HttpRepo.addDefaultHeadersAndQueryParams). Smart Remote's
// trusted hook adds its own trace; configured extra headers cannot add hops.
func SetOriginHeaders(req *http.Request) {
	trace, _ := req.Context().Value(originKey{}).(originTrace)
	values := slices.Clone(trace.values)
	if SmartActive(req.Context()) {
		values = append(values, req.Header.Values(OriginatedHeader)...)
	}
	req.Header.Del(OriginatedHeader)
	seen := map[string]bool{}
	for _, value := range values {
		if !seen[value] {
			req.Header.Add(OriginatedHeader, value)
			seen[value] = true
		}
	}
	req.Header.Add(OriginatedHeader, originHost)
	req.Header.Set(OriginHeader, originHost)
}

// A returning hop must not wait on its own original fill: its next outbound
// hop carries the second host marker that the download guard rejects. The
// persisted cache key is unchanged; only in-flight work is separated.
func originFlightKey(ctx context.Context, key string) string {
	trace, _ := ctx.Value(originKey{}).(originTrace)
	if !trace.present {
		return key
	}
	sum := sha256.Sum256([]byte(strings.Join(trace.values, "\x00")))
	return key + "/origin/" + hex.EncodeToString(sum[:])
}

func returningOrigin(ctx context.Context) bool {
	trace, _ := ctx.Value(originKey{}).(originTrace)
	return slices.Contains(trace.values, originHost)
}
