package accesshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The Access LDAP connections (/access/api/v1/ldap/settings, LDAP-01,
// research 05 "LDAP connections") and their test (LDAP-02), which asks the
// directory through Deps.LDAP.

// ldapTx runs fn for an administrator in one transaction under the
// administration guard, rechecking inside it that the caller is still an
// administrator: a token or a group change committed after the middleware's
// check must not reach the connections or their passwords.
func (s AccessService) ldapTx(ctx context.Context, fn func(context.Context, ldap.Service) error) error {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return err //nolint:wrapcheck // the auth error carries its status
	}
	st, err := s.content()
	if err != nil {
		return err
	}
	az, an := s.deps.API.Authorizer, s.deps.API.Authenticator
	if (az == nil) != (an == nil) {
		return fmt.Errorf("ldap: incomplete authority bindings: %w", kernel.ErrStore)
	}
	svc := ldap.Service{Configs: st.Configs(), Settings: st.Settings()}
	// Enabling a connection may write the internal password switch off
	// (ldap.Service), which this instance's logins then apply at once.
	defer s.forgetPolicies()
	return st.Tx(ctx, func(ctx context.Context) error { //nolint:wrapcheck // fn's errors are mapped by ldapError
		if err := st.Projects().Guard(ctx); err != nil {
			return fmt.Errorf("ldap: guard administration: %w", err)
		}
		if az != nil {
			p, _ := kernel.FromContext(ctx)
			current, err := an.Current(ctx, p)
			if err != nil {
				return fmt.Errorf("ldap: current caller: %w", err)
			}
			decider, err := az.For(ctx, current)
			if err != nil {
				return fmt.Errorf("ldap: current permissions: %w", err)
			}
			if decider == nil || !decider.IsAdmin() {
				return kernel.ErrForbidden
			}
		}
		return fn(ctx, svc)
	})
}

// ldapError maps the service's errors: a refusal is 400 with its message, a
// missing key 404, authentication and authorization keep their statuses,
// and anything else is a 500 that names nothing internal.
func (s AccessService) ldapError(ctx context.Context, op string, err error) error {
	var ae *apierror.Error
	var invalid *accessapi.LDAPInvalid
	switch {
	case errors.As(err, &ae), errors.Is(err, kernel.ErrForbidden),
		errors.Is(err, kernel.ErrUnauthenticated), errors.Is(err, kernel.ErrAPIRestricted):
		return err
	case errors.As(err, &invalid):
		return httpkit.BadRequestf("%s", invalid.Error())
	case errors.Is(err, ldap.ErrExists):
		return httpkit.BadRequestf("an LDAP connection with that key already exists")
	case errors.Is(err, ldap.ErrNotFound):
		return apierror.New(http.StatusNotFound, "no LDAP connection has that key")
	default:
		return s.internalError(ctx, op, err)
	}
}

// ldapChange reads a request body. Every field the body leaves out takes its
// default on a create or replace, except the manager password, which a
// replace keeps (ldap.Service.Replace).
func ldapChange(b accessgen.LdapSettings) accessapi.LDAPChange {
	ch := accessapi.LDAPChange{
		Enabled: b.Enabled, URL: b.LdapUrl, UserDNPattern: b.UserDnPattern, EmailAttribute: b.EmailAttribute,
		AutoCreateUser: b.AutoCreateUser, PoisoningProtection: b.LdapPoisoningProtection,
		AllowUserToAccessProfile: b.AllowUserToAccessProfile, PagingSupportEnabled: b.PagingSupportEnabled,
	}
	if b.Key != nil {
		ch.Key = *b.Key
	}
	if sp := b.Search; sp != nil {
		ch.SearchFilter, ch.SearchBase, ch.SearchSubTree = sp.SearchFilter, sp.SearchBase, sp.SearchSubTree
		ch.ManagerDN, ch.ManagerPassword = sp.ManagerDn, sp.ManagerPassword
	}
	return ch
}

// ldapWire is a connection as Access returns it: every field, so a client
// that compares what it sent with what it reads sees no difference, and no
// manager password.
func ldapWire(c accessapi.LDAPConnection) accessgen.LdapSettings {
	return accessgen.LdapSettings{
		Key: &c.Key, Enabled: &c.Enabled, LdapUrl: &c.URL, UserDnPattern: &c.UserDNPattern,
		EmailAttribute: &c.EmailAttribute, AutoCreateUser: &c.AutoCreateUser,
		LdapPoisoningProtection: &c.PoisoningProtection, AllowUserToAccessProfile: &c.AllowUserToAccessProfile,
		PagingSupportEnabled: &c.PagingSupportEnabled,
		Search: &accessgen.LdapSearchPattern{
			SearchFilter: &c.SearchFilter, SearchBase: &c.SearchBase, SearchSubTree: &c.SearchSubTree,
			ManagerDn: &c.ManagerDN,
		},
	}
}

// GetAllLdapSettings lists the connections (GET /access/api/v1/ldap/settings).
func (s AccessService) GetAllLdapSettings(
	ctx context.Context,
	_ accessgen.GetAllLdapSettingsRequestObject,
) (accessgen.GetAllLdapSettingsResponseObject, error) {
	var all []accessapi.LDAPConnection
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		var err error
		all, err = svc.List(ctx)
		return err //nolint:wrapcheck // mapped by ldapError
	}); err != nil {
		return nil, s.ldapError(ctx, "list LDAP connections", err)
	}
	out := make(accessgen.GetAllLdapSettings200JSONResponse, 0, len(all))
	for _, c := range all {
		out = append(out, ldapWire(c))
	}
	return out, nil
}

// GetLdapSettingsByKey returns one connection.
func (s AccessService) GetLdapSettingsByKey(
	ctx context.Context,
	req accessgen.GetLdapSettingsByKeyRequestObject,
) (accessgen.GetLdapSettingsByKeyResponseObject, error) {
	var c accessapi.LDAPConnection
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		var err error
		c, err = svc.Get(ctx, req.Key)
		return err //nolint:wrapcheck // mapped by ldapError
	}); err != nil {
		return nil, s.ldapError(ctx, "get LDAP connection", err)
	}
	return accessgen.GetLdapSettingsByKey200JSONResponse(ldapWire(c)), nil
}

// CreateLdapSettings adds a connection and answers 201, the status the
// artifactory_ldap_setting_v2 resource requires.
func (s AccessService) CreateLdapSettings(
	ctx context.Context,
	req accessgen.CreateLdapSettingsRequestObject,
) (accessgen.CreateLdapSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a body with the LDAP connection is required")
	}
	b := req.Body
	ch := ldapChange(accessgen.LdapSettings{
		Key: &b.Key, Enabled: b.Enabled, LdapUrl: &b.LdapUrl, UserDnPattern: b.UserDnPattern,
		EmailAttribute: b.EmailAttribute, AutoCreateUser: b.AutoCreateUser,
		LdapPoisoningProtection: b.LdapPoisoningProtection, AllowUserToAccessProfile: b.AllowUserToAccessProfile,
		PagingSupportEnabled: b.PagingSupportEnabled, Search: b.Search,
	})
	var c accessapi.LDAPConnection
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		var err error
		c, err = svc.Create(ctx, ch)
		return err //nolint:wrapcheck // mapped by ldapError
	}); err != nil {
		return nil, s.ldapError(ctx, "create LDAP connection", err)
	}
	return accessgen.CreateLdapSettings201JSONResponse(ldapWire(c)), nil
}

// UpdateLdapSettings replaces the connection the body's key names.
func (s AccessService) UpdateLdapSettings(
	ctx context.Context,
	req accessgen.UpdateLdapSettingsRequestObject,
) (accessgen.UpdateLdapSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a body with the LDAP connection is required")
	}
	ch := ldapChange(*req.Body)
	if ch.Key == "" {
		return nil, httpkit.BadRequestf("key is required")
	}
	var c accessapi.LDAPConnection
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		var err error
		c, err = svc.Replace(ctx, ch)
		return err //nolint:wrapcheck // mapped by ldapError
	}); err != nil {
		return nil, s.ldapError(ctx, "replace LDAP connection", err)
	}
	return accessgen.UpdateLdapSettings200JSONResponse(ldapWire(c)), nil
}

// DeleteLdapSettings removes a connection and answers 204.
func (s AccessService) DeleteLdapSettings(
	ctx context.Context,
	req accessgen.DeleteLdapSettingsRequestObject,
) (accessgen.DeleteLdapSettingsResponseObject, error) {
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		return svc.Delete(ctx, req.Key) //nolint:wrapcheck // mapped by ldapError
	}); err != nil {
		return nil, s.ldapError(ctx, "delete LDAP connection", err)
	}
	return accessgen.DeleteLdapSettings204Response{}, nil
}

// TestLdapConnection tests the connection the body describes without storing
// it (POST /access/api/v1/ldap/settings/test): 204 when the directory
// answered, 400 with what failed when it did not, so a test reports the
// directory's answer and never the settings' validity alone. The authority
// recheck and the stored password's read end their transaction before the
// directory is asked: no metadata transaction waits on directory I/O.
func (s AccessService) TestLdapConnection(
	ctx context.Context,
	req accessgen.TestLdapConnectionRequestObject,
) (accessgen.TestLdapConnectionResponseObject, error) {
	if s.deps.LDAP == nil {
		return nil, apierror.New(http.StatusNotImplemented, "LDAP connection tests are not available")
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a body with the LDAP connection is required")
	}
	var conn ldap.Connection
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		var err error
		conn, err = svc.TestConnection(ctx, ldapChange(*req.Body))
		return err //nolint:wrapcheck // mapped by ldapError
	}); err != nil {
		return nil, s.ldapError(ctx, "prepare LDAP connection test", err)
	}
	if err := s.deps.LDAP.Test(ctx, conn); err != nil {
		var failed *ldap.DirectoryError
		if errors.As(err, &failed) {
			return nil, httpkit.BadRequestf("LDAP connection test failed: %s", failed.Error())
		}
		return nil, s.internalError(ctx, "test LDAP connection", err)
	}
	return accessgen.TestLdapConnection204Response{}, nil
}
