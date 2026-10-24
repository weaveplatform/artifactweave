package rpm_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/rpm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestFormatContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.RPM, RepoWork: []string{rpm.KindIndex},
		Local: func(c *repo.Config) {
			yes := true
			c.CalculateYumMetadata = &yes
		},
		Version: func(name, version string, n int) formattest.Version {
			body, err := os.ReadFile(fmt.Sprintf("testdata/%s_%s_%d.rpm", name, version, n))
			if err != nil {
				t.Fatal(err)
			}
			return formattest.Version{
				Package: name, Version: version + "-1.fc42.noarch",
				Files: []formattest.File{{
					Path: fmt.Sprintf("pool/%s-%s-1.fc42.noarch.rpm", name, version),
					Body: body,
				}},
			}
		},
	})
}
