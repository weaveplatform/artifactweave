package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	"github.com/weaveplatform/artifactweave/internal/service/identity"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Access v2 users and groups, the basic authentication switch, the password
// policy and the default token lifetime (issue #81). Users and groups go
// through the identity service the legacy security API uses, so both APIs
// read and write one record.

const (
	accessUsers  = "/access/api/v2/users/"
	accessGroups = "/access/api/v2/groups/"
	// defaultListLimit is a list page's size when the request names none.
	defaultListLimit = 1000
	// maxUsernameLength is the spec's bound on a username.
	maxUsernameLength = 255
)

func (s accessService) identity() (*identity.Service, error) {
	st, err := artifactoryService{cfg: s.cfg}.content()
	if err != nil {
		return nil, err
	}
	return &identity.Service{Store: st, Hash: s.cfg.PasswordHash}, nil
}

// adminIdentity checks the caller is an administrator, then returns the
// service.
func (s accessService) adminIdentity(ctx context.Context) (*identity.Service, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	return s.identity()
}

// readerIdentity checks the caller may read users and groups, then returns
// the service.
func (s accessService) readerIdentity(ctx context.Context) (*identity.Service, error) {
	return s.identityFor(ctx, "")
}

// identityFor returns the service when the caller may read users and
// groups, or self's record: administrators, and tokens whose system scope
// covers identities:r, as the permission evaluator decides (decision 0006).
func (s accessService) identityFor(ctx context.Context, self string) (*identity.Service, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // an apierror value
	}
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	if self != "" && p.Name == self {
		return svc, nil
	}
	a, err := accessFor(ctx, svc.Store, p)
	if err != nil {
		return nil, s.identityErr(ctx, "evaluate permissions", "", err)
	}
	if !a.d.System("identities", types.ScopeRead) {
		return nil, auth.ErrForbidden
	}
	return svc, nil
}

// identityErr maps the identity service's errors the way Access v2 documents
// them: a protected principal is 403, a taken name 409, and the rest as the
// legacy API maps them (400, 404 or 500).
func (s accessService) identityErr(ctx context.Context, op, notFound string, err error) error {
	switch {
	case errors.Is(err, identity.ErrProtected):
		return apierror.New(http.StatusForbidden, afterSentinels(err))
	case errors.Is(err, identity.ErrExists):
		return apierror.New(http.StatusConflict, notFound)
	}
	return artifactoryService{cfg: s.cfg}.identityErr(ctx, op, notFound, err)
}

// afterSentinels is an error's message without its sentinel prefixes.
func afterSentinels(err error) string {
	msg := err.Error()
	for _, prefix := range []string{identity.ErrInvalid.Error() + ": ", identity.ErrProtected.Error() + ": "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

func userExists(name string) string  { return fmt.Sprintf("User '%s' already exists", name) }
func groupExists(name string) string { return fmt.Sprintf("Group '%s' already exists", name) }

// --- Users -------------------------------------------------------------------

func (s accessService) GetUserList(
	ctx context.Context,
	req accessgen.GetUserListRequestObject,
) (accessgen.GetUserListResponseObject, error) {
	svc, err := s.readerIdentity(ctx)
	if err != nil {
		return nil, err
	}
	p := req.Params
	limit, err := listLimit(p.Limit)
	if err != nil {
		return nil, err
	}
	if err := projectsUnsupported(p.Role, p.ProjectKey); err != nil {
		return nil, err
	}
	if p.Status != nil && !slices.Contains(
		[]types.UserStatus{
			types.UserInvited,
			types.UserEnabled,
			types.UserDisabled,
			types.UserLocked,
		},
		types.UserStatus(*p.Status),
	) {
		return nil, badRequest(
			"status %q is not one of invited, enabled, disabled or locked",
			*p.Status,
		)
	}
	users, err := svc.Users(ctx)
	if err != nil {
		return nil, s.identityErr(ctx, "list users", "", err)
	}
	granted, err := s.grantedOn(ctx, svc, p.ResourceType, p.ResourceName)
	if err != nil {
		return nil, err
	}
	admins, err := adminGroups(ctx, svc)
	if err != nil {
		return nil, s.identityErr(ctx, "list users", "", err)
	}
	users = slices.DeleteFunc(users, func(u types.User) bool {
		switch {
		case p.Status != nil && statusOf(u) != accessgen.UserDetailsStatus(*p.Status):
			return true
		case p.Username != nil && !containsFold(u.Name, *p.Username):
			return true
		case p.OnlyAdmins != nil && *p.OnlyAdmins && !effectiveAdmin(u, admins):
			return true
		case granted != nil && !granted.user(u):
			return true
		}
		return false
	})
	pageItems, cursor, err := paginate(users, func(u types.User) string { return u.Name },
		deref(p.Cursor), limit, deref(p.DescendingOrder))
	if err != nil {
		return nil, err
	}
	out := accessgen.GetUserList200JSONResponse{Users: []struct {
		Realm    string `json:"realm"`
		Status   string `json:"status"`
		Uri      string `json:"uri"`
		Username string `json:"username"`
	}{}}
	base := baseurl.From(ctx) + accessUsers
	for _, u := range pageItems {
		out.Users = append(out.Users, struct {
			Realm    string `json:"realm"`
			Status   string `json:"status"`
			Uri      string `json:"uri"`
			Username string `json:"username"`
		}{
			Realm: realmOf(u.Realm), Status: string(statusOf(u)),
			Uri: base + url.PathEscape(u.Name), Username: u.Name,
		})
	}
	out.Cursor = cursor
	return out, nil
}

func (s accessService) CreateUser(
	ctx context.Context,
	req accessgen.CreateUserRequestObject,
) (accessgen.CreateUserResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil {
		return nil, badRequest("a body with the user is required")
	}
	// The spec stores usernames lower-case, as Artifactory does.
	name := strings.ToLower(b.Username)
	if name == "" || len(name) > maxUsernameLength {
		return nil, badRequest("username must be 1 to %d characters", maxUsernameLength)
	}
	u := types.User{
		Name:             name,
		Email:            string(b.Email),
		Admin:            deref(b.Admin),
		ProfileUpdatable: derefOr(b.ProfileUpdatable, true),
		DisableUIAccess: deref(
			b.DisableUiAccess,
		),
		InternalPasswordDisabled: deref(b.InternalPasswordDisabled),
		Groups:                   derefSlice(b.Groups),
		Roles: types.RoleFlags{
			WatchManager: deref(b.WatchManager), PolicyViewer: deref(b.PolicyViewer),
			PolicyManager: deref(b.PolicyManager), ReportsManager: deref(b.ReportsManager),
			ResourcesManager: deref(b.ResourcesManager), ManageWebhook: deref(b.ManageWebhook),
			PlatformAuditor: deref(b.PlatformAuditor),
		},
	}
	if err := svc.CreateUser(ctx, u, deref(b.Password)); err != nil {
		return nil, s.identityErr(ctx, "create user", userExists(name), err)
	}
	d, err := s.userDetails(ctx, svc, name)
	if err != nil {
		return nil, err
	}
	return accessgen.CreateUser201JSONResponse(d), nil
}

func (s accessService) GetUserDetails(
	ctx context.Context,
	req accessgen.GetUserDetailsRequestObject,
) (accessgen.GetUserDetailsResponseObject, error) {
	name := string(req.Username)
	// A user may always read their own record.
	svc, err := s.identityFor(ctx, name)
	if err != nil {
		return nil, err
	}
	d, err := s.userDetails(ctx, svc, name)
	if err != nil {
		return nil, err
	}
	return accessgen.GetUserDetails200JSONResponse(d), nil
}

func (s accessService) UpdateUser(
	ctx context.Context,
	req accessgen.UpdateUserRequestObject,
) (accessgen.UpdateUserResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Username)
	b := req.Body
	if b == nil {
		b = &accessgen.UpdateUserJSONRequestBody{}
	}
	patch := identity.UserPatch{
		Password: b.Password, Admin: b.Admin, ProfileUpdatable: b.ProfileUpdatable,
		DisableUIAccess: b.DisableUiAccess, InternalPasswordDisabled: b.InternalPasswordDisabled,
		PolicyViewer: b.PolicyViewer, PolicyManager: b.PolicyManager, WatchManager: b.WatchManager,
		ReportsManager: b.ReportsManager, ResourcesManager: b.ResourcesManager,
		ManageWebhook: b.ManageWebhook, PlatformAuditor: b.PlatformAuditor,
	}
	if b.Email != nil {
		e := string(*b.Email)
		patch.Email = &e
	}
	// Turning the internal password back on needs a password to log in with.
	if b.InternalPasswordDisabled != nil && !*b.InternalPasswordDisabled && b.Password == nil {
		u, err := svc.User(ctx, name)
		if err != nil {
			return nil, s.identityErr(ctx, "update user", userNotFound(name), err)
		}
		if len(u.PasswordHash) == 0 {
			return nil, badRequest("a password is required to enable the internal password")
		}
	}
	if err := svc.PatchUser(ctx, name, patch); err != nil {
		return nil, s.identityErr(ctx, "update user", userNotFound(name), err)
	}
	d, err := s.userDetails(ctx, svc, name)
	if err != nil {
		return nil, err
	}
	return accessgen.UpdateUser200JSONResponse(d), nil
}

func (s accessService) DeleteUser(
	ctx context.Context,
	req accessgen.DeleteUserRequestObject,
) (accessgen.DeleteUserResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Username)
	if err := svc.DeleteUser(ctx, name); err != nil {
		return nil, s.identityErr(ctx, "delete user", userNotFound(name), err)
	}
	return accessgen.DeleteUser204Response{}, nil
}

func (s accessService) UpdateUserGroups(
	ctx context.Context,
	req accessgen.UpdateUserGroupsRequestObject,
) (accessgen.UpdateUserGroupsResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Username)
	var add, remove []string
	if req.Body != nil {
		add, remove = derefSlice(req.Body.Add), derefSlice(req.Body.Remove)
	}
	groups, err := svc.UpdateUserGroups(ctx, name, add, remove)
	if err != nil {
		return nil, s.identityErr(ctx, "update user groups", userNotFound(name), err)
	}
	return accessgen.UpdateUserGroups200JSONResponse{Groups: nonNil(groups)}, nil
}

func (s accessService) ChangeUserPassword(
	ctx context.Context,
	req accessgen.ChangeUserPasswordRequestObject,
) (accessgen.ChangeUserPasswordResponseObject, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // an apierror value
	}
	name := string(req.Username)
	// An administrator sets anyone's password; any user sets their own, even
	// with an expired password (the middleware lets that one request through),
	// but not through a narrowed token (narrowedToken).
	if !p.Admin && (p.Name != name || narrowedToken(p)) {
		return nil, auth.ErrForbidden
	}
	if req.Body == nil || req.Body.Password == nil || *req.Body.Password == "" {
		return nil, badRequest("a body with the new password is required")
	}
	svc, err := s.identity()
	if err != nil {
		return nil, err
	}
	if err := svc.SetPassword(ctx, name, *req.Body.Password); err != nil {
		return nil, s.identityErr(ctx, "change password", userNotFound(name), err)
	}
	return accessgen.ChangeUserPassword204Response{}, nil
}

func (s accessService) ExpireUserPassword(
	ctx context.Context,
	req accessgen.ExpireUserPasswordRequestObject,
) (accessgen.ExpireUserPasswordResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Username)
	unknown, err := svc.ExpirePasswords(ctx, []string{name})
	if err != nil {
		return nil, s.identityErr(ctx, "expire password", userNotFound(name), err)
	}
	if len(unknown) > 0 {
		return nil, apierror.New(http.StatusNotFound, userNotFound(name))
	}
	return accessgen.ExpireUserPassword204Response{}, nil
}

func (s accessService) ExpirePasswordForAllUsers(
	ctx context.Context,
	_ accessgen.ExpirePasswordForAllUsersRequestObject,
) (accessgen.ExpirePasswordForAllUsersResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.ExpireAllPasswords(ctx); err != nil {
		return nil, s.identityErr(ctx, "expire passwords", "", err)
	}
	return accessgen.ExpirePasswordForAllUsers200TextResponse("All passwords were expired"), nil
}

func (s accessService) UnexpirePasswordForAllUsers(
	ctx context.Context,
	_ accessgen.UnexpirePasswordForAllUsersRequestObject,
) (accessgen.UnexpirePasswordForAllUsersResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.UnexpireAllPasswords(ctx); err != nil {
		return nil, s.identityErr(ctx, "unexpire passwords", "", err)
	}
	return accessgen.UnexpirePasswordForAllUsers200TextResponse(
		"All passwords were un-expired",
	), nil
}

func (s accessService) UnlockUser(
	ctx context.Context,
	req accessgen.UnlockUserRequestObject,
) (accessgen.UnlockUserResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Username)
	// Unlocking an unlocked user succeeds; an unknown one is 404.
	if _, err := svc.User(ctx, name); err != nil {
		return nil, s.identityErr(ctx, "unlock user", userNotFound(name), err)
	}
	if err := svc.Unlock(ctx, []string{name}); err != nil {
		return nil, s.identityErr(ctx, "unlock user", userNotFound(name), err)
	}
	return accessgen.UnlockUser204Response{}, nil
}

// userDetails reads a user in the Access v2 shape.
func (s accessService) userDetails(
	ctx context.Context,
	svc *identity.Service,
	name string,
) (accessgen.UserDetails, error) {
	u, err := svc.User(ctx, name)
	if err != nil {
		return accessgen.UserDetails{}, s.identityErr(ctx, "read user", userNotFound(name), err)
	}
	admins, err := adminGroups(ctx, svc)
	if err != nil {
		return accessgen.UserDetails{}, s.identityErr(ctx, "read user", "", err)
	}
	r := u.Roles
	d := accessgen.UserDetails{
		Username:                 u.Name,
		Email:                    u.Email,
		Admin:                    u.Admin,
		EffectiveAdmin:           effectiveAdmin(u, admins),
		DisableUiAccess:          u.DisableUIAccess,
		InternalPasswordDisabled: u.InternalPasswordDisabled,
		ProfileUpdatable:         u.ProfileUpdatable,
		Realm:                    realmOf(u.Realm),
		Status:                   statusOf(u),
		Groups:                   nonNil(u.Groups),
		WatchManager:             &r.WatchManager,
		PolicyViewer:             &r.PolicyViewer,
		PolicyManager:            &r.PolicyManager,
		ReportsManager:           &r.ReportsManager,
		ResourcesManager:         &r.ResourcesManager,
		ManageWebhook:            &r.ManageWebhook,
		PlatformAuditor:          &r.PlatformAuditor,
	}
	if !u.LastLoggedIn.IsZero() {
		t := u.LastLoggedIn
		d.LastLoggedIn = &t
	}
	return d, nil
}

// --- Groups ------------------------------------------------------------------

func (s accessService) GetGroupList(
	ctx context.Context,
	req accessgen.GetGroupListRequestObject,
) (accessgen.GetGroupListResponseObject, error) {
	svc, err := s.readerIdentity(ctx)
	if err != nil {
		return nil, err
	}
	p := req.Params
	limit, err := listLimit(p.Limit)
	if err != nil {
		return nil, err
	}
	if err := projectsUnsupported(p.Role, p.ProjectKey); err != nil {
		return nil, err
	}
	groups, err := svc.Groups(ctx)
	if err != nil {
		return nil, s.identityErr(ctx, "list groups", "", err)
	}
	granted, err := s.grantedOn(ctx, svc, p.ResourceType, p.ResourceName)
	if err != nil {
		return nil, err
	}
	groups = slices.DeleteFunc(groups, func(g types.Group) bool {
		return (p.GroupName != nil && !containsFold(g.Name, *p.GroupName)) ||
			(granted != nil && !granted.groups[g.Name])
	})
	pageItems, cursor, err := paginate(groups, func(g types.Group) string { return g.Name },
		deref(p.Cursor), limit, deref(p.DescendingOrder))
	if err != nil {
		return nil, err
	}
	out := accessgen.GetGroupList200JSONResponse{
		Groups: []accessgen.GroupRecordResponse{},
		Cursor: cursor,
	}
	base := baseurl.From(ctx) + accessGroups
	for _, g := range pageItems {
		out.Groups = append(out.Groups, accessgen.GroupRecordResponse{
			GroupName: g.Name, Uri: base + url.PathEscape(g.Name),
		})
	}
	return out, nil
}

func (s accessService) CreateGroup(
	ctx context.Context,
	req accessgen.CreateGroupRequestObject,
) (accessgen.CreateGroupResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil || b.Name == "" {
		return nil, badRequest("a body with the group name is required")
	}
	if b.ImportFromLdapGroupSetting != nil {
		if b.Members != nil {
			return nil, badRequest(
				"members and import_from_ldap_group_setting are mutually exclusive",
			)
		}
		return nil, badRequest("LDAP group import is not available in artifactweave")
	}
	g := types.Group{
		Name: b.Name, Description: deref(b.Description), AutoJoin: deref(b.AutoJoin),
		AdminPrivileges: deref(b.AdminPrivileges), ExternalID: deref(b.ExternalId),
		Roles: types.RoleFlags{
			WatchManager: deref(b.WatchManager), PolicyViewer: deref(b.PolicyViewer),
			PolicyManager: deref(b.PolicyManager), ReportsManager: deref(b.ReportsManager),
			ResourcesManager: deref(b.ManageResources), ManageWebhook: deref(b.ManageWebhook),
		},
	}
	if err := svc.CreateGroup(ctx, g, derefSlice(b.Members)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apierror.New(
				http.StatusNotFound,
				"One or more users in members were not found",
			)
		}
		return nil, s.identityErr(ctx, "create group", groupExists(b.Name), err)
	}
	d, err := s.groupDetails(ctx, svc, b.Name)
	if err != nil {
		return nil, err
	}
	return accessgen.CreateGroup201JSONResponse(d), nil
}

func (s accessService) GetGroupDetails(
	ctx context.Context,
	req accessgen.GetGroupDetailsRequestObject,
) (accessgen.GetGroupDetailsResponseObject, error) {
	svc, err := s.readerIdentity(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.groupDetails(ctx, svc, string(req.Name))
	if err != nil {
		return nil, err
	}
	return accessgen.GetGroupDetails200JSONResponse(d), nil
}

func (s accessService) UpdateGroup(
	ctx context.Context,
	req accessgen.UpdateGroupRequestObject,
) (accessgen.UpdateGroupResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Name)
	g, err := svc.Group(ctx, name)
	if err != nil {
		return nil, s.identityErr(ctx, "update group", groupNotFound(name), err)
	}
	if err := fixedGroupFields(rawBody(ctx), g); err != nil {
		return nil, err
	}
	b := req.Body
	if b == nil {
		b = &accessgen.UpdateGroupJSONRequestBody{}
	}
	patch := identity.GroupPatch{
		Description:      b.Description,
		AutoJoin:         b.AutoJoin,
		AdminPrivileges:  b.AdminPrivileges,
		WatchManager:     b.WatchManager,
		PolicyManager:    b.PolicyManager,
		ReportsManager:   b.ReportsManager,
		ExternalID:       b.ExternalId,
		PolicyViewer:     b.PolicyViewer,
		ResourcesManager: b.ManageResources,
		ManageWebhook:    b.ManageWebhook,
	}
	if err := svc.PatchGroup(ctx, name, patch); err != nil {
		return nil, s.identityErr(ctx, "update group", groupNotFound(name), err)
	}
	d, err := s.groupDetails(ctx, svc, name)
	if err != nil {
		return nil, err
	}
	return accessgen.UpdateGroup200JSONResponse(d), nil
}

// fixedGroupFields refuses a group update that changes the name, realm or
// realm attributes, which JFrog's update request does not carry and Access
// does not change. One sent with its stored value is accepted, and members
// are ignored, since they have their own endpoint: platform_group PATCHes
// its whole model.
func fixedGroupFields(raw []byte, g types.Group) error {
	if len(raw) == 0 {
		return nil
	}
	var sent struct {
		Name            *string `json:"name"`
		Realm           *string `json:"realm"`
		RealmAttributes *string `json:"realm_attributes"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		return badRequest("the body is not a group update: %v", err)
	}
	switch {
	case sent.Name != nil && *sent.Name != g.Name:
		return badRequest("name cannot be changed")
	case sent.Realm != nil && *sent.Realm != realmOf(g.Realm):
		return badRequest("realm cannot be changed")
	case sent.RealmAttributes != nil && *sent.RealmAttributes != g.RealmAttributes:
		return badRequest("realm_attributes cannot be changed")
	}
	return nil
}

func (s accessService) DeleteGroup(
	ctx context.Context,
	req accessgen.DeleteGroupRequestObject,
) (accessgen.DeleteGroupResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Name)
	if err := svc.DeleteGroup(ctx, name); err != nil {
		return nil, s.identityErr(ctx, "delete group", groupNotFound(name), err)
	}
	return accessgen.DeleteGroup204Response{}, nil
}

func (s accessService) UpdateGroupMembers(
	ctx context.Context,
	req accessgen.UpdateGroupMembersRequestObject,
) (accessgen.UpdateGroupMembersResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := string(req.Name)
	var add, remove []string
	if req.Body != nil {
		add, remove = derefSlice(req.Body.Add), derefSlice(req.Body.Remove)
	}
	members, err := svc.UpdateGroupMembers(ctx, name, add, remove)
	if errors.Is(err, store.ErrNotFound) {
		// The group was found first, so a miss names an unknown user.
		if _, gerr := svc.Group(ctx, name); gerr == nil {
			return nil, apierror.New(http.StatusNotFound, "A user to add does not exist")
		}
	}
	if err != nil {
		return nil, s.identityErr(ctx, "update group members", groupNotFound(name), err)
	}
	return accessgen.UpdateGroupMembers200JSONResponse{Members: nonNil(members)}, nil
}

// groupDetails reads a group in the Access v2 shape.
func (s accessService) groupDetails(
	ctx context.Context,
	svc *identity.Service,
	name string,
) (accessgen.GroupDetails, error) {
	g, err := svc.Group(ctx, name)
	if err != nil {
		return accessgen.GroupDetails{}, s.identityErr(ctx, "read group", groupNotFound(name), err)
	}
	r := g.Roles
	d := accessgen.GroupDetails{
		Name:            g.Name,
		AutoJoin:        g.AutoJoin,
		AdminPrivileges: g.AdminPrivileges,
		Members:         nonNil(g.Members),
		Realm:           realmOf(g.Realm),
		WatchManager:    &r.WatchManager,
		PolicyViewer:    &r.PolicyViewer,
		PolicyManager:   &r.PolicyManager,
		ReportsManager:  &r.ReportsManager,
		ManageResources: &r.ResourcesManager,
		ManageWebhook:   &r.ManageWebhook,
	}
	if g.Description != "" {
		d.Description = &g.Description
	}
	if g.ExternalID != "" {
		d.ExternalId = &g.ExternalID
	}
	if g.RealmAttributes != "" {
		d.RealmAttributes = &g.RealmAttributes
	}
	return d, nil
}

// --- Configuration -------------------------------------------------------------

func (s accessService) GetBasicAuthenticationStatus(
	ctx context.Context,
	_ accessgen.GetBasicAuthenticationStatusRequestObject,
) (accessgen.GetBasicAuthenticationStatusResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	on, err := auth.BasicAuthEnabled(ctx, svc.Store.Settings())
	if err != nil {
		return nil, s.identityErr(ctx, "read basic authentication switch", "", err)
	}
	return accessgen.GetBasicAuthenticationStatus200JSONResponse{Enabled: &on}, nil
}

// SetBasicAuthenticationStatus switches passwords over HTTP Basic on or
// off. Tokens keep working either way, which is how an administrator turns
// it back on.
func (s accessService) SetBasicAuthenticationStatus(
	ctx context.Context,
	req accessgen.SetBasicAuthenticationStatusRequestObject,
) (accessgen.SetBasicAuthenticationStatusResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("a body with enabled is required")
	}
	if err := svc.Store.Settings().
		Put(ctx, types.SettingBasicAuthEnabled, req.Body.Enabled); err != nil {
		return nil, s.identityErr(ctx, "set basic authentication switch", "", err)
	}
	s.forgetPolicies()
	return accessgen.SetBasicAuthenticationStatus200Response{}, nil
}

// CreatePasswordPolicy replaces the whole policy: an omitted field returns
// to its default, as JFrog documents for this PUT.
func (s accessService) CreatePasswordPolicy(
	ctx context.Context,
	req accessgen.CreatePasswordPolicyRequestObject,
) (accessgen.CreatePasswordPolicyResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	p := types.DefaultPasswordPolicy()
	if req.Body != nil {
		p = mergePolicy(p, *req.Body)
	}
	if err := svc.SetPasswordPolicy(ctx, p); err != nil {
		return nil, s.identityErr(ctx, "set password policy", "", err)
	}
	return accessgen.CreatePasswordPolicy204Response{}, nil
}

// UpdatePasswordPolicy merges the fields sent into the stored policy.
func (s accessService) UpdatePasswordPolicy(
	ctx context.Context,
	req accessgen.UpdatePasswordPolicyRequestObject,
) (accessgen.UpdatePasswordPolicyResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	p, err := svc.PasswordPolicy(ctx)
	if err != nil {
		return nil, s.identityErr(ctx, "read password policy", "", err)
	}
	if req.Body != nil {
		p = mergePolicy(p, *req.Body)
	}
	if err := svc.SetPasswordPolicy(ctx, p); err != nil {
		return nil, s.identityErr(ctx, "set password policy", "", err)
	}
	return accessgen.UpdatePasswordPolicy204Response{}, nil
}

func mergePolicy(p types.PasswordPolicy, b accessgen.PasswordPolicy) types.PasswordPolicy {
	setIf(&p.Uppercase, b.Uppercase)
	setIf(&p.Lowercase, b.Lowercase)
	setIf(&p.Digit, b.Digit)
	setIf(&p.SpecialChar, b.SpecialChar)
	setIf(&p.Length, b.Length)
	setIf(&p.NotMatchOld, b.NotMatchOld)
	return p
}

// GetAccessUserLockPolicy returns the Access view of the state that
// /artifactory/api/security/userLockPolicy and the password expiration
// policy also read.
func (s accessService) GetAccessUserLockPolicy(
	ctx context.Context,
	_ accessgen.GetAccessUserLockPolicyRequestObject,
) (accessgen.GetAccessUserLockPolicyResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	p, err := svc.AccessLockPolicy(ctx)
	if err != nil {
		return nil, s.identityErr(ctx, "read user lock policy", "", err)
	}
	return accessgen.GetAccessUserLockPolicy200JSONResponse(lockPolicyDoc(p)), nil
}

// ConfigureAccessUserLockPolicy replaces the policy. An omitted field is
// stored as 0 or false, not as its default: Access's request model has plain
// numbers and booleans, and JFrog documents the consequences (omitting
// attempts turns locking off).
func (s accessService) ConfigureAccessUserLockPolicy(
	ctx context.Context,
	req accessgen.ConfigureAccessUserLockPolicyRequestObject,
) (accessgen.ConfigureAccessUserLockPolicyResponseObject, error) {
	svc, err := s.adminIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("a user lock policy is required")
	}
	b := req.Body
	stored, err := svc.SetAccessLockPolicy(ctx, identity.AccessLockPolicy{
		Attempts:           derefOr(b.Attempts, 0),
		PasswordExpiryDays: derefOr(b.PasswordExpiryDays, 0),
		AccessLockSettings: types.AccessLockSettings{
			SecondsToUnlock:                derefOr(b.SecondsToUnlock, 0),
			AdminPasswordExpirable:         derefOr(b.AdminPasswordExpirable, false),
			MaxLoginDelayIncorrectAttempts: derefOr(b.MaxLoginDelayIncorrectAttempts, 0),
			MaxLoginDelayMillis:            derefOr(b.MaxLoginDelayMillis, 0),
			BaseLoginDelayMillis:           derefOr(b.BaseLoginDelayMillis, 0),
			ClientLoginSuspensionEnabled:   derefOr(b.ClientLoginSuspensionEnabled, false),
		},
	})
	if err != nil {
		return nil, s.identityErr(ctx, "set user lock policy", "", err)
	}
	s.forgetPolicies()
	return accessgen.ConfigureAccessUserLockPolicy200JSONResponse(lockPolicyDoc(stored)), nil
}

// lockPolicyDoc sets every field, so a 0 or false is sent rather than
// omitted.
func lockPolicyDoc(p identity.AccessLockPolicy) accessgen.UserLockPolicyConfig {
	return accessgen.UserLockPolicyConfig{
		Attempts:                       &p.Attempts,
		SecondsToUnlock:                &p.SecondsToUnlock,
		PasswordExpiryDays:             &p.PasswordExpiryDays,
		AdminPasswordExpirable:         &p.AdminPasswordExpirable,
		MaxLoginDelayIncorrectAttempts: &p.MaxLoginDelayIncorrectAttempts,
		MaxLoginDelayMillis:            &p.MaxLoginDelayMillis,
		BaseLoginDelayMillis:           &p.BaseLoginDelayMillis,
		ClientLoginSuspensionEnabled:   &p.ClientLoginSuspensionEnabled,
	}
}

// forgetPolicies makes this instance's next login read the switch and the
// policies again; other instances follow within the authenticator's cache
// lifetime.
func (s accessService) forgetPolicies() {
	if s.cfg.Auth != nil {
		s.cfg.Auth.ForgetPolicies()
	}
}

// --- Helpers -------------------------------------------------------------------

// grants is who a permission grants on one resource.
type grants struct {
	users  map[string]bool
	groups map[string]bool
}

func (g *grants) user(u types.User) bool {
	if g.users[u.Name] {
		return true
	}
	return slices.ContainsFunc(u.Groups, func(name string) bool { return g.groups[name] })
}

// grantedOn returns who holds any action on the resource that resourceType
// and resourceName name, or nil when the request filters on no resource
// (one of the two missing).
func (s accessService) grantedOn(
	ctx context.Context,
	svc *identity.Service,
	resourceType, resourceName *string,
) (*grants, error) {
	if resourceType == nil || resourceName == nil {
		return nil, nil //nolint:nilnil // no filter
	}
	rt := types.ResourceType(*resourceType)
	switch rt {
	case types.ResourceArtifact, types.ResourceBuild, types.ResourceReleaseBundle,
		types.ResourceDestination, types.ResourcePipelineSource:
	default:
		return nil, badRequest("resourceType %q is not a permission resource type", *resourceType)
	}
	perms, err := svc.Store.Permissions().WithTarget(ctx, *resourceName)
	if err != nil {
		return nil, s.identityErr(ctx, "read permissions", "", err)
	}
	out := &grants{users: map[string]bool{}, groups: map[string]bool{}}
	for _, p := range perms {
		r, ok := p.Resources[rt]
		if !ok {
			continue
		}
		if _, named := r.Targets[*resourceName]; !named {
			continue
		}
		for u, a := range r.Actions.Users {
			out.users[u] = out.users[u] || a != 0
		}
		for g, a := range r.Actions.Groups {
			out.groups[g] = out.groups[g] || a != 0
		}
	}
	return out, nil
}

// projectsUnsupported refuses the role and project filters: roles are held
// within projects, which artifactweave does not serve yet.
func projectsUnsupported(role, projectKey *string) error {
	if role != nil || projectKey != nil {
		return badRequest(
			"filtering by role or projectKey needs projects, which artifactweave does not serve yet",
		)
	}
	return nil
}

func listLimit(limit *int) (int, error) {
	if limit == nil {
		return defaultListLimit, nil
	}
	if *limit < 1 {
		return 0, badRequest("limit must be at least 1")
	}
	return *limit, nil
}

// paginate returns the page of items after cursor, ordered by name, and the
// cursor of the next page when more remain. A cursor is the last name of
// the previous page, encoded so clients treat it as opaque.
func paginate[T any](
	items []T,
	name func(T) string,
	cursor string,
	limit int,
	desc bool,
) ([]T, *string, error) {
	if desc {
		slices.Reverse(items)
	}
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, nil, badRequest("cursor is not valid")
		}
		after := string(b)
		i := slices.IndexFunc(items, func(t T) bool {
			if desc {
				return name(t) < after
			}
			return name(t) > after
		})
		if i < 0 {
			i = len(items)
		}
		items = items[i:]
	}
	if len(items) <= limit {
		return items, nil, nil
	}
	items = items[:limit]
	next := base64.RawURLEncoding.EncodeToString([]byte(name(items[len(items)-1])))
	return items, &next, nil
}

// adminGroups returns the names of groups whose members are admins.
func adminGroups(ctx context.Context, svc *identity.Service) (map[string]bool, error) {
	groups, err := svc.Groups(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller maps it with identityErr
	}
	out := map[string]bool{}
	for _, g := range groups {
		if g.AdminPrivileges {
			out[g.Name] = true
		}
	}
	return out, nil
}

// effectiveAdmin is the admin flag or membership of an admin group.
func effectiveAdmin(u types.User, adminGroups map[string]bool) bool {
	return u.Admin || slices.ContainsFunc(u.Groups, func(g string) bool { return adminGroups[g] })
}

func statusOf(u types.User) accessgen.UserDetailsStatus {
	if u.Status == "" {
		return accessgen.UserDetailsStatusEnabled
	}
	return accessgen.UserDetailsStatus(u.Status)
}

func realmOf(realm string) string {
	if realm == "" {
		return types.RealmInternal
	}
	return realm
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func derefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func derefSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}
