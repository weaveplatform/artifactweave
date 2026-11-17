package pkgroute

import (
	"context"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// ItemKit returns the kit of p over o for a format whose files the item
// routes serve rather than a route of its own, such as Alpine's
// APKINDEX.tar.gz, YUM's repodata and Opkg's feed (itemkit's overlays). It
// claims no route, so its helpers answer only the requests Bind hands them.
// 7.x reads a file through a virtual repository with one walk whatever
// route asks for it (decision 0008, R1 to R3), so such a format merges a
// virtual repository's index from MergeMembers, Collect and Merged, as a
// protocol's handlers do, and keeps it in the same cache (R8).
func ItemKit(o Options, p Protocol) *Kit { return newKit(o, p) }

// Bind returns r as a request of the kit's protocol for the repository c,
// as Mount's routes are handed one once it has resolved the repository. The
// item route has already resolved c and asked its read gate, so Bind checks
// nothing: a kit from ItemKit binds only what an overlay has answered for.
func (k *Kit) Bind(r *http.Request, c repo.Config) *http.Request {
	q := Request{Repo: c, Source: k.p.Sources[c.Class]}
	return r.WithContext(context.WithValue(r.Context(), requestKey{}, q))
}
