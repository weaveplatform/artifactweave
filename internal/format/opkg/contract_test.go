package opkg_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/opkg"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestFormatContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Opkg, RepoWork: []string{opkg.KindIndex},
		Version: func(name, version string, n int) formattest.Version {
			body, err := os.ReadFile(fmt.Sprintf("testdata/%s_%s_%d.ipk", name, version, n))
			if err != nil {
				t.Fatal(err)
			}
			return formattest.Version{
				Package: name, Version: version,
				Files: []formattest.File{{
					Path: fmt.Sprintf("feed/%s_%s_all.ipk", name, version),
					Body: body,
				}},
			}
		},
	})
}
