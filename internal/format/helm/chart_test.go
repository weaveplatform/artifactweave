package helm_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func read(t *testing.T, b []byte) (*helm.Chart, error) {
	t.Helper()
	return helm.ReadChart(bytes.NewReader(b), int64(len(b)))
}

// A v2 chart's Chart.yaml reads into Helm's model: every field, scalars
// kept as written, dependencies with their aliases and import-values, and
// keys outside the model dropped.
func TestUnitReadChartV2(t *testing.T) {
	yaml := `apiVersion: v2
name: web
version: 1.10
appVersion: 504e199
description: "A web app: <b>bold</b>"
home: https://example.com/web
sources: [https://example.com/src]
keywords: [web, app]
maintainers:
  - name: Ann
    email: ann@example.com
    url: https://example.com/ann
icon: https://example.com/icon.png
condition: web.enabled
tags: front
deprecated: true
annotations:
  artifacthub.io/changes: "- fixed"
  build: 96e29
kubeVersion: ">=1.22.0"
type: application
dependencies:
  - name: db
    version: 2.x
    repository: https://charts.example.com
    condition: db.enabled
    tags: [storage]
    enabled: true
    alias: database
    import-values:
      - defaults
      - child: a
        parent: b
      - {1: x}
      - [nested, {k: v}]
unknown: [1, 2]
`
	got, err := read(t, tgz(
		file{name: "web/"},
		file{name: "web/Chart.yaml", body: yaml},
		file{name: "web/values.yaml", body: "x: 1\n"},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := &helm.Chart{
		APIVersion: "v2", Name: "web", Version: "1.10", AppVersion: "504e199",
		Description: "A web app: <b>bold</b>", Home: "https://example.com/web",
		Sources: []string{"https://example.com/src"}, Keywords: []string{"web", "app"},
		Maintainers: []helm.Maintainer{
			{Name: "Ann", Email: "ann@example.com", URL: "https://example.com/ann"},
		},
		Icon: "https://example.com/icon.png", Condition: "web.enabled", Tags: "front",
		Deprecated: true,
		Annotations: map[string]string{
			"artifacthub.io/changes": "- fixed", "build": "96e29",
		},
		KubeVersion: ">=1.22.0", Type: "application",
		Dependencies: []helm.Dependency{{
			Name: "db", Version: "2.x", Repository: "https://charts.example.com",
			Condition: "db.enabled", Tags: []string{"storage"}, Enabled: true, Alias: "database",
			ImportValues: []any{
				"defaults",
				map[string]any{"child": "a", "parent": "b"},
				map[string]any{"1": "x"},
				[]any{"nested", map[string]any{"k": "v"}},
			},
		}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

// Charts as they come: a v1 chart names no apiVersion, which Helm reads as
// v1, and keeps its dependencies in requirements.yaml; a library chart; a
// chart packed by hand with Chart.yaml at the root, whose top-level folders
// are no charts of their own; Windows line endings, a byte order mark and
// backslashes; a plain tar; and a .tar.gz.
func TestUnitReadChartShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		body []byte
		want helm.Chart
	}{
		{
			"v1 with requirements",
			tgz(
				file{name: "old/requirements.yaml", body: "dependencies:\n  - name: db\n    repository: '@stable'\n"},
				file{name: "old/Chart.yaml", body: "name: old\nversion: 0.1.0\n"},
			),
			helm.Chart{
				APIVersion: "v1", Name: "old", Version: "0.1.0",
				Dependencies: []helm.Dependency{{Name: "db", Repository: "@stable"}},
			},
		},
		{
			"library",
			tgz(file{name: "lib/Chart.yaml", body: chartYAML("lib", "1.0.0", "type: library")}),
			helm.Chart{APIVersion: "v2", Name: "lib", Version: "1.0.0", Type: "library"},
		},
		{
			"packed by hand",
			tgz(
				file{name: "templates/Chart.yaml", body: chartYAML("nope", "9.9.9")},
				file{name: "Chart.yaml", body: chartYAML("root", "1.0.0")},
				file{name: "charts/sub/Chart.yaml", body: chartYAML("sub", "2.0.0")},
			),
			helm.Chart{APIVersion: "v2", Name: "root", Version: "1.0.0"},
		},
		{
			"subcharts are not the chart",
			tgz(
				file{name: "top/charts/sub/Chart.yaml", body: chartYAML("sub", "2.0.0")},
				file{name: "top/Chart.yaml", body: chartYAML("top", "1.0.0")},
			),
			helm.Chart{APIVersion: "v2", Name: "top", Version: "1.0.0"},
		},
		{
			"windows",
			tgz(file{
				name: `win\Chart.yaml`,
				body: "\xef\xbb\xbfapiVersion: v2\r\nname: win\r\nversion: 1.0.0\r\n",
			}),
			helm.Chart{APIVersion: "v2", Name: "win", Version: "1.0.0"},
		},
		{
			"plain tar, a later entry replacing an earlier",
			tarOf(false,
				file{name: "t/Chart.yaml", body: chartYAML("t", "0.0.1")},
				file{name: "t/Chart.yaml", body: chartYAML("t", "0.0.2")},
			),
			helm.Chart{APIVersion: "v2", Name: "t", Version: "0.0.2"},
		},
		{
			"the first top-level folder",
			tgz(
				file{name: "a/Chart.yaml", body: chartYAML("a", "1.0.0")},
				file{name: "b/Chart.yaml", body: chartYAML("b", "1.0.0")},
			),
			helm.Chart{APIVersion: "v2", Name: "a", Version: "1.0.0"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := read(t, c.body)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(&c.want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

// Content the format cannot index is no chart, and is stored as any file.
func TestUnitReadChartRefuses(t *testing.T) {
	big := strings.Repeat("#", 64)
	for _, c := range []struct {
		name string
		body []byte
	}{
		{"not an archive", []byte("just text")},
		{"corrupt gzip", []byte{0x1f, 0x8b, 0x08, 0, 0, 0}},
		{"zip", []byte("PK\x03\x04 not really")},
		{"empty archive", tgz()},
		{"no Chart.yaml", tgz(file{name: "x/values.yaml", body: "a: 1"})},
		{"only a subchart", tgz(file{name: "x/charts/sub/Chart.yaml", body: chartYAML("sub", "1.0.0")})},
		{"a link", tgz(file{name: "x/Chart.yaml", link: true})},
		{"a folder", tgz(file{name: "x/Chart.yaml/"})},
		{"escaping", tgz(file{name: "../Chart.yaml", body: chartYAML("x", "1.0.0")})},
		{"no mapping", tgz(file{name: "x/Chart.yaml", body: "- a\n- b\n"})},
		{"keywords as a string", tgz(file{name: "x/Chart.yaml", body: chartYAML("x", "1.0.0", "keywords: a")})},
		{"a key twice", tgz(file{name: "x/Chart.yaml", body: "name: x\nname: y\nversion: 1.0.0\n"})},
		{"no name", tgz(file{name: "x/Chart.yaml", body: "version: 1.0.0\n"})},
		{"no version", tgz(file{name: "x/Chart.yaml", body: "name: x\n"})},
		{"bad requirements", tgz(
			file{name: "x/Chart.yaml", body: chartYAML("x", "1.0.0")},
			file{name: "x/requirements.yaml", body: "dependencies: nope\n"},
		)},
		{"oversized Chart.yaml", tgz(file{name: "x/Chart.yaml", body: chartYAML("x", "1.0.0", big)})},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer helm.SetLimits(archive.Limits{Entries: 10, Inflated: 1 << 20, File: 48})()
			if got, err := read(t, c.body); !errors.Is(err, helm.ErrNotChart) {
				t.Fatalf("%+v, %v", got, err)
			}
		})
	}
	defer helm.SetLimits(archive.Limits{Entries: 1, Inflated: 1 << 20, File: 1 << 20})()
	if _, err := read(t, chartTgz("x", "1.0.0")); !errors.Is(err, helm.ErrNotChart) {
		t.Fatalf("past the entry limit: %v", err)
	}
}

var errDisk = errors.New("disk gone")

// failing reads fail past the first n bytes.
type failing struct {
	b []byte
	n int64
}

func (f failing) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.n {
		return 0, errDisk
	}
	return copy(p, f.b[off:]), nil
}

// A read that fails is a failure, not content that is no chart, or the
// chart would silently go unindexed.
func TestUnitReadChartReadFailure(t *testing.T) {
	b := chartTgz("x", "1.0.0")
	_, err := helm.ReadChart(failing{b: b, n: int64(len(b)) / 2}, int64(len(b)))
	if !errors.Is(err, errDisk) || errors.Is(err, helm.ErrNotChart) {
		t.Fatal(err)
	}
}

// brokenBlobs fails every open.
type brokenBlobs struct{}

func (brokenBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errDisk
}

func (brokenBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{}, errDisk
}

func TestUnitReadStoredOpenFailure(t *testing.T) {
	if _, err := helm.ReadStored(
		context.Background(),
		brokenBlobs{},
		"abc",
	); !errors.Is(
		err,
		errDisk,
	) {
		t.Fatal(err)
	}
}

func TestUnitPaths(t *testing.T) {
	for p, want := range map[string]bool{
		"a.tgz": true, "x/a-1.0.0.tar.gz": true, "a.tgz.prov": false, "index.yaml": false,
		"a.TGZ": false, "a.gz": false,
	} {
		if helm.IsChartPath(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
	for v, want := range map[string]bool{
		"1.0.0": true, "0.0.0-rc.1+build.5": true, "1.0.0+20210519.140020.54be389b": true,
		"v1.0.0": false, "1.0": false, "1.0.0.0": false, "01.0.0": false, "1.0.0-": false,
	} {
		if helm.IsSemver(v) != want {
			t.Errorf("%s: %v", v, !want)
		}
	}
	h := helm.Handler{}
	if got := h.Sidecars(
		repo.Config{},
		"a/b-1.0.0.tgz",
	); !cmp.Equal(
		got,
		[]string{"a/b-1.0.0.tgz.prov"},
	) {
		t.Fatal(got)
	}
	if got := h.Sidecars(repo.Config{}, "a/b-1.0.0.tgz.prov"); got != nil {
		t.Fatal(got)
	}
}
