package ldap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

var (
	// ErrGroupSettingNotFound reports a group setting that does not exist.
	ErrGroupSettingNotFound = errors.New("ldap: no LDAP group setting has that name")
	// ErrGroupSettingExists reports a create whose name is taken. It matches
	// kernel.ErrInvalid, as ErrExists does for connections.
	ErrGroupSettingExists = fmt.Errorf("ldap: an LDAP group setting with that name already exists: %w",
		kernel.ErrInvalid)
)

// The Access API's names of the group setting fields a refusal can name.
const (
	FieldGroupName        = "name"
	FieldGroupBaseDN      = "group_base_dn"
	FieldGroupNameAttr    = "group_name_attribute"
	FieldGroupMemberAttr  = "group_member_attribute"
	FieldGroupFilter      = "filter"
	FieldGroupDescription = "description_attribute"
	FieldGroupStrategy    = "strategy"
	FieldGroupEnabledLDAP = "enabled_ldap"
)

// groupDocument is the stored form of a group setting, with the revision
// that a sync and a login's publication recheck after directory I/O.
type groupDocument struct {
	Name                 string `json:"name"`
	EnabledLDAP          string `json:"enabled_ldap"`
	GroupBaseDN          string `json:"group_base_dn"`
	GroupNameAttribute   string `json:"group_name_attribute"`
	GroupMemberAttribute string `json:"group_member_attribute"`
	Filter               string `json:"filter"`
	DescriptionAttribute string `json:"description_attribute"`
	SubTree              bool   `json:"sub_tree"`
	ForceAttributeSearch bool   `json:"force_attribute_search"`
	Strategy             string `json:"strategy"`
	Revision             string `json:"revision"`
}

func (d groupDocument) setting() accessapi.LDAPGroupSetting {
	return accessapi.LDAPGroupSetting{
		Name: d.Name, EnabledLDAP: d.EnabledLDAP, GroupBaseDN: d.GroupBaseDN,
		GroupNameAttribute: d.GroupNameAttribute, GroupMemberAttribute: d.GroupMemberAttribute,
		Filter: d.Filter, DescriptionAttribute: d.DescriptionAttribute, SubTree: d.SubTree,
		ForceAttributeSearch: d.ForceAttributeSearch, Strategy: accessapi.LDAPGroupStrategy(d.Strategy),
	}
}

func toGroupDocument(g accessapi.LDAPGroupSetting, revision string) groupDocument {
	return groupDocument{
		Name: g.Name, EnabledLDAP: g.EnabledLDAP, GroupBaseDN: g.GroupBaseDN,
		GroupNameAttribute: g.GroupNameAttribute, GroupMemberAttribute: g.GroupMemberAttribute,
		Filter: g.Filter, DescriptionAttribute: g.DescriptionAttribute, SubTree: g.SubTree,
		ForceAttributeSearch: g.ForceAttributeSearch, Strategy: string(g.Strategy), Revision: revision,
	}
}

// GroupDefaults is a new group setting before its fields are applied: 7.x's
// LdapGroupSetting defaults, the cn name attribute, no base DN, a one-level
// search and the STATIC strategy. The artifactory_ldap_group_setting
// resources send sub_tree explicitly, so the descriptor's false default
// never reaches a setting they write.
func GroupDefaults(name string) accessapi.LDAPGroupSetting {
	return accessapi.LDAPGroupSetting{Name: name, GroupNameAttribute: "cn", Strategy: accessapi.LDAPStatic}
}

// MergeGroup applies a change's set fields to g. A strategy is validated
// afterwards, so an unknown one is carried to the refusal unchanged.
func MergeGroup(g accessapi.LDAPGroupSetting, ch accessapi.LDAPGroupChange) accessapi.LDAPGroupSetting {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&g.EnabledLDAP, ch.EnabledLDAP)
	set(&g.GroupBaseDN, ch.GroupBaseDN)
	set(&g.GroupNameAttribute, ch.GroupNameAttribute)
	set(&g.GroupMemberAttribute, ch.GroupMemberAttribute)
	set(&g.Filter, ch.Filter)
	set(&g.DescriptionAttribute, ch.DescriptionAttribute)
	if ch.SubTree != nil {
		g.SubTree = *ch.SubTree
	}
	if ch.ForceAttributeSearch != nil {
		g.ForceAttributeSearch = *ch.ForceAttributeSearch
	}
	if ch.Strategy != nil {
		g.Strategy = accessapi.LDAPGroupStrategy(*ch.Strategy)
	}
	return g
}

// memberAttribute is a group member attribute: an attribute name, optionally
// with an extensible match rule, such as member:1.2.840.113556.1.4.1941:,
// the rule JFrog's Entra ID page gives for nested groups. A STATIC search
// puts it before "=" in the filter, where the rule asks the directory itself
// to follow nesting.
var memberAttribute = regexp.MustCompile(
	`^(?:[A-Za-z][A-Za-z0-9-]*|[0-9]+(?:\.[0-9]+)+)(?:;[A-Za-z0-9-]+)*(?::[0-9]+(?:\.[0-9]+)+:)?$`)

func refuseGroup(field, reason string) error {
	return &accessapi.LDAPGroupInvalid{Field: field, Reason: reason}
}

// ValidateGroup checks a group setting as it will be stored or searched
// with. The rules are local engineering choices [I]: JFrog documents the
// fields but not their validation, and each refuses a value the directory
// client could never use.
func ValidateGroup(g accessapi.LDAPGroupSetting) error {
	switch {
	case g.Name == "":
		return refuseGroup(FieldGroupName, "is required")
	case strings.Contains(g.Name, "/"):
		// GET, DELETE and refresh address a setting by one path segment.
		return refuseGroup(FieldGroupName, "must not contain /")
	case strings.ContainsFunc(g.Name, unicode.IsControl):
		// The snapshot revision separates names with NUL.
		return refuseGroup(FieldGroupName, "must not contain control characters")
	case strings.ContainsFunc(g.EnabledLDAP, unicode.IsControl):
		return refuseGroup(FieldGroupEnabledLDAP, "must not contain control characters")
	}
	switch g.Strategy {
	case accessapi.LDAPStatic, accessapi.LDAPDynamic, accessapi.LDAPHierarchical:
	default:
		return refuseGroup(FieldGroupStrategy, "must be STATIC, DYNAMIC or HIERARCHICAL")
	}
	if g.GroupBaseDN != "" && !validDN(g.GroupBaseDN) {
		return refuseGroup(FieldGroupBaseDN, "must be a distinguished name such as ou=groups")
	}
	if !attributeName.MatchString(g.GroupNameAttribute) {
		return refuseGroup(FieldGroupNameAttr, "must be an LDAP attribute name such as cn")
	}
	if g.GroupMemberAttribute != "" && !memberAttribute.MatchString(g.GroupMemberAttribute) {
		return refuseGroup(FieldGroupMemberAttr, "must be an LDAP attribute name such as uniqueMember or memberOf")
	}
	if g.DescriptionAttribute != "" && !attributeName.MatchString(g.DescriptionAttribute) {
		return refuseGroup(FieldGroupDescription, "must be an LDAP attribute name such as description")
	}
	if g.Filter != "" && !balanced(g.Filter) {
		return refuseGroup(FieldGroupFilter, "must have balanced parentheses, for example (objectClass=groupOfNames)")
	}
	return nil
}

// storedGroup is a group setting as read, with its revision.
type storedGroup struct {
	setting  accessapi.LDAPGroupSetting
	revision string
}

func decodeGroup(d configdoc.ConfigDocument) (storedGroup, error) {
	var doc groupDocument
	if err := json.Unmarshal(d.Document, &doc); err != nil {
		return storedGroup{}, fmt.Errorf("ldap: group setting %s: %w", d.Key, kernel.ErrStore)
	}
	if doc.Name != d.Key {
		return storedGroup{}, fmt.Errorf("ldap: group setting %s names %s: %w", d.Key, doc.Name, kernel.ErrStore)
	}
	return storedGroup{setting: doc.setting(), revision: doc.Revision}, nil
}

func (s Service) loadGroup(ctx context.Context, name string) (storedGroup, bool, error) {
	d, err := s.Configs.Get(ctx, configdoc.ConfigLDAPGroup, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return storedGroup{}, false, nil
	}
	if err != nil {
		return storedGroup{}, false, fmt.Errorf("ldap: read group setting %s: %w", name, err)
	}
	st, err := decodeGroup(d)
	return st, err == nil, err
}

func (s Service) allGroups(ctx context.Context) ([]storedGroup, error) {
	docs, err := s.Configs.List(ctx, configdoc.ConfigLDAPGroup)
	if err != nil {
		return nil, fmt.Errorf("ldap: list group settings: %w", err)
	}
	out := make([]storedGroup, 0, len(docs))
	for _, d := range docs {
		st, err := decodeGroup(d)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// GroupSettings returns every group setting, by name.
func (s Service) GroupSettings(ctx context.Context) ([]accessapi.LDAPGroupSetting, error) {
	all, err := s.allGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]accessapi.LDAPGroupSetting, 0, len(all))
	for _, st := range all {
		out = append(out, st.setting)
	}
	return out, nil
}

// GroupSetting returns one group setting; ErrGroupSettingNotFound when there
// is none.
func (s Service) GroupSetting(ctx context.Context, name string) (accessapi.LDAPGroupSetting, error) {
	st, found, err := s.loadGroup(ctx, name)
	switch {
	case err != nil:
		return accessapi.LDAPGroupSetting{}, err
	case !found:
		return accessapi.LDAPGroupSetting{}, ErrGroupSettingNotFound
	}
	return st.setting, nil
}

// CreateGroupSetting stores a new group setting: GroupDefaults with the
// change's fields. A name in use is ErrGroupSettingExists.
func (s Service) CreateGroupSetting(
	ctx context.Context,
	ch accessapi.LDAPGroupChange,
) (accessapi.LDAPGroupSetting, error) {
	if _, found, err := s.loadGroup(ctx, ch.Name); err != nil {
		return accessapi.LDAPGroupSetting{}, err
	} else if found {
		return accessapi.LDAPGroupSetting{}, ErrGroupSettingExists
	}
	return s.writeGroup(ctx, MergeGroup(GroupDefaults(ch.Name), ch))
}

// ReplaceGroupSetting replaces a group setting with GroupDefaults and the
// change's fields, as the Access PUT does; ErrGroupSettingNotFound when
// there is none.
func (s Service) ReplaceGroupSetting(
	ctx context.Context,
	ch accessapi.LDAPGroupChange,
) (accessapi.LDAPGroupSetting, error) {
	if _, found, err := s.loadGroup(ctx, ch.Name); err != nil {
		return accessapi.LDAPGroupSetting{}, err
	} else if !found {
		return accessapi.LDAPGroupSetting{}, ErrGroupSettingNotFound
	}
	return s.writeGroup(ctx, MergeGroup(GroupDefaults(ch.Name), ch))
}

func (s Service) writeGroup(ctx context.Context, g accessapi.LDAPGroupSetting) (accessapi.LDAPGroupSetting, error) {
	if err := ValidateGroup(g); err != nil {
		return accessapi.LDAPGroupSetting{}, err
	}
	if err := s.putGroup(ctx, g); err != nil {
		return accessapi.LDAPGroupSetting{}, err
	}
	return g, nil
}

// putGroup stores g with a new revision, so a sync or login that read the
// setting before this write publishes nothing it computed from the old one.
func (s Service) putGroup(ctx context.Context, g accessapi.LDAPGroupSetting) error {
	revision, err := s.revision()
	if err != nil {
		return err
	}
	body, err := json.Marshal(toGroupDocument(g, revision))
	if err != nil {
		return fmt.Errorf("ldap: encode group setting %s: %w", g.Name, err)
	}
	if err := s.Configs.Put(ctx, configdoc.ConfigDocument{
		Kind: configdoc.ConfigLDAPGroup, Key: g.Name, Enabled: g.EnabledLDAP != "",
		Document: body, Updated: s.now(),
	}); err != nil {
		return fmt.Errorf("ldap: write group setting %s: %w", g.Name, err)
	}
	return nil
}

// DeleteGroupSetting removes a group setting; ErrGroupSettingNotFound when
// there is none. The groups imported through it stay, as local groups in
// the ldap realm (the Access API's delete).
func (s Service) DeleteGroupSetting(ctx context.Context, name string) error {
	err := s.Configs.Delete(ctx, configdoc.ConfigLDAPGroup, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return ErrGroupSettingNotFound
	}
	if err != nil {
		return fmt.Errorf("ldap: delete group setting %s: %w", name, err)
	}
	return nil
}

type plannedGroup struct {
	result  accessapi.LDAPResult
	setting accessapi.LDAPGroupSetting
}

// PlanGroups works out every descriptor change against the stored group
// settings, field by field: a change to a stored setting keeps what it
// omits, and one that creates a setting starts from GroupDefaults. Nothing
// is written.
func (s Service) PlanGroups(
	ctx context.Context,
	changes []accessapi.LDAPGroupChange,
) ([]accessapi.LDAPResult, error) {
	plans, err := s.planGroups(ctx, changes)
	if err != nil {
		return nil, err
	}
	out := make([]accessapi.LDAPResult, len(plans))
	for i, p := range plans {
		out[i] = p.result
	}
	return out, nil
}

func (s Service) planGroups(ctx context.Context, changes []accessapi.LDAPGroupChange) ([]plannedGroup, error) {
	out := make([]plannedGroup, 0, len(changes))
	for _, ch := range changes {
		cur, found, err := s.loadGroup(ctx, ch.Name)
		if err != nil {
			return nil, err
		}
		p := plannedGroup{result: accessapi.LDAPResult{Key: ch.Name}}
		if ch.Delete {
			if found {
				p.result.Outcome = accessapi.LDAPDeleted
			}
			out = append(out, p)
			continue
		}
		base := GroupDefaults(ch.Name)
		if found {
			base = cur.setting
		}
		p.setting = MergeGroup(base, ch)
		if err := ValidateGroup(p.setting); err != nil {
			var refused *accessapi.LDAPGroupInvalid
			if errors.As(err, &refused) {
				refused.Name = ch.Name
			}
			return nil, err
		}
		switch {
		case !found:
			p.result.Outcome = accessapi.LDAPCreated
		case p.setting != cur.setting:
			p.result.Outcome = accessapi.LDAPUpdated
		}
		out = append(out, p)
	}
	return out, nil
}

// ApplyGroups plans the changes as PlanGroups does and writes those that
// change a setting, so applying the same descriptor again writes nothing and
// keeps every revision.
func (s Service) ApplyGroups(
	ctx context.Context,
	changes []accessapi.LDAPGroupChange,
) ([]accessapi.LDAPResult, error) {
	plans, err := s.planGroups(ctx, changes)
	if err != nil {
		return nil, err
	}
	out := make([]accessapi.LDAPResult, len(plans))
	for i, p := range plans {
		out[i] = p.result
		switch p.result.Outcome {
		case accessapi.LDAPDeleted:
			err = s.DeleteGroupSetting(ctx, p.result.Key)
		case accessapi.LDAPCreated, accessapi.LDAPUpdated:
			err = s.putGroup(ctx, p.setting)
		case accessapi.LDAPUnchanged:
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// StrategyDefaults are the attribute names and filter a strategy starts
// from, the answer of GET /access/api/v1/ldap/groups/strategy: 7.x's
// GroupMappingStrategyService, which answers STATIC for a strategy it does
// not know, and so does this.
type StrategyDefaults struct {
	GroupKeyMember     string
	Filter             string
	GroupNameAttribute string
	Description        string
}

// DefaultsOf returns the defaults of strategy, matched without regard to
// case; "hierarchy" is the spelling 7.x's UI sends.
func DefaultsOf(strategy string) StrategyDefaults {
	switch strings.ToUpper(strategy) {
	case string(accessapi.LDAPDynamic):
		return StrategyDefaults{"memberOf", "(objectClass=group)", "cn", "description"}
	case string(accessapi.LDAPHierarchical), "HIERARCHY":
		return StrategyDefaults{"ou", "(objectClass=organizationalUnit)", "cn", ""}
	default:
		return StrategyDefaults{"uniqueMember", "(objectClass=groupOfNames)", "cn", "description"}
	}
}
