package docker

import (
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// StorageResource translates a native storage file to the normal registry read
// and cache identity. Tag copies of a blob share its _uploads representation;
// an index's list.manifest.json shares the native manifest request identity.
// Unknown files have no registry operation and must not become arbitrary GETs.
func StorageResource(full string) (source.Resource, bool) {
	parts := strings.Split(full, "/")
	if len(parts) < 3 {
		return source.Resource{}, false
	}
	image, folder, file := strings.Join(parts[:len(parts)-2], "/"), parts[len(parts)-2], parts[len(parts)-1]
	if folder != UploadsFolder && !ValidTag(folder) && !IsDigestFolder(folder) {
		return source.Resource{}, false
	}
	operation, reference := "blobs", ""
	if file == ManifestFile || file == ListManifestFile {
		if folder == UploadsFolder {
			return source.Resource{}, false
		}
		operation, reference = "manifests", folder
	} else if digest, ok := strings.CutPrefix(file, blobPrefix); ok {
		reference = "sha256:" + digest
	} else {
		return source.Resource{}, false
	}
	resource, err := RegistryResource(image, operation, reference)
	return resource, err == nil
}

// StoragePath is the native cache path used for canonical selection and stable
// deduplication. Unknown addressed files retain their path and later report an
// explicit unsupported result rather than silently disappearing from a run.
func StoragePath(full string) string {
	if resource, ok := StorageResource(full); ok {
		return resource.Path
	}
	return full
}

// StorageRoot locates the bounded storage subtree containing every equivalent
// upstream spelling of an addressed native cache file. The reader still filters
// to the original canonical selection before pagination and disclosure.
func StorageRoot(root string) string {
	if resource, ok := StorageResource(root); ok {
		image, _, _, _ := ParseRegistryPath(resource.UpstreamPath)
		return image
	}
	if image, ok := strings.CutSuffix(root, "/"+UploadsFolder); ok {
		if _, err := RegistryResource(image, "tags", "list"); err == nil {
			return image
		}
	}
	return root
}
