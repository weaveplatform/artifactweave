package golang

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Merger unions GOPROXY version lines in the effective member order. A
// duplicate keeps the first member's complete line, including its timestamp.
// The ascending order is the local Go list's order [I], pending #216/P19.
type Merger struct{}

// Merge returns the first copy of each version, in Go version order.
func (Merger) Merge(ctx context.Context, parts []virtual.Part[[]string]) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("merge Go versions: %w", err)
	}
	lists := make([][]string, 0, len(parts))
	for _, p := range parts {
		lists = append(lists, p.Doc)
	}
	out := virtual.UnionBy(lists, LineVersion)
	virtual.SortVersions(out, LineVersion, compare)
	return out, nil
}

// LineVersion is the version in a GOPROXY list line, with an optional time.
func LineVersion(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
