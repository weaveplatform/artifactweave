package docker

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Compare orders tags and image names as the specification orders tags,
// case-insensitively, with names that differ only in case ordered by
// their bytes, so that the order is total and a page continued after last
// neither repeats nor skips a name (decision 0031).
func Compare(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

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
	items store.ItemStore,
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
	items store.ItemStore,
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

// Image is an image in a repository.
type Image struct {
	Repo string
	Name string
}

// Images returns the images of the repositories keys, each a folder
// holding at least one manifest in a tag or digest folder of its own
// (decision 0031), ordered by repository and then as tags are ordered.
// Whether a name is valid is the caller's to judge.
func Images(ctx context.Context, items store.ItemStore, keys []string) ([]Image, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	refs := make([]aql.RepoRef, 0, len(keys))
	for _, k := range keys {
		refs = append(refs, aql.RepoRef{Key: k})
	}
	hits, err := items.Search(ctx, manifestFiles(aql.RepoSet{Refs: refs}), aql.Options{})
	if err != nil {
		return nil, fmt.Errorf("docker: images: %w", err)
	}
	seen := map[Image]bool{}
	var out []Image
	for _, h := range hits {
		i := strings.LastIndex(h.Item.Path, "/")
		if i < 0 {
			// A manifest in a top-level folder has no image above it.
			continue
		}
		im := Image{Repo: h.Item.RepoKey, Name: h.Item.Path[:i]}
		if _, isFolder := manifestFolder(h.Item.Path[i+1:]); isFolder && !seen[im] {
			seen[im] = true
			out = append(out, im)
		}
	}
	slices.SortFunc(out, func(a, b Image) int {
		if c := strings.Compare(a.Repo, b.Repo); c != 0 {
			return c
		}
		return Compare(a.Name, b.Name)
	})
	return out, nil
}
