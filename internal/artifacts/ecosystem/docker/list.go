package docker

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// Compare orders tags and image names as the specification orders tags,
// case-insensitively, with names that differ only in case ordered by
// their bytes, so that the order is total and a page continued after last
// neither repeats nor skips a name (decision 0031).
func Compare(a, b string) int {
	if c := strings.Compare(Fold(a), Fold(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// Fold is the case folding Compare orders by first.
func Fold(s string) string { return strings.ToLower(s) }

// digestFolder reports whether folder is the name of a digest folder,
// where a manifest pushed by digest lives. A sha256__<hex> folder is a tag.
func digestFolder(folder string) bool {
	return IsDigestFolder(folder) && hexRE.MatchString(folder[len(DigestFolder("")):])
}

// manifestFolder reports whether folder, a direct child of an image, is
// one a manifest makes part of the image: a tag folder or a digest folder.
// tag is set for a tag folder.
func manifestFolder(folder string) (tag, ok bool) {
	switch {
	case digestFolder(folder):
		return false, true
	case ValidTag(folder) && !ReservedTag(folder):
		return true, true
	default:
		return false, false
	}
}

// manifestFiles matches the stored manifests, manifest.json and
// list.manifest.json files, with more criteria.
func manifestFiles(more ...aql.Node) *aql.Query {
	return &aql.Query{Criteria: append(aql.And{
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		aql.Or{
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ManifestFile},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ListManifestFile},
		},
	}, more...)}
}

// inImage limits a query to the image name's own folders in repoKey: the
// files exactly two levels below name, as FindBlob finds blobs, so that an
// image whose name begins with this one's contributes nothing.
func inImage(repoKey, name string) []aql.Node {
	return []aql.Node{
		aql.RepoSet{Refs: []aql.RepoRef{{Key: repoKey}}},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: name + "/*"},
		&aql.FieldCriterion{
			Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(strings.Count(name, "/") + 3),
		},
	}
}

// Tags returns the tags of the image name in repoKey in the
// specification's order: the folders directly below it that hold a
// manifest (decision 0031). A digest folder holds a manifest but is no
// tag, and a reserved name is never one. found is false when the image
// holds no manifest at all, which the registry answers NAME_UNKNOWN.
func Tags(
	ctx context.Context,
	items port.ItemStore,
	repoKey, name string,
) ([]string, bool, error) {
	hits, err := items.Search(ctx, manifestFiles(inImage(repoKey, name)...), aql.Options{})
	if err != nil {
		return nil, false, fmt.Errorf("docker: tags of %s: %w", name, err)
	}
	found := false
	var tags []string
	for _, h := range hits {
		folder := path.Base(h.Item.Path)
		tag, ok := manifestFolder(folder)
		found = found || ok
		if tag && !slices.Contains(tags, folder) {
			tags = append(tags, folder)
		}
	}
	slices.SortFunc(tags, Compare)
	return tags, found, nil
}

// ManifestFolders returns the folders of the image name in repoKey that
// hold the manifest whose SHA-256 is hex, tag folders and the digest
// folder alike, in the specification's order.
func ManifestFolders(
	ctx context.Context,
	items port.ItemStore,
	repoKey, name, hex string,
) ([]string, error) {
	q := manifestFiles(append(inImage(repoKey, name),
		&aql.FieldCriterion{Field: aql.FieldSHA256, Op: aql.OpEq, Text: hex})...)
	hits, err := items.Search(ctx, q, aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("docker: folders of manifest %s in %s: %w", hex, name, err)
	}
	var folders []string
	for _, h := range hits {
		folder := path.Base(h.Item.Path)
		if _, ok := manifestFolder(folder); ok && !slices.Contains(folders, folder) {
			folders = append(folders, folder)
		}
	}
	slices.SortFunc(folders, Compare)
	return folders, nil
}
