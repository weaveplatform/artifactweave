package remote

import "context"

// EagerKey marks the context of a Maven companion job; its value is the
// credential revision the job was queued under. A policy read or Check in
// that context answers source.ErrSuperseded when the revision it would use
// differs, so a job never fetches with credentials other than those of the
// read that queued it. The Maven slice sets it when a job runs, and reads
// it so that a companion's own read queues no further companion.
type EagerKey struct{}

// The stored credential revision also fences the interval between restoring a
// job and the source backend acquiring its own transport options.
func mavenCompanionRevision(ctx context.Context, revision string) bool {
	expected, ok := ctx.Value(EagerKey{}).(string)
	return !ok || expected == revision
}
