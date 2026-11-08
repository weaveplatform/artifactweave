package remote

import "context"

type EagerKey struct{}

// The stored credential revision also fences the interval between restoring a
// job and the source backend acquiring its own transport options.
func mavenCompanionRevision(ctx context.Context, revision string) bool {
	expected, ok := ctx.Value(EagerKey{}).(string)
	return !ok || expected == revision
}
