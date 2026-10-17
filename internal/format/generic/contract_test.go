package generic_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Generic files belong to no package, so every write leaves the index
// empty.
func TestContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Generic,
		Version: func(p, v string, n int) formattest.Version {
			body := []byte(p + " " + v)
			if n > 0 {
				body = append(body, " again"...)
			}
			return formattest.Version{
				Files: []formattest.File{{Path: p + "/" + v + "/" + p + ".bin", Body: body}},
			}
		},
	})
}
