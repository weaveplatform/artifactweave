// Package docker is the storage layout of Docker and OCI images in a
// repository's item tree (decision 0031), which the JFrog CLI reads with AQL
// and the storage API: an image is a folder, each tag or digest a folder of
// its own below it, and every blob a file named sha256__<hex> in the folders
// of the images that use it. Finished uploads wait in the image's _uploads
// folder until a manifest links them. A format package never imports
// internal/server, nor the item service, which calls the format hooks.
package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Serves reports whether repositories of package type t hold images: docker,
// and the oci and helmoci types that share its protocol and layout
// (research 04).
func Serves(t repo.PackageType) bool {
	return t == repo.Docker || t == repo.OCI || t == repo.HelmOCI
}

// UploadsFolder is the folder below an image that holds its finished
// uploads, as on Artifactory; jf leaves it out of its searches.
const UploadsFolder = "_uploads"

// blobPrefix starts a blob's file name: a digest's colon is not allowed in
// every path the item tree is exported to, so Artifactory writes two
// underscores instead.
const blobPrefix = "sha256__"

// BlobFile is the file name of the blob whose SHA-256 is hex.
func BlobFile(hex string) string { return blobPrefix + hex }

// UploadPath is the path of the finished upload of hex in the image name.
func UploadPath(name, hex string) string {
	return name + "/" + UploadsFolder + "/" + BlobFile(hex)
}

// FindBlob returns a file holding the blob hex in one of the image name's
// own folders: a tag folder, a digest folder or _uploads, each a direct
// child of name (decision 0031). A blob only in a deeper folder belongs to
// another image whose name begins with this one, and is not found: the
// depth puts the file exactly two levels below name. store.ErrNotFound
// when there is none.
func FindBlob(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name, hex string,
) (types.Item, error) {
	q := aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: repoKey}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldSHA256, Op: aql.OpEq, Text: hex},
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: BlobFile(hex)},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: name + "/*"},
		&aql.FieldCriterion{
			Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(strings.Count(name, "/") + 3),
		},
	}, Limit: 1, HasLimit: true}
	hits, err := items.Search(ctx, &q, aql.Options{})
	if err != nil {
		return types.Item{}, fmt.Errorf("docker: find blob %s in %s: %w", hex, name, err)
	}
	if len(hits) == 0 {
		return types.Item{}, fmt.Errorf("docker: blob %s in %s: %w", hex, name, store.ErrNotFound)
	}
	return hits[0].Item, nil
}
