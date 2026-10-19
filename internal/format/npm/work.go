package npm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The kinds of work the npm format runs from the persistent work queue
// (decisions 0033 and 0034), each keyed by repository key. Both are JFrog
// operations that 7.x schedules rather than runs before it answers.
const (
	// KindReindex rebuilds a repository's index from its tarballs and
	// their properties: "Recalculating index for npm repository <repo>
	// scheduled to run" (JFrog's archived REST reference, KB 000005641).
	KindReindex = "npm.reindex"
	// KindConvert converts a repository to strict layout, moving each
	// tarball off its canonical path to it, as 7.x's conversion does in
	// the background (npm-repositories, "Enforce strict layout").
	KindConvert = "npm.convert"
)

// WorkRequest is the payload of the format's work: who asked for it.
type WorkRequest struct {
	// By is recorded as the mover of what a conversion moves.
	By string `json:"by,omitempty"`
}

// ErrUnconverted reports a conversion that left tarballs off their
// canonical paths.
var ErrUnconverted = errors.New("npm: tarballs left off their canonical paths")

// WorkKinds are the kinds the npm format runs.
func (Handler) WorkKinds() []string { return []string{KindReindex, KindConvert} }

// Owed is nothing: a read waits for no reindex or conversion, as 7.x's
// reads do not, since the deploys keep the index current.
func (Handler) Owed(repo.Config, bool, string) []format.WorkRef { return nil }

// Work runs a reindex or a conversion of the repository the item names. A
// repository that is gone, or no longer an npm one, owes nothing, and nor
// does an item of a kind the format no longer runs, which an earlier build
// may have left in the queue: running it as a reindex would rebuild a
// repository nobody asked about.
func (h Handler) Work(ctx context.Context, env format.Env, it types.WorkItem) error {
	if it.Kind != KindReindex && it.Kind != KindConvert {
		return nil
	}
	c, err := env.Store.Repos().Get(ctx, it.Key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("npm: repository %s: %w", it.Key, err)
	case !Serves(c.PackageType):
		return nil
	case it.Kind == KindConvert:
		var req WorkRequest
		// An empty or foreign payload names no mover, which a move
		// records as no one.
		_ = json.Unmarshal(it.Payload, &req)
		return convert(ctx, env, c, req.By)
	}
	return h.reindex(ctx, env, c)
}

// maxReported bounds the tarballs a failed conversion names in its error,
// which the work queue keeps and logs.
const maxReported = 10

// convert moves each tarball of c that the index does not hold, but whose
// package.json names a version, to its canonical path, where Deployed
// indexes it, so running it again changes nothing. A tarball without a
// readable package.json stays where it is, as strict layout stores it. A
// canonical path that already holds the same content needs nothing; one
// that holds anything else, or a move the repository refuses, such as one
// its release and snapshot handling forbids, leaves the tarball where it
// is and is reported: the conversion fails naming each, so the work queue
// retries it and, once its retries run out, parks it for resume to queue
// again when the paths are free (decision 0034). [I] What 7.x does with a
// canonical path already taken is unknown (#216).
func convert(ctx context.Context, env format.Env, c repo.Config, by string) error {
	hits, err := env.Store.Items().Search(ctx, tarballQuery(c, false), aql.Options{})
	if err != nil {
		return fmt.Errorf("npm: tarballs of %s: %w", c.Key, err)
	}
	var problems []string
	for _, h := range hits {
		problem, err := convertOne(ctx, env, c, h.Item, by)
		if err != nil {
			return err
		}
		if problem != "" {
			problems = append(problems, problem)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	more := ""
	if n := len(problems) - maxReported; n > 0 {
		problems, more = problems[:maxReported], fmt.Sprintf("; and %d more", n)
	}
	return fmt.Errorf("%w in %s: %s%s", ErrUnconverted, c.Key, strings.Join(problems, "; "), more)
}

// convertOne moves it to its canonical path when it belongs there, saying
// why it could not.
func convertOne(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	it types.Item,
	by string,
) (string, error) {
	if _, indexed, err := versionOf(ctx, env.Store, it.ID); err != nil || indexed {
		return "", err
	}
	t, ok, err := ReadStored(ctx, env.Blobs, it.Actual.SHA256)
	if err != nil || !ok {
		return "", err
	}
	from, to := it.FullPath(), CanonicalPath(t.Name, t.Version)
	there, err := env.Store.Items().Get(ctx, c.Key, false, to)
	switch {
	case err == nil && !there.Folder && there.Actual.SHA256 == it.Actual.SHA256:
		return "", nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("npm: get %s: %w", to, err)
	}
	problem, err := env.Move(ctx, c, from, to, by)
	if err != nil || problem == "" {
		return "", err //nolint:wrapcheck // the item service names its own
	}
	return from + ": " + problem, nil
}
