package descriptor

import (
	"context"
	"errors"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
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
// Apply and Export need the store and owning read contracts; Parse reads none of them.
type Deps struct {
	// Tx runs descriptor writes or the export snapshot as one transaction.
	// Export additionally requires InTx(context.Context) bool to refuse an
	// existing transaction on this store; Apply still joins one as Runner does.
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
	// LDAP checks and writes the security section's LDAP connections and
	// reads them for the export. A descriptor that names connections is
	// refused without it; an export without it has no ldapSettings.
	LDAP accessapi.LDAPSettings
	// Security checks and writes the security section's userLockPolicy and
	// passwordSettings and reads them for the export. A descriptor that names
	// them is refused without it; an export without it leaves them out.
	Security accessapi.SecurityPolicies
	// Projects serializes planning and publication before repository locks,
	// including a descriptor that also changes anonymous credential policy.
	Projects accessapi.Projects
	// Authentication and authorization are rechecked under Projects.Guard before
	// exporting protected configuration. Both must be set for HTTP callers; both
	// may be nil only for trusted embeddings that establish global administrator
	// authority before XML. A one-sided binding is refused. Parse and CLI Apply
	// do not use these read-authority contracts.
	Authenticator accessapi.Authenticator
	Authorizer    accessapi.Authorizer
	// Settings supply the installation's base_url, which a stored urlBase
	// overrides, and the CDN hosts a urlBase may not sit above.
	Settings configapi.Settings
}

// NewAPI binds the configuration module's API over d. Its Descriptor parses
// with Parse, which reads none of d, so NewAPI(Deps{}).Descriptor parses a
// descriptor before any store is open, as config apply does; its Apply takes
// only the plans Parse returned and applies them with Apply over d. Its
// System reads the minimum redirect size from d.Configs with
// DownloadRedirectMinSize. Nothing is read or written until a method is
// called.
func NewAPI(d Deps) configapi.API {
	var guard func(context.Context) error
	if d.Projects != nil {
		guard = d.Projects.Guard
	}
	public := newPublicURL(d)
	return configapi.API{
		Descriptor: descriptorAPI{d: d, public: public},
		Export:     exportAPI{d: d},
		System:     systemAPI{configs: d.Configs},
		Mail:       system.Mail{Configs: d.Configs, Tx: d.Tx, Guard: guard},
		WebServer:  webServerAPI{d: d, public: public},
	}
}

// DescriptorPlan makes a *Patch the configapi.Plan that the binding's Parse
// returns and its Apply takes.
func (*Patch) DescriptorPlan() {}

type descriptorAPI struct {
	d      Deps
	public *system.PublicURL
}

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
	res, err := Apply(ctx, a.d, p, now, dryRun)
	if err == nil && !dryRun && touchesURLBase(p) {
		// Inside an enclosing transaction this runs before that commits; the
		// next read may then cache the old value until PublicURLRefresh.
		a.public.Invalidate()
	}
	if err == nil && !dryRun && (p.Security != nil || len(p.LDAP) > 0) && a.d.Security != nil {
		// An enabled LDAP connection may have turned internal password
		// login off, which the authenticator caches with the policies.
		a.d.Security.Forget()
	}
	return res, err
}

type systemAPI struct{ configs configdoc.ConfigStore }

func (s systemAPI) DownloadRedirectMinSize(ctx context.Context) (int64, bool, error) {
	return DownloadRedirectMinSize(ctx, s.configs)
}
