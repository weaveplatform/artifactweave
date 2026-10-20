package format

import "github.com/weaveplatform/artifactweave/pkg/repo"

// SidecarKeeper is a format some of whose files own sidecars: files at
// paths named after theirs that are of use only beside them, such as a Helm
// chart's provenance file, which helm fetches at the chart's URL plus
// ".prov" (decision 0039). The item service keeps a sidecar with its owner:
//   - a delete of the owner deletes it in the same operation, into the
//     trash can when that is on;
//   - a copy or move of the owner takes it to the sidecar's path beside the
//     owner's target, under the same rules as the owner, and the owner does
//     not go without it;
//   - a trash restore of the owner restores it when the same delete removed
//     it.
//
// On its own a sidecar is an ordinary file: deleting, copying or moving it
// touches nothing else, and a deploy over its owner leaves it as it is. It
// is never a version file, so the format's index links no sidecar, and it
// is no checksum file either: that is SidecarOwner.
type SidecarKeeper interface {
	// Sidecars returns the paths in c of the sidecars the file at path
	// owns, whether or not any exists, and none for a file that owns none.
	// The item service asks it of owners only, never of a sidecar it
	// found, so a sidecar of a sidecar is not followed.
	Sidecars(c repo.Config, path string) []string
}
