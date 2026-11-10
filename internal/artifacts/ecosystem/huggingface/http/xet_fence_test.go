package huggingfacehttp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	format "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface"
)

func TestUnitXetDeliveryRevalidatesMetadataPathAndArtifact(t *testing.T) {
	id := format.Identity{Kind: "models", Owner: "acme", Name: "demo"}
	file := sibling{Filename: "weights.bin"}
	for _, revoked := range []string{"metadata", "path", "artifact"} {
		t.Run(revoked, func(t *testing.T) {
			var checked []string
			check := func(name string) error {
				checked = append(checked, name)
				if name == revoked {
					return source.ErrDenied
				}
				return nil
			}
			chosen := selected{
				fence: func(context.Context) error { return check("metadata") },
				check: func(_ context.Context, path string) error {
					if path != "models--acme--demo/snapshots/"+strings.Repeat(
						"a",
						40,
					)+"/weights.bin" {
						t.Fatalf("wrong authority path: %s", path)
					}
					return check("path")
				},
			}
			chosen.info.SHA = strings.Repeat("a", 40)
			result := source.Result{
				Revalidate: func(context.Context) error { return check("artifact") },
			}
			if err := (handler{}).xetSourceFence(
				t.Context(),
				chosen,
				id,
				file,
				result,
			); !errors.Is(
				err,
				source.ErrDenied,
			) {
				t.Fatalf("revoked Xet authority accepted: %v", err)
			}
			want := map[string]string{"metadata": "metadata", "path": "metadata,path", "artifact": "metadata,path,artifact"}[revoked]
			if strings.Join(checked, ",") != want {
				t.Fatalf("checks continued past revocation: %v", checked)
			}
		})
	}
}
