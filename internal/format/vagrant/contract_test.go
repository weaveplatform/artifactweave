package vagrant_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/vagrant"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The generic contract fixture has no deployment-property field. This adapter
// supplies the properties a Vagrant client's matrix upload normally carries.
type contractHandler struct{ vagrant.Handler }

func (h contractHandler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w.Properties = maps.Clone(w.Properties)
	parts := strings.Split(w.Path, "/")
	if len(parts) >= 3 && strings.HasSuffix(w.Path, ".box") {
		if w.Properties == nil {
			w.Properties = types.Properties{}
		}
		w.Properties[vagrant.PropName] = []string{parts[len(parts)-3]}
		w.Properties[vagrant.PropVersion] = []string{parts[len(parts)-2]}
		w.Properties[vagrant.PropProvider] = []string{"virtualbox"}
	}
	return h.Handler.Admit(ctx, env, c, w)
}

func TestUnitFormatContract(t *testing.T) {
	formattest.Run(
		t,
		formattest.Format{
			Type:          repo.Vagrant,
			Handler:       contractHandler{},
			NoRemoteCache: true,
			Version: func(p, v string, n int) formattest.Version {
				var b bytes.Buffer
				tw := tar.NewWriter(&b)
				for _, e := range []struct{ name, body string }{{"metadata.json", `{"provider":"virtualbox"}`}, {"disk.vmdk", fmt.Sprintf("disk %d", n)}} {
					if err := tw.WriteHeader(
						&tar.Header{Name: e.name, Size: int64(len(e.body)), Mode: 0o644},
					); err != nil {
						t.Fatal(err)
					}
					if _, err := tw.Write([]byte(e.body)); err != nil {
						t.Fatal(err)
					}
				}
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
				return formattest.Version{
					Package: p,
					Version: v,
					Files:   []formattest.File{{Path: p + "/" + v + "/image.box", Body: b.Bytes()}},
				}
			},
		},
	)
}
