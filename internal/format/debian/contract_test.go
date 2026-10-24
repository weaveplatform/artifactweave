package debian_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
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
	})
}
