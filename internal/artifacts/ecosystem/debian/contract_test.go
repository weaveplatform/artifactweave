package debian_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/apptest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/formattest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
)

func TestUnitFormatContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Debian, RepoWork: []string{debian.KindIndex},
		Version: func(name, version string, n int) formattest.Version {
			body, err := os.ReadFile(fmt.Sprintf("testdata/%s_%s_amd64_%d.deb", name, version, n))
			if err != nil {
				t.Fatal(err)
			}
			return formattest.Version{
				Package: name, Version: version,
				Files: []formattest.File{{
					Path: fmt.Sprintf("pool/%s_%s_amd64.deb", name, version), Body: body,
				}},
			}
		},
	}, apptest.FormatBackends())
}
