package accesshttp

import (
	"context"
	"errors"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The Access LDAP group settings (/access/api/v1/ldap/groups, LDAP-03,
// research 05 "LDAP groups"): their CRUD, the strategy defaults, and the
// search, import and refresh that ask the directory through Deps.LDAP.

// groupError maps the group services' errors as ldapError does the
// connections', with the group refusals and the directory's failures.
func (s AccessService) groupError(ctx context.Context, op string, err error) error {
	var (
		invalid *accessapi.LDAPGroupInvalid
		unknown *ldap.UnknownUserError
		failed  *ldap.DirectoryError
	)
	switch {
	case errors.As(err, &invalid):
		return httpkit.BadRequestf("%s", invalid.Error())
	case errors.As(err, &unknown):
		return httpkit.BadRequestf("%s", unknown.Error())
	case errors.As(err, &failed):
		// The directory's own answer, as the connection test reports it.
		return httpkit.BadRequestf("LDAP group %s failed: %s", op, failed.Error())
	case errors.Is(err, ldap.ErrGroupSettingExists):
		return httpkit.BadRequestf("an LDAP group setting with that name already exists")
	case errors.Is(err, ldap.ErrNoConnection):
		return httpkit.BadRequestf("enabled_ldap names no enabled LDAP connection")
	case errors.Is(err, ldap.ErrChanged):
		return httpkit.BadRequestf("the LDAP settings changed while the directory was asked; nothing was changed")
	case errors.Is(err, ldap.ErrGroupSettingNotFound):
		return apierror.New(http.StatusNotFound, "no LDAP group setting has that name")
	case errors.Is(err, ldap.ErrGroupNotInDirectory):
		return apierror.New(http.StatusNotFound, "no group of the LDAP group setting's directory has that name")
	default:
		return s.ldapError(ctx, op, err)
	}
}

// groupChange reads a request body. Every field it leaves out takes its
// default on a create or replace; an empty strategy is the default STATIC.
func groupChange(b accessgen.LdapGroupSettings) accessapi.LDAPGroupChange {
	ch := accessapi.LDAPGroupChange{
		EnabledLDAP: b.EnabledLdap, GroupBaseDN: b.GroupBaseDn, GroupNameAttribute: b.GroupNameAttribute,
		GroupMemberAttribute: b.GroupMemberAttribute, Filter: b.Filter, DescriptionAttribute: b.DescriptionAttribute,
		SubTree: b.SubTree, ForceAttributeSearch: b.ForceAttributeSearch, Strategy: b.Strategy,
	}
	if b.Name != nil {
		ch.Name = *b.Name
	}
	if ch.Strategy != nil && *ch.Strategy == "" {
		ch.Strategy = nil
	}
	return ch
}

// groupSetting is a search's or import's setting: the defaults with the
// body's fields, never stored.
func groupSetting(b *accessgen.LdapGroupSettings) accessapi.LDAPGroupSetting {
	if b == nil {
		return ldap.GroupDefaults("")
	}
	ch := groupChange(*b)
	return ldap.MergeGroup(ldap.GroupDefaults(ch.Name), ch)
}

// groupWire is a group setting as Access returns it: every field.
func groupWire(g accessapi.LDAPGroupSetting) accessgen.LdapGroupSettings {
	strategy := string(g.Strategy)
	return accessgen.LdapGroupSettings{
		Name: &g.Name, EnabledLdap: &g.EnabledLDAP, GroupBaseDn: &g.GroupBaseDN,
		GroupNameAttribute: &g.GroupNameAttribute, GroupMemberAttribute: &g.GroupMemberAttribute,
		Filter: &g.Filter, DescriptionAttribute: &g.DescriptionAttribute, SubTree: &g.SubTree,
		ForceAttributeSearch: &g.ForceAttributeSearch, Strategy: &strategy,
	}
}

// sync is the group sync over the request's store and directory, whose
// transactions recheck the caller's authority as ldapTx does.
func (s AccessService) sync() (ldap.Sync, error) {
	st, err := s.content()
	if err != nil {
		return ldap.Sync{}, err
	}
	return ldap.Sync{
		Settings:  ldap.Service{Configs: st.Configs(), Tx: st, Guard: st.Projects().Guard},
		Directory: s.deps.LDAP, Users: st.Users(), Groups: st.Groups(),
		Tx: func(ctx context.Context, fn func(context.Context) error) error {
			return s.ldapTx(ctx, func(ctx context.Context, _ ldap.Service) error { return fn(ctx) })
		},
	}, nil
}

// importGroup imports the directory group name names through the body's
// import_from_ldap_group_setting, for Create a Group (replace false) and
// Replace or Create a Group (replace true): the directory is asked outside
// any transaction, and the group is stored only while the settings it was
// found through are unchanged. It returns the group's details, under the
// directory's name in lower case, and whether it created the group.
func (s AccessService) importGroup(
	ctx context.Context,
	svc *identity.Service,
	name string,
	b accessgen.GroupCreateRequest,
	replace bool,
) (accessgen.GroupDetails, bool, error) {
	if err := s.directoryReady(); err != nil {
		return accessgen.GroupDetails{}, false, err
	}
	sync, err := s.sync()
	if err != nil {
		return accessgen.GroupDetails{}, false, err
	}
	n, err := sync.FindNamed(ctx, *b.ImportFromLdapGroupSetting, name)
	if err != nil {
		return accessgen.GroupDetails{}, false, s.groupError(ctx, "import", err)
	}
	g, err := groupFromCreate(n.Local, b)
	if err != nil {
		return accessgen.GroupDetails{}, false, err
	}
	g.Realm, g.RealmAttributes = model.RealmLDAP, n.RealmAttributes
	if b.Description == nil {
		// As the LDAP Groups screen imports it [I]: a description the
		// request sends is the request's, like every other field.
		g.Description = n.Description
	}
	var created bool
	err = sync.PublishNamed(ctx, n, func(ctx context.Context) (err error) {
		created, err = svc.ImportGroup(ctx, g, replace)
		return err //nolint:wrapcheck // mapped below
	})
	switch {
	case errors.Is(err, identity.ErrExists), errors.Is(err, identity.ErrInvalid):
		return accessgen.GroupDetails{}, false, s.identityErr(ctx, "import group", groupExists(n.Local), err)
	case err != nil:
		return accessgen.GroupDetails{}, false, s.groupError(ctx, "import", err)
	}
	d, err := s.groupDetails(ctx, svc, n.Local)
	return d, created, err
}

// GetAllLdapGroupSettings lists the group settings.
func (s AccessService) GetAllLdapGroupSettings(
	ctx context.Context,
	_ accessgen.GetAllLdapGroupSettingsRequestObject,
) (accessgen.GetAllLdapGroupSettingsResponseObject, error) {
	var all []accessapi.LDAPGroupSetting
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) (err error) {
		all, err = svc.GroupSettings(ctx)
		return err //nolint:wrapcheck // mapped by groupError
	}); err != nil {
		return nil, s.groupError(ctx, "list", err)
	}
	out := make(accessgen.GetAllLdapGroupSettings200JSONResponse, 0, len(all))
	for _, g := range all {
		out = append(out, groupWire(g))
	}
	return out, nil
}

// GetLdapGroupSettingsByName returns one group setting.
func (s AccessService) GetLdapGroupSettingsByName(
	ctx context.Context,
	req accessgen.GetLdapGroupSettingsByNameRequestObject,
) (accessgen.GetLdapGroupSettingsByNameResponseObject, error) {
	var g accessapi.LDAPGroupSetting
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) (err error) {
		g, err = svc.GroupSetting(ctx, req.Name)
		return err //nolint:wrapcheck // mapped by groupError
	}); err != nil {
		return nil, s.groupError(ctx, "read", err)
	}
	return accessgen.GetLdapGroupSettingsByName200JSONResponse(groupWire(g)), nil
}

// CreateLdapGroupSettings adds a group setting and answers 201, the status
// artifactory_ldap_group_setting_v2 requires.
func (s AccessService) CreateLdapGroupSettings(
	ctx context.Context,
	req accessgen.CreateLdapGroupSettingsRequestObject,
) (accessgen.CreateLdapGroupSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a body with the LDAP group setting is required")
	}
	b := req.Body
	ch := groupChange(accessgen.LdapGroupSettings{
		Name: &b.Name, EnabledLdap: &b.EnabledLdap, GroupBaseDn: b.GroupBaseDn,
		GroupNameAttribute: b.GroupNameAttribute, GroupMemberAttribute: b.GroupMemberAttribute, Filter: b.Filter,
		DescriptionAttribute: b.DescriptionAttribute, SubTree: b.SubTree, ForceAttributeSearch: b.ForceAttributeSearch,
		Strategy: b.Strategy,
	})
	var g accessapi.LDAPGroupSetting
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) (err error) {
		g, err = svc.CreateGroupSetting(ctx, ch)
		return err //nolint:wrapcheck // mapped by groupError
	}); err != nil {
		return nil, s.groupError(ctx, "create", err)
	}
	return accessgen.CreateLdapGroupSettings201JSONResponse(groupWire(g)), nil
}

// UpdateLdapGroupSettings replaces the group setting the body's name names.
func (s AccessService) UpdateLdapGroupSettings(
	ctx context.Context,
	req accessgen.UpdateLdapGroupSettingsRequestObject,
) (accessgen.UpdateLdapGroupSettingsResponseObject, error) {
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a body with the LDAP group setting is required")
	}
	ch := groupChange(*req.Body)
	if ch.Name == "" {
		return nil, httpkit.BadRequestf("name is required")
	}
	var g accessapi.LDAPGroupSetting
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) (err error) {
		g, err = svc.ReplaceGroupSetting(ctx, ch)
		return err //nolint:wrapcheck // mapped by groupError
	}); err != nil {
		return nil, s.groupError(ctx, "replace", err)
	}
	return accessgen.UpdateLdapGroupSettings200JSONResponse(groupWire(g)), nil
}

// DeleteLdapGroupSettings removes a group setting and answers 204; the
// groups imported through it stay.
func (s AccessService) DeleteLdapGroupSettings(
	ctx context.Context,
	req accessgen.DeleteLdapGroupSettingsRequestObject,
) (accessgen.DeleteLdapGroupSettingsResponseObject, error) {
	if err := s.ldapTx(ctx, func(ctx context.Context, svc ldap.Service) error {
		return svc.DeleteGroupSetting(ctx, req.Name) //nolint:wrapcheck // mapped by groupError
	}); err != nil {
		return nil, s.groupError(ctx, "delete", err)
	}
	return accessgen.DeleteLdapGroupSettings204Response{}, nil
}

// GetLdapGroupMappingStrategy returns a strategy's defaults. It reads
// nothing stored, but answers administrators alone, as every LDAP route.
func (s AccessService) GetLdapGroupMappingStrategy(
	ctx context.Context,
	req accessgen.GetLdapGroupMappingStrategyRequestObject,
) (accessgen.GetLdapGroupMappingStrategyResponseObject, error) {
	if err := s.ldapTx(ctx, func(context.Context, ldap.Service) error { return nil }); err != nil {
		return nil, s.groupError(ctx, "strategy", err)
	}
	strategy := ""
	if req.Params.Strategy != nil {
		strategy = *req.Params.Strategy
	}
	d := ldap.DefaultsOf(strategy)
	return accessgen.GetLdapGroupMappingStrategy200JSONResponse{
		GroupKeyMember: &d.GroupKeyMember, Filter: &d.Filter, GroupNameAttribute: &d.GroupNameAttribute,
		Description: &d.Description,
	}, nil
}

func (s AccessService) directoryReady() error {
	if s.deps.LDAP == nil {
		return apierror.New(http.StatusNotImplemented, "LDAP group synchronisation is not available")
	}
	return nil
}

// SearchLdapGroups runs the body's group search and stores nothing.
func (s AccessService) SearchLdapGroups(
	ctx context.Context,
	req accessgen.SearchLdapGroupsRequestObject,
) (accessgen.SearchLdapGroupsResponseObject, error) {
	if err := s.directoryReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpkit.BadRequestf("a body with the LDAP group setting is required")
	}
	g := groupSetting(req.Body)
	if g.Name == "" {
		// A search names no stored setting, so a body without a name is a
		// search all the same.
		g.Name = "search"
	}
	sync, err := s.sync()
	if err != nil {
		return nil, err
	}
	username := ""
	if req.Params.Username != nil {
		username = *req.Params.Username
	}
	found, err := sync.Search(ctx, g, username)
	if err != nil {
		return nil, s.groupError(ctx, "search", err)
	}
	out := make(accessgen.SearchLdapGroups200JSONResponse, 0, len(found))
	for _, f := range found {
		status := accessgen.LdapGroupRequiredUpdate(f.Status)
		out = append(out, accessgen.LdapGroup{
			GroupName: &f.Name, GroupDn: &f.DN, Description: &f.Description, RequiredUpdate: &status,
		})
	}
	return out, nil
}

// ImportLdapGroups creates or takes over the local groups the body names,
// all or none, and answers 204.
func (s AccessService) ImportLdapGroups(
	ctx context.Context,
	req accessgen.ImportLdapGroupsRequestObject,
) (accessgen.ImportLdapGroupsResponseObject, error) {
	if req.Body == nil || req.Body.ImportGroups == nil {
		return nil, httpkit.BadRequestf("a body with import_groups is required")
	}
	g := groupSetting(req.Body.LdapGroupSettings)
	if g.Name == "" {
		g.Name = "import"
	}
	if err := ldap.ValidateGroup(g); err != nil {
		return nil, s.groupError(ctx, "import", err)
	}
	groups := make([]ldap.ImportGroup, 0, len(*req.Body.ImportGroups))
	for _, ig := range *req.Body.ImportGroups {
		var g ldap.ImportGroup
		if ig.GroupName != nil {
			g.Name = *ig.GroupName
		}
		if ig.GroupDn != nil {
			g.DN = *ig.GroupDn
		}
		if ig.Description != nil {
			g.Description = *ig.Description
		}
		if ig.RequiredUpdate != nil {
			g.Status = ldap.GroupStatus(*ig.RequiredUpdate)
		}
		groups = append(groups, g)
	}
	sync, err := s.sync()
	if err != nil {
		return nil, err
	}
	if err := sync.Tx(ctx, func(ctx context.Context) error {
		_, err := sync.ImportIn(ctx, g.Strategy, groups)
		return err //nolint:wrapcheck // mapped by groupError
	}); err != nil {
		return nil, s.groupError(ctx, "import", err)
	}
	return accessgen.ImportLdapGroups204Response{}, nil
}

// RefreshLdapGroupSettings re-reads a stored setting's groups and answers
// 200 with a plain-text summary.
func (s AccessService) RefreshLdapGroupSettings(
	ctx context.Context,
	req accessgen.RefreshLdapGroupSettingsRequestObject,
) (accessgen.RefreshLdapGroupSettingsResponseObject, error) {
	if err := s.directoryReady(); err != nil {
		return nil, err
	}
	op := ldap.OpUpdateAndImport
	if req.Params.Operation != nil {
		op = ldap.Operation(*req.Params.Operation)
	}
	username := ""
	if req.Params.Username != nil {
		username = *req.Params.Username
	}
	sync, err := s.sync()
	if err != nil {
		return nil, err
	}
	sum, err := sync.Refresh(ctx, req.Name, op, username)
	if err != nil {
		return nil, s.groupError(ctx, "refresh", err)
	}
	return accessgen.RefreshLdapGroupSettings200TextResponse(sum.String()), nil
}
