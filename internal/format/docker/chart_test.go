package docker_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// chartConfig is a chart's config as Helm pushes it: its Chart.yaml as
// JSON, the version holding the "+" its tag writes as "_".
const chartConfig = `{"name":"acme","version":"1.2.3+build.4",` +
	`"description":"A chart for the Helm OCI tests","apiVersion":"v2",` +
	`"appVersion":"2.0.1","type":"application"}`

// chartMeta is what the package index records for chartConfig's tag.
const chartMeta = `{"chart":{"name":"acme","version":"1.2.3+build.4","appVersion":"2.0.1"}}`

// helmManifest is a manifest as Helm pushes it, without a mediaType, of a
// config of configType and a chart layer.
func helmManifest(configType, config string) string {
	return `{"schemaVersion":2,"config":{"mediaType":"` + configType + `",` +
		`"digest":"sha256:` + sum(config) + `","size":` + strconv.Itoa(len(config)) + `},` +
		`"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip",` +
		`"digest":"sha256:` + sum("chart") + `","size":5}]}`
}

// charts is a Helm OCI repository whose items go through the item service.
type charts struct {
	t     *testing.T
	db    store.Store
	blobs blob.Store
	items *item.Service
	c     repo.Config
}

func newCharts(t *testing.T) *charts {
	t.Helper()
	db := inmem.New()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{
		Key: "helmoci-local", Class: repo.Local, PackageType: repo.HelmOCI,
	})
	if err := db.Repos().Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return &charts{t: t, db: db, blobs: blobs, c: c, items: &item.Service{
		Store: db, Blobs: blobs,
		Now: func() time.Time { return time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC) },
	}}
}

func (x *charts) deploy(path, body string) types.Item {
	x.t.Helper()
	it, err := x.items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: x.c, Path: path}, Body: strings.NewReader(body),
	})
	if err != nil {
		x.t.Fatal(err)
	}
	return it
}

// version returns the index's record of the image's tag; ok is false when
// the tag is no version.
func (x *charts) version(name, tag string) (types.PackageVersion, bool) {
	x.t.Helper()
	ctx := context.Background()
	p, err := x.db.Packages().Find(ctx, x.c.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return types.PackageVersion{}, false
	}
	if err != nil {
		x.t.Fatal(err)
	}
	v, err := x.db.Packages().Version(ctx, p.ID, tag)
	if errors.Is(err, store.ErrNotFound) {
		return types.PackageVersion{}, false
	}
	if err != nil {
		x.t.Fatal(err)
	}
	return v, true
}

// A tag holding a Helm chart is a version named by the tag, whose metadata
// is the chart's own name, version and appVersion from its config; a
// reindex records the same. Any other manifest, or a chart config that is
// not stored or names no chart, records none.
func TestUnitChartMetadata(t *testing.T) {
	x := newCharts(t)
	ctx := context.Background()
	x.deploy("acme/_uploads/sha256__"+sum(chartConfig), chartConfig)
	x.deploy("acme/1.2.3_build.4/manifest.json", helmManifest(layout.MediaTypeHelmConfig,
		chartConfig))
	v, ok := x.version("acme", "1.2.3_build.4")
	if !ok || string(v.Metadata) != chartMeta {
		t.Fatalf("chart tag %+v %s", v, v.Metadata)
	}

	for _, tc := range []struct {
		tag, body string
		// stored is a config the repository holds, if any.
		stored string
	}{
		{tag: "image", body: helmManifest("application/vnd.oci.image.config.v1+json",
			chartConfig)},
		{tag: "absent", body: helmManifest(layout.MediaTypeHelmConfig,
			`{"name":"acme","version":"9.9.9"}`)},
		{
			tag:    "unversioned",
			body:   helmManifest(layout.MediaTypeHelmConfig, `{"name":"acme"}`),
			stored: `{"name":"acme"}`,
		},
		{tag: "malformed", body: helmManifest(layout.MediaTypeHelmConfig, "{"), stored: "{"},
		{tag: "bad-digest", body: strings.Replace(
			helmManifest(layout.MediaTypeHelmConfig, chartConfig),
			"sha256:"+sum(chartConfig), "sha256:x", 1)},
		{tag: "not-json", body: "not a manifest"},
	} {
		if tc.stored != "" {
			x.deploy("acme/_uploads/sha256__"+sum(tc.stored), tc.stored)
		}
		x.deploy("acme/"+tc.tag+"/manifest.json", tc.body)
		if v, ok := x.version("acme", tc.tag); !ok || v.Metadata != nil {
			t.Errorf("%s: %+v %v", tc.tag, v, ok)
		}
	}
	x.deploy("acme/list/list.manifest.json", helmManifest(layout.MediaTypeHelmConfig,
		chartConfig))
	if v, ok := x.version("acme", "list"); !ok || v.Metadata != nil {
		t.Errorf("list: %+v %v", v, ok)
	}

	// An overwrite with an image clears it, and the chart brings it back.
	x.deploy("acme/1.2.3_build.4/manifest.json", helmManifest(
		"application/vnd.oci.image.config.v1+json", chartConfig))
	if v, _ := x.version("acme", "1.2.3_build.4"); v.Metadata != nil {
		t.Fatalf("after an image: %s", v.Metadata)
	}
	x.deploy("acme/1.2.3_build.4/manifest.json", helmManifest(layout.MediaTypeHelmConfig,
		chartConfig))

	// A reindex finds what the hooks recorded, and restores what was lost.
	v, _ = x.version("acme", "1.2.3_build.4")
	v.Metadata = nil
	if _, err := x.db.Packages().PutVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: x.db, Blobs: x.blobs}
	if err := (layout.Handler{}).Reindex(ctx, env, x.c); err != nil {
		t.Fatal(err)
	}
	if v, _ := x.version("acme", "1.2.3_build.4"); string(v.Metadata) != chartMeta {
		t.Fatalf("after a reindex: %s", v.Metadata)
	}

	// A hook run on a store alone reads no content, so records none.
	m, err := x.db.Items().Get(ctx, x.c.Key, false, "acme/1.2.3_build.4/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := (layout.Handler{}).Deployed(ctx, format.Env{Store: x.db}, x.c, m); err != nil {
		t.Fatal(err)
	}
	if v, _ := x.version("acme", "1.2.3_build.4"); v.Metadata != nil {
		t.Fatalf("without blobs: %s", v.Metadata)
	}
}

var errBlob = errors.New("blob boom")

// scriptedBlobs serves content from a map, failing to open the SHA-256s in
// fail and failing every read of those in broken. A SHA-256 in big reads as
// more than any manifest or config may hold.
type scriptedBlobs struct {
	content      map[string]string
	fail, broken map[string]bool
	big          map[string]bool
}

type readSeekCloser struct {
	io.Reader
	io.Seeker
}

func (readSeekCloser) Close() error { return nil }

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errBlob }

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

//nolint:wrapcheck // test double
func (b scriptedBlobs) Open(_ context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	switch {
	case b.fail[sha]:
		return nil, blob.Info{}, errBlob
	case b.broken[sha]:
		return readSeekCloser{brokenReader{}, nil}, blob.Info{}, nil
	case b.big[sha]:
		return readSeekCloser{io.LimitReader(zeros{}, layout.MaxManifestSize+1), nil},
			blob.Info{}, nil
	}
	c, ok := b.content[sha]
	if !ok {
		return nil, blob.Info{}, blob.ErrNotFound
	}
	return readSeekCloser{bytes.NewReader([]byte(c)), nil}, blob.Info{}, nil
}

func (b scriptedBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, errors.New("unused")
}

// A manifest or chart config that cannot be read, or is too large to be
// one, gives no chart, and neither the hook nor the reindex fails for it: a
// push never fails for its metadata. A reindex once the content reads again
// records the chart.
func TestUnitChartMetadataReads(t *testing.T) {
	x := newCharts(t)
	ctx := context.Background()
	body := helmManifest(layout.MediaTypeHelmConfig, chartConfig)
	x.deploy("acme/_uploads/sha256__"+sum(chartConfig), chartConfig)
	m := x.deploy("acme/1.0.0/manifest.json", body)
	content := map[string]string{sum(body): body, sum(chartConfig): chartConfig}
	h := layout.Handler{}
	only := func(sha string) map[string]bool { return map[string]bool{sha: true} }
	for name, b := range map[string]scriptedBlobs{
		"open the manifest":   {content: content, fail: only(sum(body))},
		"read the manifest":   {content: content, broken: only(sum(body))},
		"a large manifest":    {content: content, big: only(sum(body))},
		"open the config":     {content: content, fail: only(sum(chartConfig))},
		"read the config":     {content: content, broken: only(sum(chartConfig))},
		"a large config":      {content: content, big: only(sum(chartConfig))},
		"a config not stored": {content: map[string]string{sum(body): body}},
	} {
		t.Run(name, func(t *testing.T) {
			env := format.Env{Store: x.db, Blobs: b}
			for _, run := range []func() error{
				func() error { return h.Deployed(ctx, env, x.c, m) },
				func() error { return h.Reindex(ctx, env, x.c) },
			} {
				if err := run(); err != nil {
					t.Fatal(err)
				}
				if v, _ := x.version("acme", "1.0.0"); v.Metadata != nil {
					t.Fatalf("metadata %s", v.Metadata)
				}
			}
			if err := h.Reindex(ctx, format.Env{Store: x.db, Blobs: x.blobs}, x.c); err != nil {
				t.Fatal(err)
			}
			if v, _ := x.version("acme", "1.0.0"); string(v.Metadata) != chartMeta {
				t.Fatalf("after a reindex: %s", v.Metadata)
			}
		})
	}
}
