package maven

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// versionMeta is a version's metadata in the package index (decision
// 0036): what its POM says, for the plugin-level metadata (#279).
type versionMeta struct {
	// POM is the SHA-256 of the POM it was read from: while that stays the
	// version's POM, a write need not read it again.
	POM       string `json:"pom"`
	Packaging string `json:"packaging,omitempty"`
	Name      string `json:"name,omitempty"`
}

// metadataOf is the metadata of a version whose files are its, prev being
// what the index holds for it. It is read from the version's POM (versionPOM):
// from what Inspect read when the write being indexed stored that POM, from
// prev while the POM is the one prev was read from, and otherwise from the
// blob store. A version without a POM has none.
func metadataOf(
	ctx context.Context,
	env format.Env,
	prev []byte,
	its []types.Item,
) ([]byte, error) {
	it, ok := versionPOM(its)
	if !ok {
		return nil, nil
	}
	sha := it.Actual.SHA256
	var was versionMeta
	if json.Unmarshal(prev, &was) == nil && was.POM == sha {
		return prev, nil
	}
	p, ok := env.Inspection.(*pom)
	if !ok || p.SHA256 != sha {
		var err error
		if p, err = readPOM(ctx, env.Blobs, sha); err != nil {
			return nil, err
		}
	}
	m := versionMeta{POM: sha}
	if p.Err == nil {
		m.Packaging, m.Name = p.Packaging, p.Name
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("maven: metadata: %w", err)
	}
	return b, nil
}

// versionPOM is the POM a version's metadata comes from: its only one, or,
// among a snapshot's unique builds, the highest build's.
func versionPOM(its []types.Item) (types.Item, bool) {
	var best types.Item
	bestBuild, found := 0, false
	for _, it := range its {
		p := it.FullPath()
		if !IsPom(p) {
			continue
		}
		build := -1
		if b, ok := ParseUniqueBuild(path.Base(p)); ok {
			build = b.Number
		}
		if !found || build > bestBuild || build == bestBuild && p > best.FullPath() {
			best, bestBuild, found = it, build, true
		}
	}
	return best, found
}
