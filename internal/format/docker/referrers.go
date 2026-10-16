package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The properties a manifest with a subject carries, from which the referrers
// API is answered: no referrers.json is written (DV-036).
const (
	// SubjectProperty is the subject's digest.
	SubjectProperty = "oci.subject"
	// ArtifactTypeProperty is the type the referrers API reports and filters
	// the manifest by.
	ArtifactTypeProperty = "oci.artifactType"
)

// ReferrerType is the artifactType the referrers API reports for the
// manifest: its own artifactType, or for an image manifest without one its
// config's media type, as the specification says. An index without one has
// none.
func (m Manifest) ReferrerType() string {
	if m.ArtifactType != "" || m.Config == nil {
		return m.ArtifactType
	}
	return m.Config.MediaType
}

// Children are the digests of an index's manifests, once each, in the
// index's order.
func (m Manifest) Children() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range m.Manifests {
		if !seen[d.Digest] {
			seen[d.Digest] = true
			out = append(out, d.Digest)
		}
	}
	return out
}

// ImageManifests returns every stored manifest, image or index, in the
// image name's own folders: its tag folders and its digest folders, each a
// direct child of name, as FindManifest searches them. A manifest of an
// image whose name begins with this one sits deeper and is left out. Each
// hit carries its properties.
func ImageManifests(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name string,
) ([]aql.Hit, error) {
	hits, err := searchManifests(ctx, items, repoKey, name)
	if err != nil {
		return nil, fmt.Errorf("docker: list the manifests of %s: %w", name, err)
	}
	return hits, nil
}

// FindReferrers returns the stored manifests in the image name's own
// folders whose subject is the digest subject, those of artifactType alone
// when it is set, each with its properties. A manifest stored in several
// folders, by digest and by tag, is a hit in each.
func FindReferrers(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name, subject, artifactType string,
) ([]aql.Hit, error) {
	by := []aql.Node{&aql.PropertyCriterion{
		Part: aql.PartPair, Key: SubjectProperty, Op: aql.OpEq, Value: subject,
	}}
	if artifactType != "" {
		by = append(by, &aql.PropertyCriterion{
			Part: aql.PartPair, Key: ArtifactTypeProperty, Op: aql.OpEq, Value: artifactType,
		})
	}
	hits, err := searchManifests(ctx, items, repoKey, name, by...)
	if err != nil {
		return nil, fmt.Errorf("docker: find the referrers of %s in %s: %w", subject, name, err)
	}
	return hits, nil
}

// searchManifests runs the search ImageManifests and FindReferrers share:
// manifest files in name's own folders that also meet more, with their
// properties.
func searchManifests(
	ctx context.Context,
	items store.ItemStore,
	repoKey, name string,
	more ...aql.Node,
) ([]aql.Hit, error) {
	criteria := make(aql.And, 0, 5+len(more))
	criteria = append(criteria,
		aql.RepoSet{Refs: []aql.RepoRef{{Key: repoKey}}},
		&aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"},
		aql.Or{
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ManifestFile},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpEq, Text: ListManifestFile},
		},
		&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: name + "/*"},
		&aql.FieldCriterion{
			Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(strings.Count(name, "/") + 3),
		},
	)
	criteria = append(criteria, more...)
	q := aql.Query{Criteria: criteria, Include: []string{"property.*"}}
	return items.Search(ctx, &q, aql.Options{}) //nolint:wrapcheck // the callers name the search
}
