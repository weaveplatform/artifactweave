package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
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

// blobPrefix starts a blob's file name, sha256__<hex>, in every folder of an
// image, digest folders included: jf looks blobs up by that name (research
// 23, 5.1). It is not the digest folder's prefix in a local repository (see
// DigestFolder).
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
// depth puts the file exactly two levels below name. kernel.ErrNotFound
// when there is none.
func FindBlob(
	ctx context.Context,
	items port.ItemStore,
	repoKey, name, hex string,
) (model.Item, error) {
	q := blobCopies(repoKey, name, hex)
	q.Limit, q.HasLimit = 1, true
	hits, err := items.Search(ctx, &q, aql.Options{})
	if err != nil {
		return model.Item{}, fmt.Errorf("docker: find blob %s in %s: %w", hex, name, err)
	}
	if len(hits) == 0 {
		return model.Item{}, fmt.Errorf("docker: blob %s in %s: %w", hex, name, kernel.ErrNotFound)
	}
	return hits[0].Item, nil
}

// FindBlobs returns every file FindBlob could return for the blob hex: a
// layer that several tags of the image share has a copy in each tag folder,
// and a pull may serve any copy the repository's patterns accept.
func FindBlobs(
	ctx context.Context,
	items port.ItemStore,
	repoKey, name, hex string,
) ([]model.Item, error) {
	q := blobCopies(repoKey, name, hex)
	hits, err := items.Search(ctx, &q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("docker: find copies of blob %s in %s: %w", hex, name, err)
	}
	out := make([]model.Item, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Item)
	}
	return out, nil
}

// blobCopies is the search for the files holding the blob hex in the image
// name's own folders.
func blobCopies(repoKey, name, hex string) aql.Query {
	return aql.Query{Criteria: aql.And{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: repoKey}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		&aql.FieldCriterion{Field: aql.FieldSHA256, Op: aql.OpEq, Text: hex},
		&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: BlobFile(hex)},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: name + "/*"},
		&aql.FieldCriterion{
			Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(strings.Count(name, "/") + 3),
		},
	}}
}
