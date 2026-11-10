package server

import (
	accesshttp "github.com/weaveplatform/artifactweave/migration/access/adapters/http"
	artifactshttp "github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http"
	configurationhttp "github.com/weaveplatform/artifactweave/migration/configuration/adapters/http"
	accessgen "github.com/weaveplatform/artifactweave/migration/platform/gen/access"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/migration/platform/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/migration/platform/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/migration/platform/gen/grid"
	pfedgen "github.com/weaveplatform/artifactweave/migration/platform/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/migration/platform/gen/worker"
)

// The strict servers. Each generated interface is satisfied by a composite
// that embeds its partials at depth 1 and the generated UnimplementedStrict
// at depth 2, inside a stub holder. Go promotes the shallowest method, so
// an operation a partial implements shadows its 501 stub, and one that no
// partial implements still answers 501. Two partials implementing the same
// operation sit at the same depth, which makes the method ambiguous and
// fails the assertion below: the intended guard against a duplicate
// implementation. A partial never embeds the stubs or another partial, and
// state a partial embeds (accessState, itemkit.Kit) defines no method named
// after an operation, or it would collide with the stub at depth 2.

// artifactoryStubs holds the artifactory spec's generated stubs.
type artifactoryStubs struct{ artgen.UnimplementedStrict }

// artifactoryServer is the artifactory spec's strict server: the access
// module's security operations, the system operations, the artifacts
// module's operations, and those of each ecosystem that owns some (VCS,
// npm, Docker, Debian, CRAN and RPM). Every artifacts and ecosystem partial
// embeds the same item kit, whose methods sit at depth 2 beside the stubs
// and are named after no operation.
type artifactoryServer struct {
	*accesshttp.SecurityAPI
	*configurationhttp.SystemAPI
	*artifactshttp.ArtifactoryAPI
	*vcsAPI
	*npmAPI
	*dockerAPI
	*debianAPI
	*cranAPI
	*RPMAPI
	artifactoryStubs
}

// accessStubs holds the access spec's generated stubs.
type accessStubs struct{ accessgen.UnimplementedStrict }

// accessServer is the access spec's strict server.
type accessServer struct {
	*accesshttp.AccessService
	accessStubs
}

// eventService serves /event (webhook subscriptions). Nothing is implemented
// until the webhooks phase; the embedded stub answers 501 for every operation.
type eventService struct {
	eventgen.UnimplementedStrict
}

// The services brought into scope in P1b (research 16) start with every
// operation stubbed; their phases replace the stubs.
type (
	distributionService struct {
		distributiongen.UnimplementedStrict
	}
	workerService struct{ workergen.UnimplementedStrict }
	pfedService   struct{ pfedgen.UnimplementedStrict }
	gridService   struct{ gridgen.UnimplementedStrict }
)

var (
	_ artgen.StrictServerInterface          = artifactoryServer{}
	_ accessgen.StrictServerInterface       = accessServer{}
	_ eventgen.StrictServerInterface        = eventService{}
	_ distributiongen.StrictServerInterface = distributionService{}
	_ workergen.StrictServerInterface       = workerService{}
	_ pfedgen.StrictServerInterface         = pfedService{}
	_ gridgen.StrictServerInterface         = gridService{}
)
