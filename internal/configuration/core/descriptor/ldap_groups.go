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

// ldapGroupSection is security.ldapGroupSettings, the group settings Access
// also serves at /access/api/v1/ldap/groups. Entries are written through
// access's LDAPGroupSettings, so the descriptor and the Access API store one
// setting (LDAP-03, research 06 "security").
const ldapGroupSection = "ldapGroupSettings"

// ldapGroupStrings and ldapGroupFlags map the descriptor's fields of an
// ldapGroupSettings entry, as 7.x's LdapGroupSetting and the
// artifactory_ldap_group_setting resource spell them, to the change's.
var (
	ldapGroupStrings = map[string]func(*accessapi.LDAPGroupChange) **string{
		"enabledLdap":          func(c *accessapi.LDAPGroupChange) **string { return &c.EnabledLDAP },
		"groupBaseDn":          func(c *accessapi.LDAPGroupChange) **string { return &c.GroupBaseDN },
		"groupNameAttribute":   func(c *accessapi.LDAPGroupChange) **string { return &c.GroupNameAttribute },
		"groupMemberAttribute": func(c *accessapi.LDAPGroupChange) **string { return &c.GroupMemberAttribute },
		"filter":               func(c *accessapi.LDAPGroupChange) **string { return &c.Filter },
		"descriptionAttribute": func(c *accessapi.LDAPGroupChange) **string { return &c.DescriptionAttribute },
		"strategy":             func(c *accessapi.LDAPGroupChange) **string { return &c.Strategy },
	}
	ldapGroupFlags = map[string]func(*accessapi.LDAPGroupChange) **bool{
		"subTree":              func(c *accessapi.LDAPGroupChange) **bool { return &c.SubTree },
		"forceAttributeSearch": func(c *accessapi.LDAPGroupChange) **bool { return &c.ForceAttributeSearch },
	}
)

// ldapGroupNames rewrites the Access API's field names in a refusal into the
// descriptor's.
var ldapGroupNames = strings.NewReplacer(
	"group_base_dn", "groupBaseDn",
	"group_name_attribute", "groupNameAttribute",
	"group_member_attribute", "groupMemberAttribute",
	"description_attribute", "descriptionAttribute",
	"enabled_ldap", "enabledLdap",
)

// parseLDAPGroups reads security.ldapGroupSettings. An entry set to null
// deletes the setting and a null string field clears it. Unlike
// ldapSettings, the whole section set to null deletes every group setting:
// the artifactory_ldap_group_setting resource deletes one setting by
// sending exactly that and then the settings it keeps, so refusing it would
// leave that resource unable to delete [I]. The imported groups stay.
func parseLDAPGroups(security map[string]any, p *Patch) error {
	v, present := security[ldapGroupSection]
	if !present {
		return nil
	}
	if v == nil {
		p.LDAPGroupsCleared = true
		return nil
	}
	entries, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf(
			"%w: security.ldapGroupSettings must be a mapping of names to group settings, or null", ErrInvalid)
	}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		where := "security.ldapGroupSettings." + name
		ch := accessapi.LDAPGroupChange{Name: name}
		switch entry := entries[name].(type) {
		case nil:
			ch.Delete = true
		case map[string]any:
			if err := ldapGroupFields(where, name, entry, &ch); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %s must be a mapping or null", ErrInvalid, where)
		}
		p.LDAPGroups = append(p.LDAPGroups, ch)
	}
	return nil
}

func ldapGroupFields(where, name string, entry map[string]any, ch *accessapi.LDAPGroupChange) error {
	for _, field := range slices.Sorted(maps.Keys(entry)) {
		value := entry[field]
		var err error
		switch {
		case field == "name":
			// The artifactory_ldap_group_setting resource repeats the entry
			// name; another value is refused rather than read as a rename.
			if value != nil && value != name {
				err = fmt.Errorf("%w: %s: name %v does not match the entry name", ErrInvalid, where, value)
			}
		case ldapGroupStrings[field] != nil:
			err = setString(where+"."+field, value, ldapGroupStrings[field](ch))
		case ldapGroupFlags[field] != nil:
			err = setFlag(where+"."+field, value, ldapGroupFlags[field](ch))
		default:
			err = fmt.Errorf("%w: %s: unknown field %s", ErrInvalid, where, field)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ldapGroupChanges are the patch's changes; a cleared section deletes every
// stored setting, read in ctx's transaction.
func ldapGroupChanges(ctx context.Context, d Deps, p *Patch) ([]accessapi.LDAPGroupChange, error) {
	if !p.LDAPGroupsCleared {
		return p.LDAPGroups, nil
	}
	all, err := d.LDAPGroups.GroupSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read LDAP group settings: %w", err)
	}
	out := make([]accessapi.LDAPGroupChange, 0, len(all))
	for _, g := range all {
		out = append(out, accessapi.LDAPGroupChange{Name: g.Name, Delete: true})
	}
	return out, nil
}

// planLDAPGroups checks the patch's group settings against the stored ones
// and records their outcomes, before anything is written.
func planLDAPGroups(ctx context.Context, d Deps, p *Patch, res *configapi.Result) error {
	if len(p.LDAPGroups) == 0 && !p.LDAPGroupsCleared {
		return nil
	}
	if d.LDAPGroups == nil {
		return fmt.Errorf("security.ldapGroupSettings: no LDAP group binding: %w", kernel.ErrStore)
	}
	changes, err := ldapGroupChanges(ctx, d, p)
	if err != nil {
		return err
	}
	results, err := d.LDAPGroups.PlanGroups(ctx, changes)
	if err != nil {
		return ldapGroupError(err)
	}
	if p.LDAPGroupsCleared && len(results) == 0 {
		res.Unchanged = append(res.Unchanged, "security."+ldapGroupSection)
	}
	for _, r := range results {
		name := "security.ldapGroupSettings." + r.Key
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
	return nil
}

// applyLDAPGroups writes the patch's group settings inside the patch's
// transaction. A cleared section reads the settings again there, so one
// created since planning is deleted too, as the section said.
func applyLDAPGroups(ctx context.Context, d Deps, p *Patch) error {
	if len(p.LDAPGroups) == 0 && !p.LDAPGroupsCleared {
		return nil
	}
	changes, err := ldapGroupChanges(ctx, d, p)
	if err != nil {
		return err
	}
	if _, err := d.LDAPGroups.ApplyGroups(ctx, changes); err != nil {
		return fmt.Errorf("security.ldapGroupSettings: %w", ldapGroupError(err))
	}
	return nil
}

// ldapGroupError turns access's refusal into the descriptor's.
func ldapGroupError(err error) error {
	var refused *accessapi.LDAPGroupInvalid
	if errors.As(err, &refused) {
		return fmt.Errorf("%w: security.ldapGroupSettings.%s: %s",
			ErrInvalid, refused.Name, ldapGroupNames.Replace(refused.Error()))
	}
	return err
}
