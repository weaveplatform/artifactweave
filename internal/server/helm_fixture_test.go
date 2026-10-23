package server

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// helmChart remains for shared root assertions that exercise Helm sidecars.
func helmChart(name, version string) []byte { return support.HelmChart(name, version) }

func grantCharter(t *testing.T, db store.Store) {
	t.Helper()
	support.SeedHelmCharter(t, db, cheapHash)
}
