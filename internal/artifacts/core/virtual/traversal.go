package virtual

import (
	"context"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// traversal separates edge timing from the two public walks' policies. Walk
// rejects a cycle before reading its key; Members deduplicates only after the
// read. visit decides whether a nested virtual's descendants are traversed.
type traversal struct {
	skip  func(chain []repo.Config, key string) bool
	visit func(chain []repo.Config, member repo.Config) (bool, error)
}

// traverse owns the graph's only recursive member loop. Both policies retain
// configured depth-first reads, missing members and immediate error propagation.
func traverse(ctx context.Context, rs Repos, v repo.Config, t traversal) error {
	chain := []repo.Config{v}
	var walk func(repo.Config) error
	walk = func(c repo.Config) error {
		for _, key := range c.Repositories {
			if t.skip != nil && t.skip(chain, key) {
				continue
			}
			m, ok, err := member(ctx, rs, c, key)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			// The chain is borrowed; clipping prevents a callback's append
			// from overwriting the route used for subsequent visits.
			descend, err := t.visit(slices.Clip(chain), m)
			if err != nil {
				return err
			}
			if m.Class != repo.Virtual || !descend {
				continue
			}
			chain = append(chain, m)
			if err := walk(m); err != nil {
				return err
			}
			chain = chain[:len(chain)-1]
		}
		return nil
	}
	return walk(v)
}
