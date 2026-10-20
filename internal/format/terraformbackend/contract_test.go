package terraformbackend_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// itemWrites is the format with the item routes' writes let through under
// the policy every format applies. The contract suite drives the item
// service as the item routes do, and the format refuses those writes
// (TestItemRouteWritesAreRefused), so without it the suite could store
// nothing to hold the index to. Every hook but Admit is the format's own.
type itemWrites struct{ tfbe.Handler }

//nolint:wrapcheck // the generic policy's Rejection, as a format answers it
func (itemWrites) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	return generic.Handler{}.Admit(ctx, env, c, w)
}

// The suite's packages are workspaces and its versions states, each a
// state.<ms>.json: "1.0" is version 1000, "2.0" version 2000.
func TestContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:    repo.TerraformBackend,
		Handler: itemWrites{},
		Version: func(p, v string, n int) formattest.Version {
			ms := map[string]int64{"1.0": 1000, "2.0": 2000}[v]
			return formattest.Version{
				Package: p, Version: tfbe.Version(ms),
				Files: []formattest.File{{
					Path: tfbe.StatePath(p, ms),
					Body: fmt.Appendf(nil, `{"version":4,"serial":%d,"lineage":%q}`, n+1, p+v),
				}},
			}
		},
		// No remote Terraform backend repository exists to cache.
		NoRemoteCache: true,
	})
}
