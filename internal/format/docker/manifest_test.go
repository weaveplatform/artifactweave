package docker_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func digest(s string) string { return "sha256:" + sum(s) }

func image(config, layers string) string {
	return `{"schemaVersion":2,"mediaType":"` + layout.MediaTypeOCIManifest + `",` +
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + config +
		`","size":2},"layers":[` + layers + `]}`
}

func layer(mediaType, d string) string {
	return `{"mediaType":"` + mediaType + `","digest":"` + d + `","size":1}`
}

func TestParseManifest(t *testing.T) {
	const tarGzip = "application/vnd.oci.image.layer.v1.tar+gzip"
	foreign := layer("application/vnd.docker.image.rootfs.foreign.diff.tar.gzip", digest("win"))
	nondist := layer("application/vnd.oci.image.layer.nondistributable.v1.tar+gzip", digest("nd"))
	body := image(digest("c"), layer(tarGzip, digest("a"))+","+foreign+","+nondist+","+
		layer(tarGzip, digest("a"))+","+layer(tarGzip, "sha512:"+strings.Repeat("b", 128)))
	m, err := layout.ParseManifest(layout.MediaTypeOCIManifest+"; charset=utf-8", []byte(body), "h")
	if err != nil {
		t.Fatal(err)
	}
	if m.Index() || m.File() != layout.ManifestFile || m.Digest != "h" ||
		m.ConfigType() != "application/vnd.oci.image.config.v1+json" {
		t.Fatalf("%+v", m)
	}
	// The config first, each blob once, and no layer that lives elsewhere.
	want := []string{digest("c"), digest("a"), "sha512:" + strings.Repeat("b", 128)}
	if diff := cmp.Diff(want, m.Blobs()); diff != "" {
		t.Fatal(diff)
	}
	// Without a Content-Type the body's mediaType names the type; Docker's
	// schema 2 has no other field to tell it apart.
	docker := strings.Replace(body, layout.MediaTypeOCIManifest, layout.MediaTypeDockerManifest, 1)
	if m, err := layout.ParseManifest("", []byte(docker), "h"); err != nil ||
		m.MediaType != layout.MediaTypeDockerManifest {
		t.Fatalf("%+v %v", m, err)
	}
	// An OCI manifest may leave mediaType out.
	bare := strings.Replace(body, `"mediaType":"`+layout.MediaTypeOCIManifest+`",`, "", 1)
	if _, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(bare), "h"); err != nil {
		t.Fatal(err)
	}
	index := `{"schemaVersion":2,"manifests":[` + layer(layout.MediaTypeOCIManifest, digest("p")) +
		`],"subject":` + layer(layout.MediaTypeOCIManifest, digest("s")) + `}`
	for _, mt := range []string{layout.MediaTypeOCIIndex, layout.MediaTypeDockerList} {
		m, err := layout.ParseManifest(mt, []byte(index), "h")
		if err != nil || !m.Index() || m.File() != layout.ListManifestFile ||
			m.ConfigType() != "" ||
			len(m.Blobs()) != 0 ||
			len(m.Manifests) != 1 ||
			m.Subject == nil {
			t.Fatalf("%s: %+v %v", mt, m, err)
		}
	}
}

func TestParseManifestRefuses(t *testing.T) {
	ok := image(digest("c"), "")
	for name, c := range map[string]struct{ contentType, body, want string }{
		"schema 1": {"application/vnd.docker.distribution.manifest.v1+json", ok, "schema 1"},
		"signed schema 1": {
			"application/vnd.docker.distribution.manifest.v1+prettyjws", ok, "schema 1",
		},
		"another type":        {"application/json", ok, "unsupported manifest media type"},
		"no type at all":      {"", `{"schemaVersion":2,"config":{}}`, "unsupported"},
		"unparsable type":     {"a/b; =", ok, "does not parse"},
		"not JSON":            {layout.MediaTypeOCIManifest, "nope", "not a JSON manifest"},
		"trailing JSON":       {layout.MediaTypeOCIManifest, ok + "{}", "not a JSON manifest"},
		"mediaType disagrees": {layout.MediaTypeDockerManifest, ok, "does not match"},
		"schema version": {
			layout.MediaTypeOCIManifest,
			strings.Replace(ok, `"schemaVersion":2`, `"schemaVersion":1`, 1), "schemaVersion",
		},
		"no schema version": {
			layout.MediaTypeOCIManifest,
			strings.Replace(ok, `"schemaVersion":2,`, "", 1), "schemaVersion",
		},
		"no config": {
			layout.MediaTypeOCIManifest,
			`{"schemaVersion":2,"layers":[]}`, "needs a config",
		},
		"index with layers": {
			layout.MediaTypeOCIIndex,
			`{"schemaVersion":2,"layers":[` + layer("x", digest("a")) + `]}`, "no config or layers",
		},
		"bad digest": {
			layout.MediaTypeOCIManifest, image("sha256:zz", ""), "invalid descriptor digest",
		},
		"bad subject": {
			layout.MediaTypeOCIManifest,
			strings.TrimSuffix(ok, "}") + `,"subject":{"digest":"nope"}}`,
			"invalid descriptor digest",
		},
	} {
		_, err := layout.ParseManifest(c.contentType, []byte(c.body), "h")
		if !errors.Is(err, layout.ErrManifestInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestProperties(t *testing.T) {
	m := layout.Manifest{MediaType: layout.MediaTypeDockerManifest, Digest: "abc"}
	got := layout.Properties(m, "team/app", "v1", layout.Config{
		OS: "linux", Architecture: "arm64",
		Labels: map[string]string{
			"org.opencontainers.image.source": "https://example.com/app",
			"has space":                       "left out",
			"maintainer":                      "team",
		},
	})
	want := types.Properties{
		"docker.manifest":        {"v1"},
		"docker.manifest.digest": {"sha256:abc"},
		"docker.manifest.type":   {layout.MediaTypeDockerManifest},
		"docker.repoName":        {"team/app"},
		"sha256":                 {"abc"},
		"docker.os":              {"linux"},
		"docker.architecture":    {"arm64"},
		"docker.label.org.opencontainers.image.source": {"https://example.com/app"},
		"docker.label.maintainer":                      {"team"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
	// A config that names no platform sets neither property, so a search
	// for an empty value finds nothing.
	got = layout.Properties(m, "team/app", "v1", layout.Config{})
	if len(got["docker.os"]) != 0 || len(got["docker.architecture"]) != 0 {
		t.Fatal(got)
	}
}

func TestReadConfig(t *testing.T) {
	const config = `{"architecture":"arm","os":"linux","variant":"v7",` +
		`"config":{"Labels":{"a":"1","b":"2"}}}`
	want := layout.Config{
		OS: "linux", Architecture: "arm",
		Labels: map[string]string{"a": "1", "b": "2"},
	}
	for _, mt := range []string{
		"application/vnd.oci.image.config.v1+json",
		"application/vnd.docker.container.image.v1+json",
	} {
		if diff := cmp.Diff(want, layout.ReadConfig(mt, strings.NewReader(config))); diff != "" {
			t.Errorf("%s: %s", mt, diff)
		}
	}
	// An artifact's config, a broken one, an unreadable one and one too
	// large to be a config give nothing, without failing.
	huge := `{"config":{"Labels":{"a":"` + strings.Repeat("x", layout.MaxManifestSize) + `"}}}`
	for name, got := range map[string]layout.Config{
		"artifact": layout.ReadConfig(
			"application/vnd.cncf.helm.config.v1+json",
			strings.NewReader(config),
		),
		"broken":     layout.ReadConfig("application/vnd.oci.image.config.v1+json", strings.NewReader("{")),
		"unreadable": layout.ReadConfig("application/vnd.oci.image.config.v1+json", iotest.ErrReader(errors.New("x"))),
		"too large":  layout.ReadConfig("application/vnd.oci.image.config.v1+json", strings.NewReader(huge)),
	} {
		if diff := cmp.Diff(layout.Config{}, got); diff != "" {
			t.Errorf("%s: %s", name, diff)
		}
	}
}

func TestReferences(t *testing.T) {
	hex := sum("x")
	for d, valid := range map[string]bool{
		"sha256:" + hex:                        true,
		"sha512:" + strings.Repeat("a", 128):   true,
		"sha256:" + strings.ToUpper(hex):       false,
		"sha256:" + hex[:63]:                   false,
		"sha256:":                              false,
		"nope":                                 false,
		"multihash+base58:QmRZxt2b1FVZPNqd8hs": true,
	} {
		if layout.ValidDigest(d) != valid {
			t.Errorf("ValidDigest(%q) = %v", d, !valid)
		}
	}
	if got, ok := layout.SHA256Hex("sha256:" + hex); !ok || got != hex {
		t.Fatal(got, ok)
	}
	for _, d := range []string{"sha512:" + hex, "sha256:zz", hex} {
		if _, ok := layout.SHA256Hex(d); ok {
			t.Errorf("SHA256Hex(%q)", d)
		}
	}
	for tag, valid := range map[string]bool{
		"latest": true, "v1.2.3-rc_1": true, "_private": true, strings.Repeat("a", 128): true,
		strings.Repeat("a", 129): false, ".hidden": false, "-x": false, "a/b": false, "": false,
	} {
		if layout.ValidTag(tag) != valid {
			t.Errorf("ValidTag(%q) = %v", tag, !valid)
		}
	}
	// A sha256__<hex> tag is an ordinary tag, as in a 7.x local repository.
	for tag, reserved := range map[string]bool{
		"_uploads": true, "sha256__" + hex: false, "sha256__x": false, "uploads": false, "v1": false,
	} {
		if layout.ReservedTag(tag) != reserved {
			t.Errorf("ReservedTag(%q) = %v", tag, !reserved)
		}
	}
	// A digest folder keeps the digest's colon, the exact path jf docker
	// build searches; no valid tag can name it.
	folder := layout.DigestFolder(hex)
	if folder != "sha256:"+hex || layout.ValidTag(folder) || !layout.IsDigestFolder(folder) {
		t.Fatal(folder)
	}
	for _, f := range []string{"sha256__" + hex, "v1", layout.UploadsFolder} {
		if layout.IsDigestFolder(f) {
			t.Errorf("IsDigestFolder(%q)", f)
		}
	}
}

// FindManifest is an AQL query, so it runs on the reference evaluator and
// on SQL, as FindBlob does.
func TestFindManifest(t *testing.T) {
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
		t.Run(name, func(t *testing.T) { findManifest(t, open(t)) })
	}
}

func findManifest(t *testing.T, db store.Store) {
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
	deploy := func(path, body string) {
		t.Helper()
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: path}, Body: strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	deploy("a_b/v1/manifest.json", "tagged")
	deploy("a_b/"+layout.DigestFolder(sum("list"))+"/list.manifest.json", "list")
	deploy("a_b/sub/v1/manifest.json", "deep")
	deploy("a_b/v2/sha256__"+sum("layer"), "layer")
	deploy("a_b/v2/other.json", "other")
	for _, body := range []string{"tagged", "list"} {
		it, err := layout.FindManifest(ctx, db.Items(), "docker-local", "a_b", sum(body))
		if err != nil || it.Actual.SHA256 != sum(body) {
			t.Errorf("%s: %+v %v", body, it, err)
		}
	}
	names := []string{}
	for _, body := range []string{"deep", "layer", "other", "absent"} {
		if _, err := layout.FindManifest(
			ctx,
			db.Items(),
			"docker-local",
			"a_b",
			sum(body),
		); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			names = append(names, body)
		}
	}
	if len(names) > 0 {
		t.Errorf("found %v", slices.Sorted(slices.Values(names)))
	}
	_, err = layout.FindManifest(ctx, failingSearch{}, "r", "app", sum("x"))
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
