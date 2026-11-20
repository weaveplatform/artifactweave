package registry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccRepeatedBlobPublicationNeedsCurrentWriteOnly(t *testing.T) {
	for _, method := range []string{"monolithic", "chunked", "mount"} {
		t.Run(method, func(t *testing.T) {
			each(t, func(t *testing.T, w *world, _ func() registry.Store) {
				ctx := t.Context()
				o := w.owner("app")
				allowed, checks := true, 0
				o.CheckCurrent = func(_ context.Context, path string, want accessapi.Actions) error {
					checks++
					if path != "app/" {
						t.Fatalf("blob publication checked %q instead of its image", path)
					}
					if !allowed || want != accessapi.ActionWrite {
						return kernel.ErrForbidden
					}
					return nil
				}
				publish := func() (model.Item, error) {
					switch method {
					case "chunked":
						id, err := w.svc.Begin(ctx, o)
						if err != nil {
							return model.Item{}, err
						}
						return w.svc.Finish(ctx, o, id, 0, strings.NewReader("layer"), sum("layer"))
					case "mount":
						it, found, err := w.svc.Mount(ctx, o, w.oci, "source", sum("layer"))
						if err == nil && !found {
							t.Fatal("mount did not find its source blob")
						}
						return it, err
					default:
						return w.svc.Upload(ctx, o, strings.NewReader("layer"), sum("layer"))
					}
				}
				if method == "mount" {
					w.deploy(w.oci, "source/v1/sha256__"+sum("layer"), "layer", start)
				}
				for range 2 {
					if _, err := publish(); err != nil {
						t.Fatalf("Write-only blob publication: %v", err)
					}
				}
				before, err := w.item(w.docker, "app/_uploads/sha256__"+sum("layer"))
				if err != nil {
					t.Fatal(err)
				}
				allowed, checks = false, 0
				if _, err := publish(); !errors.Is(err, kernel.ErrForbidden) || checks == 0 {
					t.Fatalf("revoked Write published a repeated blob: checks=%d err=%v", checks, err)
				}
				after, err := w.item(w.docker, before.FullPath())
				if err != nil || before.ID != after.ID || before.Actual != after.Actual || !before.Modified.Equal(after.Modified) {
					t.Fatalf("revoked publication changed the existing blob: %v", err)
				}
			})
		})
	}
}
