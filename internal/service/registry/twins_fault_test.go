package registry_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// twinFaults adds property failures for one item to retentionFaults.
type twinFaults struct {
	retentionFaults
	// read and patch fail Properties and PatchProperties on the item with
	// this ID.
	read, patch int64
}

func (f *twinFaults) Items() store.ItemStore {
	return faultyProps{f.retentionFaults.Items(), f}
}

type faultyProps struct {
	store.ItemStore
	f *twinFaults
}

//nolint:wrapcheck // test double
func (p faultyProps) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if id == p.f.read {
		return nil, errBoom
	}
	return p.ItemStore.Properties(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyProps) PatchProperties(
	ctx context.Context, id int64, set types.Properties, remove []string, at time.Time,
) error {
	if id == p.f.patch {
		return errBoom
	}
	return p.ItemStore.PatchProperties(ctx, id, set, remove, at)
}

// Every store failure in finding, adopting or removing a tag's digest twin
// comes back to the caller, whose transaction rolls back.
func TestTwinFailures(t *testing.T) {
	body, digest := manifestOf(layerA)
	twinDir := "app/" + layout.DigestFolder(digest)
	removeV1 := func(w *world, svc *registry.Service) error {
		return svc.RemoveTag(context.Background(), w.owner("app"), "v1",
			registry.Removal{Twin: true})
	}
	pushOver := func(edit func(*repo.Config), tag string) func(*world, *registry.Service) error {
		return func(w *world, svc *registry.Service) error {
			c := w.docker
			edit(&c)
			other, otherDigest := manifestOf(layerB)
			m, err := layout.ParseManifest(layout.MediaTypeOCIManifest, []byte(other), otherDigest)
			if err != nil {
				return err
			}
			return svc.PutManifest(context.Background(), registry.Push{
				Owner: registry.Owner{Repo: c, Name: "app", By: "alice"}, Manifest: m,
				Body: []byte(other), Folders: []string{tag},
			})
		}
	}
	cases := []struct {
		name   string
		faults func(twin types.Item) twinFaults
		run    func(*world, *registry.Service) error
	}{
		{
			name: "limit: the tag's manifest",
			run:  pushOver(func(c *repo.Config) { c.MaxUniqueTags = ptr(1) }, "v2"),
			faults: func(types.Item) twinFaults {
				return twinFaults{retentionFaults: retentionFaults{
					get: func(p string) bool { return p == "app/v1/manifest.json" },
				}}
			},
		},
		{
			name: "remove: the image's manifests", run: removeV1,
			faults: func(types.Item) twinFaults {
				return twinFaults{retentionFaults: retentionFaults{search: manifestsQuery}}
			},
		},
		{
			name: "remove: the twin's folder", run: removeV1,
			faults: func(types.Item) twinFaults {
				return twinFaults{retentionFaults: retentionFaults{
					get: func(p string) bool { return p == twinDir },
				}}
			},
		},
		{
			name: "remove: the twin's manifest", run: removeV1,
			faults: func(types.Item) twinFaults {
				return twinFaults{retentionFaults: retentionFaults{
					get: func(p string) bool { return p == twinDir+"/manifest.json" },
				}}
			},
		},
		{
			name: "remove: the twin's properties", run: removeV1,
			faults: func(twin types.Item) twinFaults { return twinFaults{read: twin.ID} },
		},
		{
			name: "remove: the twin", run: removeV1,
			faults: func(types.Item) twinFaults {
				return twinFaults{retentionFaults: retentionFaults{
					del: func(p string) bool { return p == twinDir },
				}}
			},
		},
		{
			name: "retention: name the tag",
			run:  pushOver(func(c *repo.Config) { c.DockerTagRetention = ptr(2) }, "v1"),
			faults: func(twin types.Item) twinFaults {
				return twinFaults{patch: twin.ID}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, inmem.New())
			w.upload(imageConfig, layerA, layerB)
			if err := w.push(body, digest, layout.DigestFolder(digest), "v1"); err != nil {
				t.Fatal(err)
			}
			twin, err := w.item(w.docker, twinDir+"/manifest.json")
			if err != nil {
				t.Fatal(err)
			}
			f := tc.faults(twin)
			f.Store = w.db
			svc := &registry.Service{Store: &f, Blobs: w.blobs, Now: w.svc.Now}
			if err := tc.run(w, svc); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A digest folder that holds no manifest, or another one than its name
// says, is no twin, and removing the tag leaves it.
func TestTwinIsTheManifestItNames(t *testing.T) {
	each(t, func(t *testing.T, w *world, _ func() store.Store) {
		ctx := context.Background()
		w.upload(imageConfig, layerA, layerB)
		for i, layer := range []string{layerA, layerB} {
			body, digest := manifestOf(layer)
			tag := "v" + strconv.Itoa(i+1)
			if err := w.push(body, digest, tag); err != nil {
				t.Fatal(err)
			}
			dir := "app/" + layout.DigestFolder(digest)
			if i == 0 {
				w.deploy(w.docker, dir+"/notes.txt", "not a manifest", start)
			} else {
				w.deploy(w.docker, dir+"/manifest.json", `{"another":"manifest"}`, start)
			}
			if err := w.svc.RemoveTag(ctx, w.owner("app"), tag,
				registry.Removal{Twin: true}); err != nil {
				t.Fatal(err)
			}
			if !w.exists(dir) {
				t.Errorf("%s went with %s", dir, tag)
			}
		}
	})
}
