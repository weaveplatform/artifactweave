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

// ldapSection is security.ldapSettings, the connections Access also serves
// at /access/api/v1/ldap/settings. Entries are written through access's
// LDAPSettings, so the descriptor and the Access API store one connection
// (LDAP-01, research 06 "security").
const ldapSection = "ldapSettings"

// ldapStrings and ldapFlags map the descriptor's camelCase fields of an
// ldapSettings entry, as 7.x's LdapSetting and the artifactory_ldap_setting
// resource spell them, to the change's fields. The mapping is explicit so a
// field one surface adds cannot reach the other unnoticed.
var (
	ldapStrings = map[string]func(*accessapi.LDAPChange) **string{
		"ldapUrl":        func(c *accessapi.LDAPChange) **string { return &c.URL },
		"userDnPattern":  func(c *accessapi.LDAPChange) **string { return &c.UserDNPattern },
		"emailAttribute": func(c *accessapi.LDAPChange) **string { return &c.EmailAttribute },
	}
	ldapFlags = map[string]func(*accessapi.LDAPChange) **bool{
		"enabled":                  func(c *accessapi.LDAPChange) **bool { return &c.Enabled },
		"autoCreateUser":           func(c *accessapi.LDAPChange) **bool { return &c.AutoCreateUser },
		"ldapPoisoningProtection":  func(c *accessapi.LDAPChange) **bool { return &c.PoisoningProtection },
		"allowUserToAccessProfile": func(c *accessapi.LDAPChange) **bool { return &c.AllowUserToAccessProfile },
		"pagingSupportEnabled":     func(c *accessapi.LDAPChange) **bool { return &c.PagingSupportEnabled },
	}
	ldapSearchStrings = map[string]func(*accessapi.LDAPChange) **string{
		"searchFilter":    func(c *accessapi.LDAPChange) **string { return &c.SearchFilter },
		"searchBase":      func(c *accessapi.LDAPChange) **string { return &c.SearchBase },
		"managerDn":       func(c *accessapi.LDAPChange) **string { return &c.ManagerDN },
		"managerPassword": func(c *accessapi.LDAPChange) **string { return &c.ManagerPassword },
	}
	ldapSearchFlags = map[string]func(*accessapi.LDAPChange) **bool{
		"searchSubTree": func(c *accessapi.LDAPChange) **bool { return &c.SearchSubTree },
	}
)

// ldapNames rewrites the Access API's field names in a refusal into the
// descriptor's. Longer names come first so search.manager_dn is not read as
// a prefix of search.manager_password's rewrite.
var ldapNames = strings.NewReplacer(
	"search.manager_password", "search.managerPassword",
	"search.search_filter", "search.searchFilter",
	"search.search_base", "search.searchBase",
	"search.manager_dn", "search.managerDn",
	"user_dn_pattern", "userDnPattern",
	"email_attribute", "emailAttribute",
	"ldap_url", "ldapUrl",
)

// parseLDAP reads security.ldapSettings. An entry set to null deletes the
// connection; a null string field clears it ("" in the change), and
// managerPassword null or "" clears the sealed password. The whole section
// set to null is refused, as every named section's is: Artifactory has no
// documented form that deletes every connection.
func parseLDAP(security map[string]any, p *Patch) error {
	v, present := security[ldapSection]
	if !present {
		return nil
	}
	entries, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf(
			"%w: security.ldapSettings must be a mapping of keys to connections; to delete one, set it to null",
			ErrInvalid,
		)
	}
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		where := "security.ldapSettings." + key
		ch := accessapi.LDAPChange{Key: key}
		switch entry := entries[key].(type) {
		case nil:
			ch.Delete = true
		case map[string]any:
			if err := ldapFields(where, key, entry, &ch); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s must be a mapping or null", ErrInvalid, where)
		}
		p.LDAP = append(p.LDAP, ch)
	}
	return nil
}

func ldapFields(where, key string, entry map[string]any, ch *accessapi.LDAPChange) error {
	for _, name := range slices.Sorted(maps.Keys(entry)) {
		value := entry[name]
		switch {
		case name == "key":
			// The artifactory_ldap_setting resource repeats the entry key;
			// another value is refused rather than read as a rename.
			if value != nil && value != key {
				return fmt.Errorf("%w: %s: key %v does not match the entry key", ErrInvalid, where, value)
			}
		case name == "search":
			if err := ldapSearch(where+".search", value, ch); err != nil {
				return err
			}
		case ldapStrings[name] != nil:
			if err := setString(where+"."+name, value, ldapStrings[name](ch)); err != nil {
				return err
			}
		case ldapFlags[name] != nil:
			if err := setFlag(where+"."+name, value, ldapFlags[name](ch)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, name)
		}
	}
	return nil
}

func ldapSearch(where string, value any, ch *accessapi.LDAPChange) error {
	if value == nil {
		return nullRefused(where)
	}
	m, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s must be a mapping", ErrInvalid, where)
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		var err error
		switch {
		case ldapSearchStrings[name] != nil:
			err = setString(where+"."+name, m[name], ldapSearchStrings[name](ch))
		case ldapSearchFlags[name] != nil:
			err = setFlag(where+"."+name, m[name], ldapSearchFlags[name](ch))
		default:
			err = fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func setString(where string, value any, dst **string) error {
	if value == nil {
		empty := ""
		*dst = &empty
		return nil
	}
	s, ok := value.(string)
	if !ok {
		// A password written as a bare YAML number would otherwise be
		// stored as its decimal spelling, which may not be what was meant.
		return fmt.Errorf("%w: %s must be a string", ErrInvalid, where)
	}
	*dst = &s
	return nil
}

func setFlag(where string, value any, dst **bool) error {
	if value == nil {
		return nullRefused(where)
	}
	b, ok := value.(bool)
	if !ok {
		return fmt.Errorf("%w: %s must be a boolean", ErrInvalid, where)
	}
	*dst = &b
	return nil
}

// planLDAP checks the patch's connections against the stored ones and
// records their outcomes. It runs with every other entry's checks, before
// anything is written.
func planLDAP(ctx context.Context, d Deps, p *Patch, res *configapi.Result) ([]accessapi.LDAPResult, error) {
	if len(p.LDAP) == 0 {
		return nil, nil
	}
	if d.LDAP == nil {
		return nil, fmt.Errorf("security.ldapSettings: no LDAP binding: %w", kernel.ErrStore)
	}
	results, err := d.LDAP.Plan(ctx, p.LDAP)
	if err != nil {
		return nil, ldapError(err)
	}
	for _, r := range results {
		name := "security.ldapSettings." + r.Key
		switch r.Outcome {
		case accessapi.LDAPCreated:
			res.Created = append(res.Created, name)
		case accessapi.LDAPUpdated:
			res.Updated = append(res.Updated, name)
		case accessapi.LDAPDeleted:
			res.Deleted = append(res.Deleted, name)
		case accessapi.LDAPUnchanged:
			res.Unchanged = append(res.Unchanged, name)
		}
	}
	return results, nil
}

// applyLDAP writes the patch's connections inside the patch's transaction.
func applyLDAP(ctx context.Context, d Deps, p *Patch) error {
	if len(p.LDAP) == 0 {
		return nil
	}
	if _, err := d.LDAP.Apply(ctx, p.LDAP); err != nil {
		return fmt.Errorf("security.ldapSettings: %w", ldapError(err))
	}
	return nil
}

// ldapError turns access's refusal into the descriptor's: ErrInvalid, the
// entry named, and the fields in the descriptor's spelling.
func ldapError(err error) error {
	var refused *accessapi.LDAPInvalid
	if errors.As(err, &refused) {
		return fmt.Errorf("%w: security.ldapSettings.%s: %s", ErrInvalid, refused.Key, ldapNames.Replace(refused.Error()))
	}
	return err
}

// exportLDAP renders the connections in 7.x's LdapSetting and SearchType
// element order. The manager password is never exported: the provider of
// the descriptor resource ignores it on read, and the export must not carry
// a sealed secret (decision 0017). Empty strings are left out, as JAXB
// leaves out an unset element.
func exportLDAP(ctx context.Context, d Deps) ([]exportFields, error) {
	if d.LDAP == nil {
		return nil, nil
	}
	all, err := d.LDAP.Connections(ctx)
	if err != nil {
		return nil, fmt.Errorf("read export LDAP connections: %w", err)
	}
	out := make([]exportFields, 0, len(all))
	for _, c := range all {
		text := func(fields []exportField, name, value string) []exportField {
			if value == "" {
				return fields
			}
			return append(fields, exportValue(name, value))
		}
		search := exportField{Name: "search"}
		search.Children = text(search.Children, "searchFilter", c.SearchFilter)
		search.Children = text(search.Children, "searchBase", c.SearchBase)
		search.Children = append(search.Children, exportValue("searchSubTree", c.SearchSubTree))
		search.Children = text(search.Children, "managerDn", c.ManagerDN)
		fields := []exportField{exportValue("key", c.Key), exportValue("enabled", c.Enabled)}
		fields = text(fields, "ldapUrl", c.URL)
		fields = text(fields, "userDnPattern", c.UserDNPattern)
		fields = append(fields, search, exportValue("autoCreateUser", c.AutoCreateUser))
		fields = text(fields, "emailAttribute", c.EmailAttribute)
		fields = append(fields,
			exportValue("ldapPoisoningProtection", c.PoisoningProtection),
			exportValue("allowUserToAccessProfile", c.AllowUserToAccessProfile),
			exportValue("pagingSupportEnabled", c.PagingSupportEnabled),
		)
		out = append(out, exportFields{Fields: fields})
	}
	return out, nil
}

// readExportSecurity reads the security element: the anonymous access switch,
// unset until first written, and the LDAP connections.
func readExportSecurity(ctx context.Context, d Deps) (exportSecurity, error) {
	var out exportSecurity
	var err error
	out.Anonymous, err = d.AnonymousAccess.Enabled(ctx)
	if errors.Is(err, kernel.ErrNotFound) {
		err = nil
	}
	if err != nil {
		return out, fmt.Errorf("read export anonymous access: %w", err)
	}
	if err := exportPolicies(ctx, d, &out); err != nil {
		return out, err
	}
	if out.LDAP, err = exportLDAP(ctx, d); err != nil {
		return out, err
	}
	if out.LDAPGroups, err = exportLDAPGroups(ctx, d); err != nil {
		return out, err
	}
	return out, nil
}
