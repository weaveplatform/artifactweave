package docker_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// An image's tag is a version linking every file of its tag folder: the
// layers the push uploaded into it and the manifest that makes it a tag.
// Redeploying the tag changes only the manifest, as a push of a new image
// to the same tag over the same layers does.
func TestUnitContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Docker,
		// Docker's remote repositories, and the cache they fill, come in P9.
		NoRemoteCache: true,
		Version: func(p, v string, n int) formattest.Version {
			layer := []byte("layer of " + p + ":" + v)
			sum := sha256.Sum256(layer)
			dir := p + "/" + v + "/"
			return formattest.Version{
				Package: p, Version: v,
				Files: []formattest.File{
					{Path: dir + "sha256__" + hex.EncodeToString(sum[:]), Body: layer},
					{
						Path: dir + "manifest.json",
						Body: fmt.Appendf(nil, `{"schemaVersion":2,"push":%d}`, n),
					},
				},
			}
		},
	})
}
