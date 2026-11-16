package cocoapods

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// Pods is one CDN index of pods, all_pods.txt or one
// all_pods_versions_<x>_<y>_<z>.txt shard: each pod it lists, with the
// versions it lists of that pod. all_pods.txt lists no versions.
type Pods map[string][]string

// Index merges the CDN indexes of a virtual repository's members into the
// virtual repository's: every pod any member lists, with every version any
// member lists of it. "All local and remote repositories included in the
// virtual repository must also be CDN-compatible" (JFrog's CocoaPods
// Repositories page), so each member's part is its own CDN index.
type Index struct{}

// Merge returns the union of the parts' pods and of each pod's versions.
func (Index) Merge(_ context.Context, parts []virtual.Part[Pods]) (Pods, error) {
	out := Pods{}
	for _, p := range parts {
		for name, versions := range p.Doc {
			out[name] = append(out[name], versions...)
		}
	}
	for name, versions := range out {
		slices.Sort(versions)
		out[name] = slices.Compact(versions)
	}
	return out, nil
}

// Lines renders p as the rows of its CDN file, sorted: each pod's name for
// all_pods.txt, and <name>/<version>/... for a shard, whose pods list at
// least one version. Versions sort as strings, as a hosted repository's
// shard sorts them.
func (p Pods) Lines(shard bool) []string {
	rows := make([]string, 0, len(p))
	for _, name := range slices.Sorted(maps.Keys(p)) {
		switch {
		case !shard:
			rows = append(rows, name)
		case len(p[name]) > 0:
			versions := slices.Clone(p[name])
			slices.Sort(versions)
			rows = append(rows, name+"/"+strings.Join(versions, "/"))
		}
	}
	return rows
}
