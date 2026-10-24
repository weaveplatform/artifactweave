package registry_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// sizeOf is the size field manifestOf and descriptorOf write for content.
func sizeOf(content string) string {
	return `"sha256:` + sum(content) + `","size":` + strconv.Itoa(len(content))
}

// wrongSize gives content's descriptor in body the size declared instead.
func wrongSize(t *testing.T, body, content string, declared int) (string, string) {
	t.Helper()
	old := sizeOf(content)
	if !strings.Contains(body, old) {
		t.Fatalf("no descriptor of %q in %s", content, body)
	}
	body = strings.Replace(body, old, `"sha256:`+sum(content)+`","size":`+strconv.Itoa(declared), 1)
	return body, sum(body)
}

// A descriptor's size must be the stored content's: a wrong size on the
// config or on a layer is refused, naming each descriptor once, and
// nothing is stored. Matching sizes are stored as before.
func TestAccDescriptorSizes(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(imageConfig, layerA, layerB)
		good, gd := manifestOf(layerA, layerB)
		if err := w.push(good, gd, "ok"); err != nil {
			t.Fatalf("matching sizes: %v", err)
		}
		for name, c := range map[string]struct {
			content  string
			declared int
		}{
			"config": {imageConfig, len(imageConfig) + 1},
			"layer":  {layerB, len(layerB) - 1},
		} {
			body, digest := wrongSize(t, good, c.content, c.declared)
			err := w.push(body, digest, "bad-"+name)
			var sizes *registry.SizeMismatchError
			if !errors.As(err, &sizes) {
				t.Fatalf("%s: err = %v", name, err)
			}
			want := []registry.SizeMismatch{
				{
					Digest: "sha256:" + sum(
						c.content,
					),
					Declared: int64(c.declared),
					Stored:   int64(len(c.content)),
				},
			}
			if diff := cmp.Diff(want, sizes.Mismatches); diff != "" {
				t.Fatalf("%s: %s", name, diff)
			}
			if !strings.Contains(err.Error(), "differ from the stored content") {
				t.Fatalf("%s: %v", name, err)
			}
			if _, err := w.item(w.docker, "app/bad-"+name); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%s: stored: %v", name, err)
			}
		}
	})
}

// A layer listed twice is looked up once and its wrong size named once;
// content the image lacks is reported before any size.
func TestAccDescriptorSizesRepeatsAndMissing(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		w.upload(imageConfig, layerA)
		twice, _ := manifestOf(layerA, layerA)
		body := strings.ReplaceAll(twice, sizeOf(layerA), `"sha256:`+sum(layerA)+`","size":1`)
		var sizes *registry.SizeMismatchError
		if err := w.push(
			body,
			sum(body),
			"v1",
		); !errors.As(err, &sizes) ||
			len(sizes.Mismatches) != 1 {
			t.Fatalf("err = %v", err)
		}
		missing, _ := manifestOf(layerA, layerB)
		body, digest := wrongSize(t, missing, layerA, 1)
		var unknown *registry.BlobsUnknownError
		if err := w.push(body, digest, "v2"); !errors.As(err, &unknown) {
			t.Fatalf("err = %v", err)
		}
	})
}

// A manifest an index lists must be stored at the size the index gives.
func TestAccIndexChildSize(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ps := w.platforms()
		good, _ := indexOf(layout.MediaTypeOCIIndex, ps...)
		body, digest := wrongSize(t, good, ps[1], len(ps[1])+10)
		err := w.pushAs(layout.MediaTypeOCIIndex, body, digest, "v1")
		var sizes *registry.SizeMismatchError
		if !errors.As(err, &sizes) {
			t.Fatalf("err = %v", err)
		}
		want := []registry.SizeMismatch{
			{
				Digest: "sha256:" + sum(
					ps[1],
				),
				Declared: int64(len(ps[1]) + 10),
				Stored:   int64(len(ps[1])),
			},
		}
		if diff := cmp.Diff(want, sizes.Mismatches); diff != "" {
			t.Fatal(diff)
		}
		if _, err := w.item(w.docker, "app/v1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("stored: %v", err)
		}
	})
}
