package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/identity"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// The legacy security API (/api/security/users, groups, locks, policies and
// the retired API keys; decision 0005, research 05).

const (
	securityUsers  = "/artifactory/api/security/users/"
	securityGroups = "/artifactory/api/security/groups/"
	// errAPIKeysRetired is the 400 for API-key creation: Artifactory
	// disables it by default since 7.98 and the keys reached end of life.
	errAPIKeysRetired = "API key creation is disabled"
	infoNoAPIKeys     = "No API key was revoked: API keys are retired, use access tokens" //nolint:gosec // a message, not a credential
)

func (s artifactoryService) identity(st store.Store) *identity.Service {
	return &identity.Service{Store: st, Hash: s.cfg.PasswordHash}
}

// identityErr maps the identity service's errors: ErrInvalid is a 400 with
// its message, ErrNotFound a 404 with notFound, anything else a 500.
func (s artifactoryService) identityErr(ctx context.Context, op, notFound string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, identity.ErrInvalid):
		// The message is what follows the sentinel, whatever wraps it.
		msg := err.Error()
		if _, after, ok := strings.Cut(msg, identity.ErrInvalid.Error()+": "); ok {
			msg = after
		}
		return badRequestf("%s", msg)
	case errors.Is(err, store.ErrNotFound) && notFound != "":
		return apierror.New(http.StatusNotFound, notFound)
	default:
		return s.internalError(ctx, op, err)
	}
}

// plusPrefixes are the security API paths whose user, group and permission
// names take "+" for a space, as artifactory.security.api.plus.insteadof.space,
// true by default, makes them (research 05): the legacy users, groups and
// permissions and the v2 permission targets, sub-paths included.
var plusPrefixes = []string{securityUsers, securityGroups, permV1Path, permV2Path}

// plusAsSpace rewrites an unescaped "+" in those paths to an escaped space
// before chi routes the request, so the generated binders decode it as one.
// A plus sent as %2B is not a "+" in the escaped path and stays a plus,
// which keeps names that contain one reachable. It must run before routing:
// chi takes the path parameters from the escaped path as it matches.
func plusAsSpace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped := r.URL.EscapedPath()
		for _, prefix := range plusPrefixes {
			rest, ok := strings.CutPrefix(escaped, prefix)
			if !ok || !strings.Contains(rest, "+") {
				continue
			}
			raw := prefix + strings.ReplaceAll(rest, "+", "%20")
			// EscapedPath came from a parsed URL, so it unescapes.
			path, _ := url.PathUnescape(raw)
			r.URL.Path, r.URL.RawPath = path, raw
			break
		}
		next.ServeHTTP(w, r)
	})
}

// nameSegment escapes a user, group or permission name for a detail URL
// under plusPrefixes, where a bare "+" would read back as a space.
func nameSegment(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), "+", "%2B")
}

func userNotFound(name string) string  { return fmt.Sprintf("User '%s' does not exist", name) }
func groupNotFound(name string) string { return fmt.Sprintf("Group '%s' does not exist", name) }

// convert moves a document between the generated API types and the wire
// types, which share their JSON names.
func convert[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("security: encode: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("security: decode: %w", err)
	}
	return out, nil
}

func strPtr(s string) *string { return &s }

// --- Users -----------------------------------------------------------------

// GetUsers lists users with their detail URLs.
func (s artifactoryService) GetUsers(
	ctx context.Context,
	_ artgen.GetUsersRequestObject,
) (artgen.GetUsersResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.identity(st).Users(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "list users", err)
	}
	base := baseurl.From(ctx) + securityUsers
	out := artgen.GetUsers200JSONResponse{}
	for _, u := range users {
		out = append(out, artgen.UserListItem{
			Name: strPtr(
				u.Name,
			),
			Realm: strPtr(u.Realm),
			Uri:   strPtr(base + nameSegment(u.Name)),
		})
	}
	return out, nil
}

// GetUserDetails returns the legacy user document, without the password.
func (s artifactoryService) GetUserDetails(
	ctx context.Context,
	req artgen.GetUserDetailsRequestObject,
) (artgen.GetUserDetailsResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.identity(st).User(ctx, req.UserName)
	if err != nil {
		return nil, s.identityErr(ctx, "get user", userNotFound(req.UserName), err)
	}
	doc, err := convert[artgen.GetUserDetails200JSONResponse](types.UserToLegacy(u))
	if err != nil {
		return nil, s.internalError(ctx, "render user", err)
	}
	return doc, nil
}

// userBody returns the user document a write carries, in either media type.
func userBody(name string, bodies ...*artgen.UserConfiguration) (*artgen.UserConfiguration, error) {
	for _, b := range bodies {
		if b == nil {
			continue
		}
		if b.Name != nil && *b.Name != name {
			return nil, badRequestf("the user name in the body must match the path")
		}
		return b, nil
	}
	return nil, badRequestf("a user document is required")
}

// CreateOrReplaceUser creates the user or replaces its fields. It answers
// 201 either way, which the artifactory_user resource relies on.
func (s artifactoryService) CreateOrReplaceUser(
	ctx context.Context,
	req artgen.CreateOrReplaceUserRequestObject,
) (artgen.CreateOrReplaceUserResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	body, err := userBody(req.UserName, req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactorySecurityUserPlusJSONBody)
	if err != nil {
		return nil, err
	}
	w, err := convert[wire.LegacyUser](body)
	if err != nil {
		return nil, badRequestf("%v", err)
	}
	// Server-owned fields are ignored on write.
	w.Name, w.Status, w.LastLoggedIn, w.Realm, w.MFAStatus = req.UserName, "", "", "", ""
	u, err := types.UserFromLegacy(w)
	if err != nil {
		return nil, badRequestf("%v", err)
	}
	if _, err := s.identity(st).PutUser(ctx, u, w.Password); err != nil {
		return nil, s.identityErr(ctx, "put user", "", err)
	}
	return artgen.CreateOrReplaceUser201Response{}, nil
}

// UpdateUser changes the fields the body carries.
func (s artifactoryService) UpdateUser(
	ctx context.Context,
	req artgen.UpdateUserRequestObject,
) (artgen.UpdateUserResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	b, err := userBody(req.UserName, req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactorySecurityUserPlusJSONBody)
	if err != nil {
		return nil, err
	}
	err = s.identity(st).PatchUser(ctx, req.UserName, identity.UserPatch{
		Email: b.Email, Password: b.Password, Admin: b.Admin, ProfileUpdatable: b.ProfileUpdatable,
		DisableUIAccess: b.DisableUIAccess, InternalPasswordDisabled: b.InternalPasswordDisabled,
		Groups: b.Groups, PolicyViewer: b.PolicyViewer, PolicyManager: b.PolicyManager,
		WatchManager: b.WatchManager, ReportsManager: b.ReportsManager,
	})
	if err != nil {
		return nil, s.identityErr(ctx, "update user", userNotFound(req.UserName), err)
	}
	return artgen.UpdateUser200Response{}, nil
}

// DeleteUser removes the user, its memberships and its tokens.
func (s artifactoryService) DeleteUser(
	ctx context.Context,
	req artgen.DeleteUserRequestObject,
) (artgen.DeleteUserResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.identity(st).DeleteUser(ctx, req.UserName); err != nil {
		return nil, s.identityErr(ctx, "delete user", userNotFound(req.UserName), err)
	}
	return artgen.DeleteUser200TextResponse(
		fmt.Sprintf("User '%s' has been removed successfully.", req.UserName)), nil
}

// --- Groups ----------------------------------------------------------------

// GetGroups lists groups with their detail URLs.
func (s artifactoryService) GetGroups(
	ctx context.Context,
	_ artgen.GetGroupsRequestObject,
) (artgen.GetGroupsResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.identity(st).Groups(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "list groups", err)
	}
	base := baseurl.From(ctx) + securityGroups
	out := artgen.GetGroups200JSONResponse{}
	for _, g := range groups {
		out = append(out, artgen.GroupListItem{
			Name: strPtr(g.Name), Uri: strPtr(base + nameSegment(g.Name)),
		})
	}
	return out, nil
}

// GetGroupDetails returns the legacy group document; members only with
// includeUsers=true.
func (s artifactoryService) GetGroupDetails(
	ctx context.Context,
	req artgen.GetGroupDetailsRequestObject,
) (artgen.GetGroupDetailsResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	g, err := s.identity(st).Group(ctx, req.GroupName)
	if err != nil {
		return nil, s.identityErr(ctx, "get group", groupNotFound(req.GroupName), err)
	}
	w := types.GroupToLegacy(g)
	if req.Params.IncludeUsers == nil || !*req.Params.IncludeUsers {
		w.UserNames = nil
	}
	doc, err := convert[artgen.GetGroupDetails200JSONResponse](w)
	if err != nil {
		return nil, s.internalError(ctx, "render group", err)
	}
	return doc, nil
}

// groupBody returns the group document a write carries, in either media
// type.
func groupBody(
	name string,
	bodies ...*artgen.GroupConfiguration,
) (*artgen.GroupConfiguration, error) {
	for _, b := range bodies {
		if b == nil {
			continue
		}
		if b.Name != nil && *b.Name != name {
			return nil, badRequestf("the group name in the body must match the path")
		}
		return b, nil
	}
	return nil, badRequestf("a group document is required")
}

// CreateOrReplaceGroup creates the group or replaces its fields. It answers
// 201 either way, which the artifactory_group resource requires.
func (s artifactoryService) CreateOrReplaceGroup(
	ctx context.Context,
	req artgen.CreateOrReplaceGroupRequestObject,
) (artgen.CreateOrReplaceGroupResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	b, err := groupBody(req.GroupName, req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactorySecurityGroupPlusJSONBody)
	if err != nil {
		return nil, err
	}
	w, err := convert[wire.LegacyGroup](b)
	if err != nil {
		return nil, badRequestf("%v", err)
	}
	w.Name, w.Realm, w.RealmAttributes = req.GroupName, "", ""
	if _, err := s.identity(st).PutGroup(ctx, types.GroupFromLegacy(w), b.UserNames); err != nil {
		return nil, s.identityErr(ctx, "put group", "", err)
	}
	return artgen.CreateOrReplaceGroup201Response{}, nil
}

// UpdateGroup changes the fields the body carries.
func (s artifactoryService) UpdateGroup(
	ctx context.Context,
	req artgen.UpdateGroupRequestObject,
) (artgen.UpdateGroupResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	b, err := groupBody(req.GroupName, req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactorySecurityGroupPlusJSONBody)
	if err != nil {
		return nil, err
	}
	err = s.identity(st).PatchGroup(ctx, req.GroupName, identity.GroupPatch{
		Description: b.Description, AutoJoin: b.AutoJoin, AdminPrivileges: b.AdminPrivileges,
		Members: b.UserNames, WatchManager: b.WatchManager, PolicyManager: b.PolicyManager,
		ReportsManager: b.ReportsManager,
	})
	if err != nil {
		return nil, s.identityErr(ctx, "update group", groupNotFound(req.GroupName), err)
	}
	return artgen.UpdateGroup200Response{}, nil
}

// DeleteGroup removes the group and its memberships.
func (s artifactoryService) DeleteGroup(
	ctx context.Context,
	req artgen.DeleteGroupRequestObject,
) (artgen.DeleteGroupResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.identity(st).DeleteGroup(ctx, req.GroupName); err != nil {
		return nil, s.identityErr(ctx, "delete group", groupNotFound(req.GroupName), err)
	}
	return artgen.DeleteGroup200TextResponse(
		fmt.Sprintf("Group '%s' has been removed successfully.", req.GroupName)), nil
}

// --- Locks and passwords ---------------------------------------------------

// GetLockedOutUsers lists the users the lock policy locked.
func (s artifactoryService) GetLockedOutUsers(
	ctx context.Context,
	_ artgen.GetLockedOutUsersRequestObject,
) (artgen.GetLockedOutUsersResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.identity(st).Locked(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "list locked users", err)
	}
	return artgen.GetLockedOutUsers200JSONResponse(names), nil
}

// UnlockAllLockedOutUsers clears every lock.
func (s artifactoryService) UnlockAllLockedOutUsers(
	ctx context.Context,
	_ artgen.UnlockAllLockedOutUsersRequestObject,
) (artgen.UnlockAllLockedOutUsersResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.identity(st).UnlockAll(ctx); err != nil {
		return nil, s.internalError(ctx, "unlock users", err)
	}
	return artgen.UnlockAllLockedOutUsers200TextResponse("All users were unlocked"), nil
}

// UnlockLockedOutUsers clears the named users' locks.
func (s artifactoryService) UnlockLockedOutUsers(
	ctx context.Context,
	req artgen.UnlockLockedOutUsersRequestObject,
) (artgen.UnlockLockedOutUsersResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	if req.Body != nil {
		names = *req.Body
	}
	if err := s.identity(st).Unlock(ctx, names); err != nil {
		return nil, s.identityErr(ctx, "unlock users", "", err)
	}
	return artgen.UnlockLockedOutUsers200TextResponse("The users were unlocked"), nil
}

// UnlockLockedOutUser is the path form the JFrog CLI sends for one user.
func (s artifactoryService) UnlockLockedOutUser(
	ctx context.Context,
	req artgen.UnlockLockedOutUserRequestObject,
) (artgen.UnlockLockedOutUserResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	id := s.identity(st)
	if _, err := id.User(ctx, req.UserName); err != nil {
		return nil, s.identityErr(ctx, "get user", userNotFound(req.UserName), err)
	}
	if err := id.Unlock(ctx, []string{req.UserName}); err != nil {
		return nil, s.identityErr(ctx, "unlock user", "", err)
	}
	return artgen.UnlockLockedOutUser200TextResponse(
		fmt.Sprintf("User '%s' was unlocked", req.UserName)), nil
}

// GetUserLockPolicy returns the lockout settings.
func (s artifactoryService) GetUserLockPolicy(
	ctx context.Context,
	_ artgen.GetUserLockPolicyRequestObject,
) (artgen.GetUserLockPolicyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.identity(st).LockPolicy(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "get user lock policy", err)
	}
	return artgen.GetUserLockPolicy200JSONResponse{
		Enabled:       &p.Enabled,
		LoginAttempts: &p.LoginAttempts,
	}, nil
}

// ConfigureUserLockPolicy replaces the lockout settings; omitted fields take
// their defaults. Disabling the policy also clears every lock.
func (s artifactoryService) ConfigureUserLockPolicy(
	ctx context.Context,
	req artgen.ConfigureUserLockPolicyRequestObject,
) (artgen.ConfigureUserLockPolicyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequestf("a user lock policy is required")
	}
	p := types.UserLockPolicy{
		Enabled:       req.Body.Enabled != nil && *req.Body.Enabled,
		LoginAttempts: auth.DefaultLoginAttempts,
	}
	if req.Body.LoginAttempts != nil {
		if p.LoginAttempts = *req.Body.LoginAttempts; p.LoginAttempts < 1 {
			return nil, badRequestf("loginAttempts must be at least 1")
		}
	}
	if err := s.identity(st).SetLockPolicy(ctx, p); err != nil {
		return nil, s.identityErr(ctx, "set user lock policy", "", err)
	}
	s.forgetPolicies()
	return artgen.ConfigureUserLockPolicy200TextResponse("The user lock policy was updated"), nil
}

// GetPasswordExpirationPolicy returns the password expiry settings.
func (s artifactoryService) GetPasswordExpirationPolicy(
	ctx context.Context,
	_ artgen.GetPasswordExpirationPolicyRequestObject,
) (artgen.GetPasswordExpirationPolicyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.identity(st).ExpirationPolicy(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "get password expiration policy", err)
	}
	return artgen.GetPasswordExpirationPolicy200JSONResponse(expirationDoc(p)), nil
}

func expirationDoc(p types.PasswordExpirationPolicy) artgen.PasswordExpirationPolicy {
	return artgen.PasswordExpirationPolicy{
		Enabled: &p.Enabled, PasswordMaxAge: &p.MaxAgeDays, NotifyByEmail: &p.NotifyByEmail,
	}
}

// SetPasswordExpirationPolicy replaces the policy: omitted fields take their
// defaults, not their previous values, as in Artifactory.
func (s artifactoryService) SetPasswordExpirationPolicy(
	ctx context.Context,
	req artgen.SetPasswordExpirationPolicyRequestObject,
) (artgen.SetPasswordExpirationPolicyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequestf("a password expiration policy is required")
	}
	p := types.PasswordExpirationPolicy{
		MaxAgeDays:    auth.DefaultPasswordMaxAgeDays,
		NotifyByEmail: true,
	}
	if req.Body.Enabled != nil {
		p.Enabled = *req.Body.Enabled
	}
	if req.Body.PasswordMaxAge != nil {
		p.MaxAgeDays = *req.Body.PasswordMaxAge
	}
	if req.Body.NotifyByEmail != nil {
		p.NotifyByEmail = *req.Body.NotifyByEmail
	}
	stored, err := s.identity(st).SetExpirationPolicy(ctx, p)
	if err != nil {
		return nil, s.identityErr(ctx, "set password expiration policy", "", err)
	}
	s.forgetPolicies()
	return artgen.SetPasswordExpirationPolicy200JSONResponse(expirationDoc(stored)), nil
}

// forgetPolicies makes a policy change apply to this instance's next login.
func (s artifactoryService) forgetPolicies() {
	if s.cfg.Auth != nil {
		s.cfg.Auth.ForgetPolicies()
	}
}

// ExpirePasswordForMultipleUsers forces the named users to change their
// passwords; unknown names are reported, not refused.
func (s artifactoryService) ExpirePasswordForMultipleUsers(
	ctx context.Context,
	req artgen.ExpirePasswordForMultipleUsersRequestObject,
) (artgen.ExpirePasswordForMultipleUsersResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || len(*req.Body) == 0 {
		return nil, badRequestf("name at least one user")
	}
	unknown, err := s.identity(st).ExpirePasswords(ctx, *req.Body)
	if err != nil {
		return nil, s.internalError(ctx, "expire passwords", err)
	}
	msg := "The passwords were expired"
	if len(unknown) > 0 {
		msg += "; unknown users were skipped: " + strings.Join(unknown, ", ")
	}
	return artgen.ExpirePasswordForMultipleUsers200JSONResponse{
		Status: strPtr("success"), Message: &msg,
	}, nil
}

// UnexpirePasswordForSingleUser clears a forced password change.
func (s artifactoryService) UnexpirePasswordForSingleUser(
	ctx context.Context,
	req artgen.UnexpirePasswordForSingleUserRequestObject,
) (artgen.UnexpirePasswordForSingleUserResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.identity(st).UnexpirePassword(ctx, req.UserName); err != nil {
		return nil, s.identityErr(ctx, "unexpire password", userNotFound(req.UserName), err)
	}
	return artgen.UnexpirePasswordForSingleUser200TextResponse(
		fmt.Sprintf("The password of user '%s' is no longer expired", req.UserName)), nil
}

// ExpirePasswordForSingleUser is the deprecated path form of the expiry, for
// one user; an unknown user is 404, as the unexpire form answers.
func (s artifactoryService) ExpirePasswordForSingleUser(
	ctx context.Context,
	req artgen.ExpirePasswordForSingleUserRequestObject,
) (artgen.ExpirePasswordForSingleUserResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	unknown, err := s.identity(st).ExpirePasswords(ctx, []string{req.UserName})
	if err != nil {
		return nil, s.internalError(ctx, "expire password", err)
	}
	if len(unknown) > 0 {
		return nil, apierror.New(http.StatusNotFound, userNotFound(req.UserName))
	}
	return artgen.ExpirePasswordForSingleUser200TextResponse(
		fmt.Sprintf("%s's credentials have been expired", req.UserName)), nil
}

// ExpirePasswordForAllUsers expires every internal password, the caller's
// included.
func (s artifactoryService) ExpirePasswordForAllUsers(
	ctx context.Context,
	_ artgen.ExpirePasswordForAllUsersRequestObject,
) (artgen.ExpirePasswordForAllUsersResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.identity(st).ExpireAllPasswords(ctx); err != nil {
		return nil, s.internalError(ctx, "expire passwords", err)
	}
	return artgen.ExpirePasswordForAllUsers200TextResponse(
		"Credentials have been successfully expired for all users"), nil
}

// ChangePassword is the deprecated password change, which names its user in
// the body and proves the change with the current password. An
// administrator may name anyone, with the current password all the same;
// anyone else only itself, and not through a narrowed token (narrowedToken),
// as the Access v2 change refuses one. An expired password reaches here
// (ownPasswordChange) with its administrator rights intact, so it is held to
// its own name.
func (s artifactoryService) ChangePassword(
	ctx context.Context,
	req artgen.ChangePasswordRequestObject,
) (artgen.ChangePasswordResponseObject, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // typed for the response
	}
	if req.Body == nil || req.Body.UserName == "" {
		return nil, badRequestf("userName is required")
	}
	b := req.Body
	self := p.Name == b.UserName && !narrowedToken(p)
	if !self && (!p.Admin || p.PasswordExpired) {
		return nil, auth.ErrForbidden
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	err = s.identity(st).
		ChangePassword(ctx, b.UserName, b.OldPassword, b.NewPassword1, b.NewPassword2)
	if err != nil {
		return nil, s.identityErr(ctx, "change password", userNotFound(b.UserName), err)
	}
	return artgen.ChangePassword200TextResponse("Password has been successfully changed"), nil
}

// --- Retired API keys --------------------------------------------------------

// CreateApiKey refuses: API keys are retired (decision 0005).
func (s artifactoryService) CreateApiKey(
	ctx context.Context,
	_ artgen.CreateApiKeyRequestObject,
) (artgen.CreateApiKeyResponseObject, error) {
	if _, err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err //nolint:wrapcheck // typed for the response
	}
	return nil, badRequestf(errAPIKeysRetired)
}

// RegenerateApiKey refuses: no user holds a key to rotate.
func (s artifactoryService) RegenerateApiKey(
	ctx context.Context,
	_ artgen.RegenerateApiKeyRequestObject,
) (artgen.RegenerateApiKeyResponseObject, error) {
	if _, err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err //nolint:wrapcheck // typed for the response
	}
	return nil, badRequestf(errAPIKeysRetired)
}

// GetApiKey answers 404, the documented answer when no key exists; the
// JFrog CLI reads it as "no key".
func (s artifactoryService) GetApiKey(
	ctx context.Context,
	_ artgen.GetApiKeyRequestObject,
) (artgen.GetApiKeyResponseObject, error) {
	if _, err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err //nolint:wrapcheck // typed for the response
	}
	return nil, apierror.New(http.StatusNotFound, "No API key exists: API keys are retired")
}

// RevokeApiKey answers 200 with nothing revoked, for scripts that revoke
// unconditionally. deleteAll=1 needs an administrator.
func (s artifactoryService) RevokeApiKey(
	ctx context.Context,
	req artgen.RevokeApiKeyRequestObject,
) (artgen.RevokeApiKeyResponseObject, error) {
	if req.Params.DeleteAll != nil &&
		*req.Params.DeleteAll == artgen.RevokeApiKeyParamsDeleteAllN1 {
		if err := adminOnly(ctx); err != nil {
			return nil, err
		}
	} else if _, err := auth.RequireAuthenticated(
		ctx,
	); err != nil {
		return nil, err //nolint:wrapcheck // typed for the response
	}
	return artgen.RevokeApiKey200JSONResponse{Info: strPtr(infoNoAPIKeys)}, nil
}

// RevokeUserApiKey answers 200 for an existing user and 404 otherwise; there
// is never a key to remove.
func (s artifactoryService) RevokeUserApiKey(
	ctx context.Context,
	req artgen.RevokeUserApiKeyRequestObject,
) (artgen.RevokeUserApiKeyResponseObject, error) {
	st, err := s.adminStore(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.identity(st).User(ctx, req.Username); err != nil {
		return nil, s.identityErr(ctx, "get user", userNotFound(req.Username), err)
	}
	return artgen.RevokeUserApiKey200JSONResponse{Info: strPtr(infoNoAPIKeys)}, nil
}

// --- Request bodies ----------------------------------------------------------

// policyPaths are the policy writes whose bodies may spell booleans and
// numbers as strings ("true", "60"), as Artifactory's documentation shows;
// the Terraform provider sends real JSON types. Both are accepted.
var policyPaths = map[string]bool{
	"/artifactory/api/security/userLockPolicy":                         true,
	"/artifactory/api/security/configuration/passwordExpirationPolicy": true,
}

// normalizePolicyBody rewrites string-spelled booleans and integers in a
// policy body's top-level fields into JSON types before the generated
// decoder sees them. A body that is not a JSON object passes through for
// the decoder to refuse.
func normalizePolicyBody(r *http.Request) error {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("security: read body: %w", err)
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) == nil {
		for k, v := range doc {
			str, ok := v.(string)
			if !ok {
				continue
			}
			if v, err := strconv.ParseBool(str); err == nil {
				doc[k] = v
			} else if n, err := strconv.Atoi(str); err == nil {
				doc[k] = n
			}
		}
		if nb, err := json.Marshal(doc); err == nil {
			b = nb
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	return nil
}
