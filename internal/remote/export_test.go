package remote

import (
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// These names open the policy's unexported parts to the external tests
// (package remote_test). A test that needs the format registry or a format
// slice belongs there: the slices import this package, so an in-package
// test that imports them is an import cycle.

// Kind is the retrieval class the policy gives q.
func (p *Policy) Kind(q source.Request) types.RemoteKind { return p.kind(q) }

// Fresh reports whether the cache entry e still answers q without revalidation.
func (p *Policy) Fresh(q source.Request, e types.RemoteCacheEntry) bool { return p.fresh(q, e) }

// FlightKey is the key of the policy's shared fill for a cache key and source.
func (p *Policy) FlightKey(key types.RemoteCacheKey, identity types.SourceIdentity) string {
	return p.flightKey(key, identity)
}

// ProbeKey is the key of the policy's shared recovery probe.
func (p *Policy) ProbeKey(
	key types.RemoteCacheKey,
	permit source.Permit,
	state types.RemoteSourceState,
) string {
	return p.probeKey(key, permit, state)
}

// FlightWaiters and ProbeWaiters count the callers that share the fill or
// the probe under key, 0 when none is active.
func (p *Policy) FlightWaiters(key string) int { return waiters(&p.flights, key) }

func (p *Policy) ProbeWaiters(key string) int { return waiters(&p.probes, key) }

func waiters[T any](g *flightGroup[T], key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.active[key]; f != nil {
		return f.waiters
	}
	return 0
}

// SetPages and SetEntries set what a listing budget has already spent, so a
// test can start a listing at its limits.
func (b *ListingBudget) SetPages(n int) *ListingBudget { b.pages = n; return b }

func (b *ListingBudget) SetEntries(n int) *ListingBudget { b.entries = n; return b }

var (
	ResponseBase          = responseBase
	MetadataResponse      = metadataResponse
	ProjectModuleLocation = projectModuleLocation
)

// The Smart registry helpers, for smart_registry_test.go: that test passes
// the Docker slice's parser, and an in-package test importing the slice is an
// import cycle.
var (
	SmartRegistryGrant = smartRegistryGrant
	SmartPropertyPath  = smartPropertyPath
	SmartStoragePath   = smartStoragePath
)
