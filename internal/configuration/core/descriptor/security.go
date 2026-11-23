package descriptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The security section's baseline policy blocks (CFG-04), applied through
// access's SecurityPolicies, the state the Artifactory and Access REST APIs
// write: PUT /api/security/userLockPolicy,
// PUT /api/security/configuration/passwordExpirationPolicy and
// PUT /access/api/v1/config/security/user_lock_policy. hideField is the
// global switch beside them (#1903), which no REST API writes: 7.x sets it
// from the descriptor and its UI alone.
const (
	lockSection     = "userLockPolicy"
	passwordSection = "passwordSettings"
	expirySection   = "expirationPolicy"
	hideField       = "hideUnauthorizedResources"
)

// securityUnsupported are security fields 7.x documents that artifactweave
// does not apply, each with the reason its refusal gives, so a pipeline is
// told why rather than that the field is unknown. Nothing of them is stored.
var securityUnsupported = map[string]string{
	"anonAccessToBuildInfosDisabled": "is deprecated since Artifactory 6.6 and not applied",
	"crowdSettings":                  "is not applied; Crowd login is planned for P21",
	"samlSettings":                   "is not applied; SAML login is planned for P21",
	"oauthSettings":                  "is not applied; OAuth login is planned for P21",
	"httpSsoSettings":                "is not applied; HTTP SSO is planned for P21",
}

// SecurityChange is the security section's baseline policy as a document
// sets it: a nil field keeps what is stored.
type SecurityChange struct {
	LockEnabled       *bool
	LoginAttempts     *int
	ExpirationEnabled *bool
	PasswordMaxAge    *int
	NotifyByEmail     *bool
	Encryption        *accessapi.PasswordEncryption
	HideUnauthorized  *bool
}

// parseSecurity accepts Artifactory's anonymous access switch, the
// baseline policies (hideUnauthorizedResources, userLockPolicy and
// passwordSettings) and the LDAP
// connections (parseLDAP). Other security settings remain unsupported and
// fail closed.
func parseSecurity(top map[string]any, p *Patch) error {
	v, present := top[SectionSecurity]
	if !present {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: security must be a mapping", ErrInvalid)
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if reason, ok := securityUnsupported[name]; ok {
			return fmt.Errorf("%w: security.%s %s", ErrInvalid, name, reason)
		}
	}
	if bad := unknown(m, []string{"anonAccessEnabled", hideField, lockSection, passwordSection, ldapSection, ldapGroupSection}); bad != "" {
		return fmt.Errorf("%w: security: unknown field %s", ErrInvalid, bad)
	}
	if value, present := m["anonAccessEnabled"]; present {
		enabled, ok := value.(bool)
		if !ok {
			return fmt.Errorf("%w: security.anonAccessEnabled must be a boolean", ErrInvalid)
		}
		p.AnonymousAccess = &enabled
	}
	if err := parsePolicies(m, p); err != nil {
		return err
	}
	if err := parseLDAP(m, p); err != nil {
		return err
	}
	return parseLDAPGroups(m, p)
}

// parsePolicies reads hideUnauthorizedResources, userLockPolicy and
// passwordSettings. A null anywhere in them is refused: no field is a free
// string, and 7.x documents no null that resets a policy [I].
func parsePolicies(security map[string]any, p *Patch) error {
	var ch SecurityChange
	touched := false
	if v, present := security[hideField]; present {
		touched = true
		if err := setFlag("security."+hideField, v, &ch.HideUnauthorized); err != nil {
			return err
		}
	}
	if v, present := security[lockSection]; present {
		touched = true
		lock, err := block("security."+lockSection, v, "enabled", "loginAttempts")
		if err != nil {
			return err
		}
		if err := flag(lock, "security.userLockPolicy", "enabled", &ch.LockEnabled); err != nil {
			return err
		}
		if err := count(lock, "security.userLockPolicy", "loginAttempts", &ch.LoginAttempts); err != nil {
			return err
		}
	}
	if v, present := security[passwordSection]; present {
		touched = true
		where := "security." + passwordSection
		settings, err := block(where, v, "encryptionPolicy", expirySection)
		if err != nil {
			return err
		}
		if raw, present := settings["encryptionPolicy"]; present {
			policy, err := encryptionPolicy(where+".encryptionPolicy", raw)
			if err != nil {
				return err
			}
			ch.Encryption = &policy
		}
		if v, present := settings[expirySection]; present {
			where += "." + expirySection
			expiry, err := block(where, v, "enabled", "passwordMaxAge", "notifyByEmail")
			if err != nil {
				return err
			}
			if err := flag(expiry, where, "enabled", &ch.ExpirationEnabled); err != nil {
				return err
			}
			if err := count(expiry, where, "passwordMaxAge", &ch.PasswordMaxAge); err != nil {
				return err
			}
			if err := flag(expiry, where, "notifyByEmail", &ch.NotifyByEmail); err != nil {
				return err
			}
		}
	}
	if touched {
		p.Security = &ch
	}
	return nil
}

// block reads a nested mapping whose fields are among allowed.
func block(where string, v any, allowed ...string) (map[string]any, error) {
	if v == nil {
		return nil, nullRefused(where)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s must be a mapping", ErrInvalid, where)
	}
	if bad := unknown(m, allowed); bad != "" {
		return nil, fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, bad)
	}
	return m, nil
}

func flag(m map[string]any, where, name string, dst **bool) error {
	v, present := m[name]
	if !present {
		return nil
	}
	return setFlag(where+"."+name, v, dst)
}

// count reads a whole number. A YAML integer arrives as an int; anything
// else, a quoted number included, is refused rather than guessed at.
func count(m map[string]any, where, name string, dst **int) error {
	v, present := m[name]
	if !present {
		return nil
	}
	if v == nil {
		return nullRefused(where + "." + name)
	}
	n, ok := v.(int)
	if !ok {
		return fmt.Errorf("%w: %s.%s must be a whole number", ErrInvalid, where, name)
	}
	*dst = &n
	return nil
}

// encryptionPolicy reads REQUIRED, SUPPORTED or UNSUPPORTED, the spelling of
// JFrog's YAML page and the artifactory_general_security resource, in any
// case: the XML descriptor spells the same values in lower case [I].
func encryptionPolicy(where string, v any) (accessapi.PasswordEncryption, error) {
	if v == nil {
		return "", nullRefused(where)
	}
	s, ok := v.(string)
	policy := accessapi.PasswordEncryption(strings.ToLower(s))
	switch {
	case !ok:
	case policy == accessapi.EncryptionSupported, policy == accessapi.EncryptionRequired,
		policy == accessapi.EncryptionUnsupported:
		return policy, nil
	}
	return "", fmt.Errorf("%w: %s must be REQUIRED, SUPPORTED or UNSUPPORTED", ErrInvalid, where)
}

// merge is ch over cur.
func (ch SecurityChange) merge(cur accessapi.SecurityPolicy) accessapi.SecurityPolicy {
	set(&cur.LockEnabled, ch.LockEnabled)
	set(&cur.LoginAttempts, ch.LoginAttempts)
	set(&cur.ExpirationEnabled, ch.ExpirationEnabled)
	set(&cur.PasswordMaxAge, ch.PasswordMaxAge)
	set(&cur.NotifyByEmail, ch.NotifyByEmail)
	set(&cur.Encryption, ch.Encryption)
	set(&cur.HideUnauthorizedResources, ch.HideUnauthorized)
	return cur
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

// planSecurity checks the patch's policies against the current ones with the
// REST APIs' rules and records the outcome of each block the document names.
func planSecurity(ctx context.Context, d Deps, p *Patch, res *configapi.Result) error {
	if p.Security == nil {
		return nil
	}
	if d.Security == nil {
		return fmt.Errorf("security: no security policy binding: %w", kernel.ErrStore)
	}
	cur, next, err := securityPlan(ctx, d, p.Security)
	if err != nil {
		return err
	}
	for _, b := range []struct {
		name    string
		named   bool
		changed bool
	}{
		{
			"security." + hideField, p.Security.HideUnauthorized != nil,
			cur.HideUnauthorizedResources != next.HideUnauthorizedResources,
		},
		{
			"security." + lockSection, p.Security.LockEnabled != nil || p.Security.LoginAttempts != nil,
			cur.LockEnabled != next.LockEnabled || cur.LoginAttempts != next.LoginAttempts,
		},
		{
			"security." + passwordSection, p.Security.touchesPasswords(),
			cur.Encryption != next.Encryption || cur.ExpirationEnabled != next.ExpirationEnabled ||
				cur.PasswordMaxAge != next.PasswordMaxAge || cur.NotifyByEmail != next.NotifyByEmail,
		},
	} {
		switch {
		case !b.named:
		case b.changed:
			res.Updated = append(res.Updated, b.name)
		default:
			res.Unchanged = append(res.Unchanged, b.name)
		}
	}
	return nil
}

func (ch SecurityChange) touchesPasswords() bool {
	return ch.Encryption != nil || ch.ExpirationEnabled != nil || ch.PasswordMaxAge != nil || ch.NotifyByEmail != nil
}

// securityPlan reads the current policy and checks ch merged over it.
func securityPlan(
	ctx context.Context,
	d Deps,
	ch *SecurityChange,
) (cur, next accessapi.SecurityPolicy, err error) {
	cur, err = d.Security.Current(ctx)
	if err != nil {
		return cur, next, fmt.Errorf("read security policies: %w", err)
	}
	next, err = d.Security.Check(ch.merge(cur))
	var refused *accessapi.SecurityPolicyInvalid
	if errors.As(err, &refused) {
		return cur, next, fmt.Errorf("%w: %s", ErrInvalid, refused.Error())
	}
	return cur, next, err //nolint:wrapcheck // Check refuses only with SecurityPolicyInvalid
}

// applySecurity writes the policies inside the patch's transaction. It merges
// over the policy read again there, under the guard every policy write
// holds, so a REST change to a block this document leaves out, made since
// planning, is kept rather than overwritten with the planning read.
func applySecurity(ctx context.Context, d Deps, p *Patch) error {
	if p.Security == nil {
		return nil
	}
	_, next, err := securityPlan(ctx, d, p.Security)
	if err != nil {
		return err
	}
	if err := d.Security.Set(ctx, next); err != nil {
		return fmt.Errorf("security policies: %w", err)
	}
	return nil
}

// exportPolicies renders the descriptor's part of the policies:
// hideUnauthorizedResources and passwordSettings' expirationPolicy (enabled,
// passwordMaxAge, notifyByEmail, in PasswordExpirationPolicy's propOrder),
// which 7.161.26's PUT passwordExpirationPolicy stores in the descriptor.
// The lock policy and the encryption policy are Access's there, so they are
// read through GET /api/securityconfig, not the export (#1905).
// PasswordSettingsType's resetPolicy is left out: artifactweave has no
// password reset.
func exportPolicies(ctx context.Context, d Deps, out *exportSecurity) error {
	if d.Security == nil {
		return nil
	}
	p, err := d.Security.Current(ctx)
	if err != nil {
		return fmt.Errorf("read export security policies: %w", err)
	}
	out.HideUnauthorized = &p.HideUnauthorizedResources
	out.PasswordSettings = &exportFields{Fields: []exportField{
		{Name: expirySection, Children: []exportField{
			exportValue("enabled", p.ExpirationEnabled),
			exportValue("passwordMaxAge", p.PasswordMaxAge),
			exportValue("notifyByEmail", p.NotifyByEmail),
		}},
	}}
	return nil
}
