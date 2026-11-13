package port

import "context"

// DockerPageStore reads the image layout of a Docker, OCI or Helm OCI
// repository's local content (decision 0031) a page at a time, in the orders
// the registry's tags lists and catalogs answer in, so that a listing reads
// the page it serves rather than every manifest of the image or repository
// (#1402). It sees the stored manifests alone: files named manifest.json or
// list.manifest.json. Which of their folders are tags, and which images a
// client could pull, is the registry's to judge.
//
// Folding is DockerFold. Every tag and every valid image name is ASCII, so
// for them it is the registry's own case folding (strings.ToLower).
type DockerPageStore interface {
	// ManifestFolders returns the folders directly below q.Image that hold a
	// stored manifest, each once, ordered by their folded names and then by
	// their bytes: the order of the registry's tags lists. Only folders after
	// the cursor are listed, at most q.Limit of them (all when it is not
	// positive), so fewer than q.Limit means there are no more.
	ManifestFolders(ctx context.Context, q DockerFolderQuery) ([]string, error)
	// ImageManifests returns the image and folder of each stored manifest in
	// a folder below an image (never one in a top-level folder), each pair
	// once, ordered by the image's bytes and then the folder's: the order of
	// the registry's catalogs, whose images are lower case. Only pairs after
	// the cursor are listed, at most q.Limit (all when it is not positive).
	ImageManifests(ctx context.Context, q DockerImageQuery) ([]DockerImageManifest, error)
}

// DockerFolderQuery selects the manifest folders of one image.
type DockerFolderQuery struct {
	RepoKey string
	Image   string
	// AfterFold and After continue a listing: a folder is listed when its
	// folded name follows AfterFold, or equals it and the name's bytes follow
	// After. The caller folds After itself, since its fold of a name a client
	// sends need not be ASCII; both empty list every folder.
	AfterFold, After string
	Limit            int
}

// DockerImageQuery selects the manifests of one repository's images.
type DockerImageQuery struct {
	RepoKey string
	// After continues a listing at the images whose names' bytes follow it.
	// Within keeps After's own manifests too, in the folders whose names'
	// bytes follow AfterFolder (all of them when it is empty), so a listing
	// can resume inside an image or start at one.
	After       string
	Within      bool
	AfterFolder string
	Limit       int
}

// DockerImageManifest is a stored manifest's image and the folder below the
// image that holds it.
type DockerImageManifest struct {
	Image  string
	Folder string
}

// DockerFold is the folding DockerPageStore orders folder names by: A to Z
// become a to z and every other byte stays, as lower() folds on SQLite and,
// under COLLATE "C", on PostgreSQL. A listing continued after a folder the
// store returned passes this fold of it, never another: a fold that moved a
// name elsewhere in the store's order would repeat or skip folders.
func DockerFold(s string) string {
	b := []byte(s)
	for n, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[n] = c + 'a' - 'A'
		}
	}
	return string(b)
}
