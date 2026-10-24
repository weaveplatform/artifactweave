package helm_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A version is its chart archive with its provenance file, which the format
// keeps with the chart, and a second archive of the same chart in another
// folder, which the index links too, as a repository without Enforce Layout
// keeps duplicates. Beside them lies a .tgz that holds no chart, which is
// stored but not indexed. Every write queues the repository's index.yaml.
func TestUnitContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:     repo.Helm,
		RepoWork: []string{helm.KindIndex},
		Version: func(p, v string, n int) formattest.Version {
			version := v + ".0"
			archive := fmt.Sprintf("charts/%s/%s-%s.tgz", p, p, version)
			mirror := fmt.Sprintf("mirror/%s-%s.tgz", p, version)
			return formattest.Version{
				Package: p, Version: version,
				Files: []formattest.File{
					{
						Path: archive,
						Body: chartTgz(p, version, fmt.Sprintf("description: deploy %d", n)),
					},
					{
						Path:      archive + helm.ProvSuffix,
						SidecarOf: archive,
						Body: fmt.Appendf(
							nil,
							"-----BEGIN PGP SIGNED MESSAGE-----\n%s #%d\n",
							archive,
							n,
						),
					},
					{
						Path: mirror,
						Body: chartTgz(p, version, fmt.Sprintf("description: mirror %d", n)),
					},
					{
						Path: fmt.Sprintf("charts/%s/notes-%s.tgz", p, version),
						Body: fmt.Appendf(nil, "no chart #%d", n),
					},
				},
				Linked: []string{archive, mirror},
			}
		},
	})
}
