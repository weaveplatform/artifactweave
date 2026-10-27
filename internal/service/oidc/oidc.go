// Package oidc is Access's OpenID Connect integration (research 21): the
// providers whose ID tokens artifactweave trusts, the identity mappings that
// turn a token's claims into an access token, and the token exchange that
// CI systems call (POST /access/api/v1/oidc/token). Providers and mappings
// are configuration documents (types.ConfigOIDC, types.ConfigOIDCMapping)
// stored in their wire form, so they read back exactly as written.
package oidc

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var (
	// ErrNotFound reports a provider or mapping that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid reports a configuration or exchange request the server
	// refuses as malformed.
	ErrInvalid = errors.New("invalid")
	// ErrNoMatch reports a verified token that no identity mapping accepts.
	ErrNoMatch = errors.New("no identity mapping matches the subject token")
	// ErrUnverified reports a subject token that does not verify.
	ErrUnverified = errors.New("the subject token does not verify")
)

// failureError is an error a client sees: its message alone, without the
// sentinel's name, and it matches its kind and its cause with errors.Is.
type failureError struct {
	kind  error
	msg   string
	cause error
}

func (f failureError) Error() string   { return f.msg }
func (f failureError) Unwrap() []error { return []error{f.kind, f.cause} }

func invalidf(format string, a ...any) error {
	return failureError{kind: ErrInvalid, msg: fmt.Sprintf(format, a...)}
}

func notFoundf(format string, a ...any) error {
	return failureError{kind: ErrNotFound, msg: fmt.Sprintf(format, a...)}
}

// Provider types in the Access vocabulary, which the jfrog/platform
// Terraform provider sends and reads back.
const (
	TypeGeneric          = "Generic OpenID Connect"
	TypeGitHub           = "GitHub"
	TypeGitHubEnterprise = "GitHub Enterprise"
	TypeAzure            = "Azure"
)

// providerTypes maps every accepted spelling, folded to lower case and
// without spaces, to its Access form: the API's own values, the platform
// provider's schema values and the JFrog CLI's --oidc-provider-type values.
var providerTypes = map[string]string{
	"genericopenidconnect": TypeGeneric,
	"generic":              TypeGeneric,
	"genericoidc":          TypeGeneric,
	"github":               TypeGitHub,
	"githubenterprise":     TypeGitHubEnterprise,
	"azure":                TypeAzure,
}

// CanonicalType returns the Access form of a provider type; "" is generic.
func CanonicalType(s string) (string, bool) {
	if s == "" {
		return TypeGeneric, true
	}
	t, ok := providerTypes[strings.ToLower(strings.ReplaceAll(s, " ", ""))]
	return t, ok
}

// namePattern is the jfrog/platform provider's rule for provider and mapping
// names: no spaces or punctuation, so a name is a single path segment.
var namePattern = regexp.MustCompile(`^[^ !@#$%^&*()+={}\[\]:;'"<>,./?~` + "`" + `|\\]{1,255}$`)

func validName(kind, name string) error {
	if !namePattern.MatchString(name) {
		return invalidf(
			"%s name %q must be 1 to 255 characters without spaces or punctuation",
			kind,
			name,
		)
	}
	return nil
}

// Provider is an OIDC provider in its wire form.
type Provider struct {
	Name                          *string `json:"name,omitempty"`
	IssuerURL                     *string `json:"issuer_url,omitempty"`
	Audience                      *string `json:"audience,omitempty"`
	Description                   *string `json:"description,omitempty"`
	ProviderType                  *string `json:"provider_type,omitempty"`
	TokenIssuer                   *string `json:"token_issuer,omitempty"`
	EnablePermissiveConfiguration *bool   `json:"enable_permissive_configuration,omitempty"`
	UseDefaultProxy               *bool   `json:"use_default_proxy,omitempty"`
	AzureAppID                    *string `json:"azure_app_id,omitempty"`
	Organization                  *string `json:"organization,omitempty"`
	ProjectKey                    *string `json:"project_key,omitempty"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func flag(p *bool) bool { return p != nil && *p }

// Type is the provider's type in the Access vocabulary.
func (p Provider) Type() string {
	t, _ := CanonicalType(str(p.ProviderType))
	return t
}

// validate checks p and stores its type in the Access form.
func (p *Provider) validate() error {
	if err := validName("provider", str(p.Name)); err != nil {
		return err
	}
	u, err := url.Parse(str(p.IssuerURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return invalidf("issuer_url must be an http or https URL")
	}
	t, ok := CanonicalType(str(p.ProviderType))
	if !ok {
		return invalidf("provider_type %q is not one of %q, %q, %q or %q",
			str(p.ProviderType), TypeGeneric, TypeGitHub, TypeGitHubEnterprise, TypeAzure)
	}
	p.ProviderType = &t
	org := str(p.Organization)
	switch t {
	case TypeGitHub:
		if org == "" && !flag(p.EnablePermissiveConfiguration) {
			return invalidf("organization is required for a GitHub provider " +
				"unless enable_permissive_configuration is true")
		}
	case TypeGeneric, TypeAzure:
		if org != "" {
			return invalidf("organization applies to GitHub providers only")
		}
	}
	return nil
}

// TokenSpec shapes the access token a mapping issues.
type TokenSpec struct {
	Username        *string `json:"username,omitempty"`
	UsernamePattern *string `json:"username_pattern,omitempty"`
	GroupsPattern   *string `json:"groups_pattern,omitempty"`
	ResourcePattern *string `json:"resource_pattern,omitempty"`
	Scope           *string `json:"scope,omitempty"`
	Audience        *string `json:"audience,omitempty"`
	ExpiresIn       *int64  `json:"expires_in,omitempty"`
	SelfRevocable   *bool   `json:"self_revocable,omitempty"`
}

// Mapping is an identity mapping in its wire form. ProjectKey is set by the
// server, from a project role in the scope.
type Mapping struct {
	Name        *string        `json:"name,omitempty"`
	Description *string        `json:"description,omitempty"`
	Priority    *int           `json:"priority,omitempty"`
	Claims      map[string]any `json:"claims,omitempty"`
	TokenSpec   *TokenSpec     `json:"token_spec,omitempty"`
	ProjectKey  *string        `json:"project_key,omitempty"`
}

// stored is a mapping as kept: the mapping and when it was created, which
// breaks ties between equal priorities.
type stored struct {
	Mapping Mapping `json:"mapping"`
	Created int64   `json:"created"`
}

// MaxExpiry is the longest token a mapping issues: Access limits a mapping's
// expiration time to 24 hours.
const MaxExpiry = 24 * time.Hour

// DefaultExpiry is the lifetime of a mapping's token when its spec sets none.
const DefaultExpiry = time.Hour

// validate checks m and derives its project.
func (m *Mapping) validate() error {
	if err := validName("identity mapping", str(m.Name)); err != nil {
		return err
	}
	if m.Priority != nil && *m.Priority < 1 {
		return invalidf("priority must be at least 1")
	}
	ts := m.TokenSpec
	if ts == nil {
		return invalidf("token_spec is required")
	}
	for _, p := range []*string{ts.UsernamePattern, ts.GroupsPattern, ts.ResourcePattern} {
		if str(p) != "" {
			if _, err := parsePattern(*p); err != nil {
				return err
			}
		}
	}
	if ts.ExpiresIn != nil && *ts.ExpiresIn < 1 {
		return invalidf("token_spec.expires_in must be at least 1 second")
	}
	var sc types.Scope
	if str(ts.Scope) != "" {
		var err error
		if sc, err = types.ParseScope(*ts.Scope); err != nil {
			return invalidf("token_spec.scope: %v", err)
		}
	}
	if err := combinable(ts, sc); err != nil {
		return err
	}
	named := str(ts.Username) != "" || str(ts.UsernamePattern) != ""
	// A resource_pattern only narrows (mappedScope), so it too needs a user,
	// groups or project roles whose permissions it narrows.
	if !named && !hasKind(sc, types.ScopeGroups) && !hasKind(sc, types.ScopeRoles) &&
		str(ts.GroupsPattern) == "" {
		return invalidf("token_spec needs username, username_pattern, a groups or project " +
			"roles scope, or groups_pattern")
	}
	m.ProjectKey = nil
	for _, e := range sc.Entries {
		if e.Kind == types.ScopeRoles {
			// "Only one project key is allowed per mapping" (Manage
			// Project-Scoped Identity Mappings). Keeping the last project
			// instead would file the mapping, and the tokens it issues,
			// under one project while its scope still names roles in
			// another.
			if m.ProjectKey != nil && *m.ProjectKey != e.Project {
				return invalidf("token_spec.scope names project roles in '%s' and '%s'; "+
					"only one project key is allowed per mapping", *m.ProjectKey, e.Project)
			}
			project := e.Project
			m.ProjectKey = &project
		}
		// Without a user name the token's subject is synthetic, which only
		// the groups or project roles its scope names give permissions to.
		// A project roles mapping commonly has no user: the jfrog/platform
		// provider's documented example sends none.
		if !named && e.Kind != types.ScopeGroups && e.Kind != types.ScopeRoles {
			return invalidf("token_spec.username is required unless the scope names only " +
				"groups or project roles")
		}
	}
	return nil
}

// combinable applies the page's table of the token scopes one mapping may
// combine (research 21): admin combines with nothing, a user with a user
// pattern and groups with a groups pattern do not combine, and every other
// pair does. A user is the username field or an applied-permissions/user
// scope.
func combinable(ts *TokenSpec, sc types.Scope) error {
	userDynamic := str(ts.UsernamePattern) != ""
	groupsDynamic := str(ts.GroupsPattern) != ""
	resourceDynamic := str(ts.ResourcePattern) != ""
	switch {
	case hasKind(sc, types.ScopeAdmin) &&
		(len(sc.Entries) > 1 || userDynamic || groupsDynamic || resourceDynamic):
		return invalidf("token_spec: an admin scope combines with no other scope or pattern")
	case userDynamic && (str(ts.Username) != "" || hasKind(sc, types.ScopeUser)):
		return invalidf(
			"token_spec: username_pattern combines with neither username nor a user scope",
		)
	case groupsDynamic && hasKind(sc, types.ScopeGroups):
		return invalidf("token_spec: groups_pattern does not combine with a groups scope")
	case resourceDynamic && hasKind(sc, types.ScopeArtifact):
		// A token's artifact entries allow the union of what they name, so a
		// claim's entry beside the mapping's own would widen them.
		return invalidf(
			"token_spec: resource_pattern does not combine with artifact entries in the scope",
		)
	}
	return nil
}

func hasKind(sc types.Scope, kind types.ScopeKind) bool {
	return slices.ContainsFunc(sc.Entries, func(e types.ScopeEntry) bool { return e.Kind == kind })
}

// Service manages providers and mappings and exchanges tokens.
type Service struct {
	Store  store.Store
	Tokens *token.Service
	Keys   *idtoken.Verifier
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s Service) configs() store.ConfigStore { return s.Store.Configs() }

// Providers returns every provider, by name.
func (s Service) Providers(ctx context.Context) ([]Provider, error) {
	docs, err := s.configs().List(ctx, types.ConfigOIDC)
	if err != nil {
		return nil, fmt.Errorf("oidc: providers: %w", err)
	}
	out := make([]Provider, 0, len(docs))
	for _, d := range docs {
		var p Provider
		if err := json.Unmarshal(d.Document, &p); err != nil {
			return nil, fmt.Errorf("oidc: provider %s: %w", d.Key, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Provider returns one provider.
func (s Service) Provider(ctx context.Context, name string) (Provider, error) {
	d, err := s.configs().Get(ctx, types.ConfigOIDC, name)
	if errors.Is(err, store.ErrNotFound) {
		return Provider{}, notFoundf("OIDC provider '%s' does not exist", name)
	}
	if err != nil {
		return Provider{}, fmt.Errorf("oidc: provider %s: %w", name, err)
	}
	var p Provider
	if err := json.Unmarshal(d.Document, &p); err != nil {
		return Provider{}, fmt.Errorf("oidc: provider %s: %w", name, err)
	}
	return p, nil
}

func (s Service) putProvider(ctx context.Context, p Provider) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("oidc: provider: %w", err)
	}
	err = s.configs().Put(ctx, types.ConfigDocument{
		Kind: types.ConfigOIDC, Key: str(p.Name), Enabled: true, Document: doc, Updated: s.now(),
	})
	if err != nil {
		return fmt.Errorf("oidc: provider %s: %w", str(p.Name), err)
	}
	return nil
}

// CreateProvider adds p; a name in use is invalid, as Access answers 400.
func (s Service) CreateProvider(ctx context.Context, p Provider) (Provider, error) {
	if err := p.validate(); err != nil {
		return Provider{}, err
	}
	if _, err := s.Provider(ctx, str(p.Name)); err == nil {
		return Provider{}, invalidf("an OIDC provider named '%s' already exists", str(p.Name))
	} else if !errors.Is(err, ErrNotFound) {
		return Provider{}, err
	}
	return p, s.putProvider(ctx, p)
}

// UpdateProvider replaces the provider name with p, whose name, when set,
// must be name.
func (s Service) UpdateProvider(ctx context.Context, name string, p Provider) (Provider, error) {
	if p.Name == nil {
		p.Name = &name
	}
	if *p.Name != name {
		return Provider{}, invalidf("the body names '%s', not '%s'", *p.Name, name)
	}
	if err := p.validate(); err != nil {
		return Provider{}, err
	}
	if _, err := s.Provider(ctx, name); err != nil {
		return Provider{}, err
	}
	return p, s.putProvider(ctx, p)
}

// DeleteProvider removes a provider and its mappings.
func (s Service) DeleteProvider(ctx context.Context, name string) error {
	if _, err := s.Provider(ctx, name); err != nil {
		return err
	}
	all, err := s.stored(ctx, name)
	if err != nil {
		return err
	}
	for _, m := range all {
		if err := s.configs().
			Delete(ctx, types.ConfigOIDCMapping, mappingKey(name, str(m.Mapping.Name))); err != nil {
			return fmt.Errorf("oidc: delete mapping: %w", err)
		}
	}
	if err := s.configs().Delete(ctx, types.ConfigOIDC, name); err != nil {
		return fmt.Errorf("oidc: delete provider %s: %w", name, err)
	}
	return nil
}

func mappingKey(provider, name string) string { return provider + "/" + name }

// stored returns a provider's mappings in creation order.
func (s Service) stored(ctx context.Context, provider string) ([]stored, error) {
	docs, err := s.configs().List(ctx, types.ConfigOIDCMapping)
	if err != nil {
		return nil, fmt.Errorf("oidc: mappings: %w", err)
	}
	var out []stored
	for _, d := range docs {
		if !strings.HasPrefix(d.Key, provider+"/") {
			continue
		}
		var m stored
		if err := json.Unmarshal(d.Document, &m); err != nil {
			return nil, fmt.Errorf("oidc: mapping %s: %w", d.Key, err)
		}
		out = append(out, m)
	}
	slices.SortStableFunc(out, func(a, b stored) int { return cmp.Compare(a.Created, b.Created) })
	return out, nil
}

// Mappings returns a provider's mappings, only project's when it is set.
func (s Service) Mappings(ctx context.Context, provider, project string) ([]Mapping, error) {
	if _, err := s.Provider(ctx, provider); err != nil {
		return nil, err
	}
	all, err := s.stored(ctx, provider)
	if err != nil {
		return nil, err
	}
	out := make([]Mapping, 0, len(all))
	for _, m := range all {
		if project == "" || str(m.Mapping.ProjectKey) == project {
			out = append(out, m.Mapping)
		}
	}
	return out, nil
}

func (s Service) storedMapping(ctx context.Context, provider, name string) (stored, error) {
	if _, err := s.Provider(ctx, provider); err != nil {
		return stored{}, err
	}
	d, err := s.configs().Get(ctx, types.ConfigOIDCMapping, mappingKey(provider, name))
	if errors.Is(err, store.ErrNotFound) {
		return stored{}, notFoundf(
			"OIDC provider '%s' has no identity mapping '%s'",
			provider,
			name,
		)
	}
	if err != nil {
		return stored{}, fmt.Errorf("oidc: mapping %s: %w", name, err)
	}
	var m stored
	if err := json.Unmarshal(d.Document, &m); err != nil {
		return stored{}, fmt.Errorf("oidc: mapping %s: %w", name, err)
	}
	return m, nil
}

// Mapping returns one mapping.
func (s Service) Mapping(ctx context.Context, provider, name string) (Mapping, error) {
	m, err := s.storedMapping(ctx, provider, name)
	return m.Mapping, err
}

func (s Service) putMapping(ctx context.Context, provider string, m stored) error {
	doc, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("oidc: mapping: %w", err)
	}
	err = s.configs().Put(ctx, types.ConfigDocument{
		Kind: types.ConfigOIDCMapping, Key: mappingKey(provider, str(m.Mapping.Name)),
		Enabled: true, Document: doc, Updated: s.now(),
	})
	if err != nil {
		return fmt.Errorf("oidc: mapping %s: %w", str(m.Mapping.Name), err)
	}
	return nil
}

// CreateMapping adds m to a provider; a name in use is invalid.
func (s Service) CreateMapping(ctx context.Context, provider string, m Mapping) (Mapping, error) {
	if err := m.validate(); err != nil {
		return Mapping{}, err
	}
	if _, err := s.Provider(ctx, provider); err != nil {
		return Mapping{}, err
	}
	_, err := s.configs().Get(ctx, types.ConfigOIDCMapping, mappingKey(provider, str(m.Name)))
	switch {
	case err == nil:
		return Mapping{}, invalidf("an identity mapping named '%s' already exists", str(m.Name))
	case !errors.Is(err, store.ErrNotFound):
		return Mapping{}, fmt.Errorf("oidc: mapping %s: %w", str(m.Name), err)
	}
	return m, s.putMapping(ctx, provider, stored{Mapping: m, Created: s.now().UnixNano()})
}

// UpdateMapping replaces the mapping name with m, keeping its place among
// equal priorities. Its project, which its scope's project roles imply,
// cannot change: "Attempting to change the project key returns 400 Bad
// Request" (Manage Project-Scoped Identity Mappings), from a project to
// none or another, or from none to one.
func (s Service) UpdateMapping(
	ctx context.Context,
	provider, name string,
	m Mapping,
) (Mapping, error) {
	if m.Name == nil {
		m.Name = &name
	}
	if *m.Name != name {
		return Mapping{}, invalidf("the body names '%s', not '%s'", *m.Name, name)
	}
	if err := m.validate(); err != nil {
		return Mapping{}, err
	}
	old, err := s.storedMapping(ctx, provider, name)
	if err != nil {
		return Mapping{}, err
	}
	if was, now := str(old.Mapping.ProjectKey), str(m.ProjectKey); was != now {
		return Mapping{}, invalidf("the project of identity mapping '%s' cannot change "+
			"from '%s' to '%s'; its token_spec.scope must name the same project", name, was, now)
	}
	return m, s.putMapping(ctx, provider, stored{Mapping: m, Created: old.Created})
}

// DeleteMapping removes one mapping.
func (s Service) DeleteMapping(ctx context.Context, provider, name string) error {
	if _, err := s.storedMapping(ctx, provider, name); err != nil {
		return err
	}
	if err := s.configs().
		Delete(ctx, types.ConfigOIDCMapping, mappingKey(provider, name)); err != nil {
		return fmt.Errorf("oidc: delete mapping %s: %w", name, err)
	}
	return nil
}
