package descriptor

import (
	"context"
	"errors"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

var (
	_ configapi.Plan       = (*Patch)(nil)
	_ configapi.Descriptor = descriptorAPI{}
	_ configapi.System     = systemAPI{}
)

// errNotParsed refuses a plan that this package's Parse did not return,
// which no input can cause: only a caller that made its own Plan reaches it.
var errNotParsed = errors.New("descriptor: the plan was not returned by Parse")

// Deps is what Apply and the API that NewAPI binds apply a descriptor
// through: the other modules' api contracts and the platform's ports, all
// over one store. Apply runs its writes in a transaction of Tx, and every
// other port must take part in the store transaction its context carries, as
// the modules' bindings over that store do: a port that opened its own would
// leave half a descriptor applied when a later write fails (decision 0009).
// Apply needs every field; Parse reads none of them.
type Deps struct {
	// Tx runs Apply's writes as one transaction.
	Tx tx.Runner
	// Repositories reads, checks and writes the repositories of the four
	// repository sections, with the key pairs and environments their
	// entries name.
	Repositories artifactsapi.Repositories
	// Layouts checks each repoLayouts entry before it is stored.
	Layouts artifactsapi.Layouts
	// Configs holds the proxies, property sets, layouts and the
	// downloadRedirectConfig section.
	Configs configdoc.ConfigStore
	// AnonymousAccess reads and writes the security section's anonymous
	// access switch.
	AnonymousAccess accessapi.AnonymousAccess
}

// NewAPI binds the configuration module's API over d. Its Descriptor parses
// with Parse, which reads none of d, so NewAPI(Deps{}).Descriptor parses a
// descriptor before any store is open, as config apply does; its Apply takes
// only the plans Parse returned and applies them with Apply over d. Its
// System reads the minimum redirect size from d.Configs with
// DownloadRedirectMinSize. Nothing is read or written until a method is
// called.
func NewAPI(d Deps) configapi.API {
	return configapi.API{
		Descriptor: descriptorAPI{d: d},
		System:     systemAPI{configs: d.Configs},
	}
}

// DescriptorPlan makes a *Patch the configapi.Plan that the binding's Parse
// returns and its Apply takes.
func (*Patch) DescriptorPlan() {}

type descriptorAPI struct{ d Deps }

func (descriptorAPI) Parse(
	doc []byte,
	env func(name string) (string, bool),
) (configapi.Plan, error) {
	p, err := Parse(doc, env)
	if err != nil {
		// A nil *Patch in the Plan would be an interface that is not nil.
		return nil, err
	}
	return p, nil
}

func (a descriptorAPI) Apply(
	ctx context.Context,
	plan configapi.Plan,
	now time.Time,
	dryRun bool,
) (configapi.Result, error) {
	p, ok := plan.(*Patch)
	if !ok || p == nil {
		return configapi.Result{}, errNotParsed
	}
	return Apply(ctx, a.d, p, now, dryRun)
}

type systemAPI struct{ configs configdoc.ConfigStore }

func (s systemAPI) DownloadRedirectMinSize(ctx context.Context) (int64, bool, error) {
	return DownloadRedirectMinSize(ctx, s.configs)
}
