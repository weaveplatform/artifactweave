package accesstest

import (
	"github.com/weaveplatform/artifactweave/internal/access"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
)

// ProjectAdministration binds the real project lifecycle to a composite test
// store and its canonical repository API. It performs no I/O. Cross-module SQL
// tests can instrument transaction and repository boundaries without importing
// access internals or replacing the lifecycle behavior they need to exercise.
func ProjectAdministration(st access.Store, repositories artifactsapi.Repositories) project.Service {
	return project.Service{Store: st, Repositories: repositories}
}
