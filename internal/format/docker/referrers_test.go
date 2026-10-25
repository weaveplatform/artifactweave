package docker_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A manifest's own artifactType and annotations are kept, and the type the
// referrers API reports is its own, or an image's config type.
func TestUnitReferrerFields(t *testing.T) {
	const sig = "application/vnd.example.sig"
	body := strings.TrimSuffix(image(digest("c"), ""), "}") +
		`,"artifactType":"` + sig + `","annotations":{"a":"1"}}`
	m, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(body), "h")
	if err != nil || m.ArtifactType != sig || m.Annotations["a"] != "1" || m.ReferrerType() != sig {
		t.Fatalf("%+v %v", m, err)
	}
	m.ArtifactType = ""
	if m.ReferrerType() != "application/vnd.oci.image.config.v1+json" {
		t.Fatal(m.ReferrerType())
	}
	if (layout.Manifest{MediaType: layout.MediaTypeOCIIndex}).ReferrerType() != "" {
		t.Fatal("an index without an artifactType has a type")
	}
}

// An index's children are its manifests' digests, once each, in order.
func TestUnitChildren(t *testing.T) {
	index := `{"schemaVersion":2,"manifests":[` +
		layer(layout.MediaTypeOCIManifest, digest("b")) + `,` +
		layer(layout.MediaTypeOCIManifest, digest("a")) + `,` +
		layer(layout.MediaTypeOCIManifest, digest("b")) + `]}`
	m, err := layout.ParseManifest(layout.MediaTypeOCIIndex, []byte(index), "h")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{digest("b"), digest("a")}, m.Children()); diff != "" {
		t.Fatal(diff)
	}
	if (layout.Manifest{}).Children() != nil {
		t.Fatal("an image manifest has children")
	}
}

// A manifest with a subject carries oci.subject, and oci.artifactType
// when it has a type to report.
func TestUnitSubjectProperties(t *testing.T) {
	subject := &layout.Descriptor{Digest: digest("s")}
	got := layout.Properties(layout.Manifest{
		MediaType: layout.MediaTypeOCIManifest, Digest: "abc", Subject: subject,
		ArtifactType: "application/vnd.example.sig",
	}, "app", "sig", layout.Config{})
	if !got.Has("oci.subject", digest("s")) ||
		!got.Has("oci.artifactType", "application/vnd.example.sig") {
		t.Fatal(got)
	}
	got = layout.Properties(layout.Manifest{
		MediaType: layout.MediaTypeOCIIndex, Digest: "abc", Subject: subject,
	}, "app", "list", layout.Config{})
	if !got.Has("oci.subject", digest("s")) || len(got["oci.artifactType"]) != 0 {
		t.Fatal(got)
	}
}

// FindReferrers and ImageManifests are AQL queries, so they run on the
// reference evaluator and on SQL, as FindManifest does.
func TestAccFindReferrers(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"inmem": func(*testing.T) store.Store { return inmem.New() },
		"sqlite": func(t *testing.T) store.Store {
			st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
				sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	} {
		t.Run(name, func(t *testing.T) { findReferrers(t, open(t)) })
	}
}

func findReferrers(t *testing.T, db store.Store) {
	ctx := context.Background()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	c := *repo.Defaults(&repo.Config{Key: "docker-local", Class: repo.Local, PackageType: repo.Docker})
	if err := db.Repos().Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	deploy := func(path, body string, props types.Properties) {
		t.Helper()
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body),
			Properties: props,
		}); err != nil {
			t.Fatal(err)
		}
	}
	refers := func(subject, artifactType string) types.Properties {
		return types.Properties{"oci.subject": {subject}, "oci.artifactType": {artifactType}}
	}
	deploy("a_b/sig/manifest.json", "sig", refers(digest("s"), "sig"))
	sbom := "a_b/" + layout.DigestFolder(sum("sbom")) + "/manifest.json"
	deploy(sbom, "sbom", refers(digest("s"), "sbom"))
	deploy("a_b/other/manifest.json", "other", refers(digest("o"), "sig"))
	deploy("a_b/list/list.manifest.json", "list", nil)
	deploy("a_b/sub/v1/manifest.json", "deep", refers(digest("s"), "sig"))
	deploy("a_b/v2/sha256__"+sum("layer"), "layer", refers(digest("s"), "sig"))
	names := func(hits []aql.Hit) []string {
		var out []string
		for _, h := range hits {
			out = append(out, h.Item.FullPath())
		}
		slices.Sort(out)
		return out
	}
	hits, err := layout.FindReferrers(ctx, db.Items(), "docker-local", "a_b", digest("s"), "")
	want := []string{sbom, "a_b/sig/manifest.json"}
	if err != nil || !cmp.Equal(want, names(hits)) {
		t.Fatalf("%v %v", names(hits), err)
	}
	for _, h := range hits {
		if len(h.Properties["oci.artifactType"]) != 1 {
			t.Fatalf("%s: %v", h.Item.FullPath(), h.Properties)
		}
	}
	hits, err = layout.FindReferrers(ctx, db.Items(), "docker-local", "a_b", digest("s"), "sbom")
	if err != nil || !cmp.Equal(want[:1], names(hits)) {
		t.Fatalf("%v %v", names(hits), err)
	}
	hits, err = layout.ImageManifests(ctx, db.Items(), "docker-local", "a_b")
	all := []string{
		"a_b/list/list.manifest.json", "a_b/other/manifest.json",
		sbom, "a_b/sig/manifest.json",
	}
	if err != nil || !cmp.Equal(all, names(hits)) {
		t.Fatalf("%v %v", names(hits), err)
	}
	if _, err := layout.FindReferrers(
		ctx,
		failingSearch{},
		"r",
		"app",
		digest("s"),
		"",
	); err == nil ||
		errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := layout.ImageManifests(ctx, failingSearch{}, "r", "app"); err == nil {
		t.Fatal("no error")
	}
}
