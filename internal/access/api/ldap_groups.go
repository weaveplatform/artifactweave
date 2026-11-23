package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// LDAPGroupStrategy is how a group setting maps directory entries to group
// memberships: 7.x's LdapGroupPopulatorStrategies, spelled as its
// descriptor and the artifactory_ldap_group_setting resources spell them.
type LDAPGroupStrategy string

// The strategies JFrog's LDAP page describes ("LDAP Group Synchronization
// Strategies").
const (
	// LDAPStatic: group entries list their members.
	LDAPStatic LDAPGroupStrategy = "STATIC"
	// LDAPDynamic: user entries list their groups.
	LDAPDynamic LDAPGroupStrategy = "DYNAMIC"
	// LDAPHierarchical: a user's DN names its groups.
	LDAPHierarchical LDAPGroupStrategy = "HIERARCHICAL"
)

// LDAPGroupSetting is one LDAP group setting: how groups are read from the
// connection EnabledLDAP names and mapped to local groups. The Access API's
// /access/api/v1/ldap/groups and the descriptor's security.ldapGroupSettings
// read and write the same settings.
type LDAPGroupSetting struct {
	Name                 string
	EnabledLDAP          string
	GroupBaseDN          string
	GroupNameAttribute   string
	GroupMemberAttribute string
	Filter               string
	DescriptionAttribute string
	SubTree              bool
	ForceAttributeSearch bool
	Strategy             LDAPGroupStrategy
}

// LDAPGroupChange is one group setting a descriptor creates, updates or
// deletes. A nil field is left as stored, or takes its default on a setting
// the change creates; a set string, even an empty one, replaces the stored
// value.
type LDAPGroupChange struct {
	Name   string
	Delete bool

	EnabledLDAP          *string
	GroupBaseDN          *string
	GroupNameAttribute   *string
	GroupMemberAttribute *string
	Filter               *string
	DescriptionAttribute *string
	SubTree              *bool
	ForceAttributeSearch *bool
	Strategy             *string
}

// LDAPGroupInvalid refuses a group setting. Field is the Access API's name
// of the field it is about ("group_base_dn"), or "" for the setting as a
// whole. It matches kernel.ErrInvalid; its text reaches clients.
type LDAPGroupInvalid struct {
	// Name is the setting refused, which a descriptor names; the Access API
	// addresses one setting per request, so its message leaves it out.
	Name   string
	Field  string
	Reason string
}

func (e *LDAPGroupInvalid) Error() string {
	if e.Field == "" {
		return "the LDAP group setting " + e.Reason
	}
	return e.Field + " " + e.Reason
}

// Unwrap makes a refusal match kernel.ErrInvalid.
func (e *LDAPGroupInvalid) Unwrap() error { return kernel.ErrInvalid }

// LDAPGroupSettings reads and writes the LDAP group settings for the
// configuration descriptor, as LDAPSettings does the connections: each call
// joins the store transaction ctx carries, and a refusal is an
// *LDAPGroupInvalid. Results name the setting in Key.
type LDAPGroupSettings interface {
	// GroupSettings returns every group setting, by name.
	GroupSettings(ctx context.Context) ([]LDAPGroupSetting, error)
	// PlanGroups checks every change against the stored settings and returns
	// each one's outcome, in the order given, without writing.
	PlanGroups(ctx context.Context, changes []LDAPGroupChange) ([]LDAPResult, error)
	// ApplyGroups checks the changes as PlanGroups does and writes those that
	// change a setting.
	ApplyGroups(ctx context.Context, changes []LDAPGroupChange) ([]LDAPResult, error)
}
