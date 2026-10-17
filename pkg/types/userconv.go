package types

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// ErrInvalidTimestamp reports a timestamp a wire form could not have sent.
var ErrInvalidTimestamp = errors.New("invalid timestamp")

// ErrInvalidStatus reports a user status no wire form defines.
var ErrInvalidStatus = errors.New("invalid user status")

// legacyTime is the millisecond ISO 8601 form the legacy API prints.
const legacyTime = "2006-01-02T15:04:05.000Z07:00"

func flag(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func ptr(b bool) *bool { return &b }

func parseWireTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidTimestamp, s)
	}
	return t.UTC(), nil
}

// nullableWireTime is an Access v2 time: nil, which encodes as null, for
// the zero time.
func nullableWireTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func formatWireTime(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(layout)
}

// legacyStatus maps the internal status to the legacy upper-case set, which
// has no INVITED: an invited user cannot sign in yet, so the legacy view
// reports DISABLED.
func legacyStatus(s UserStatus) string {
	switch s {
	case UserEnabled:
		return "ENABLED"
	case UserLocked:
		return "LOCKED"
	case UserInvited, UserDisabled:
		return "DISABLED"
	default:
		return ""
	}
}

func parseStatus(s string) (UserStatus, error) {
	switch st := UserStatus(strings.ToLower(s)); st {
	case "":
		return UserEnabled, nil
	case UserInvited, UserEnabled, UserDisabled, UserLocked:
		return st, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidStatus, s)
	}
}

func orRealm(r string) string {
	if r == "" {
		return RealmInternal
	}
	return r
}

// UserFromLegacy reads a legacy user document. Absent flags take the
// documented defaults (profileUpdatable true, everything else false); the
// write-only password stays on the wire struct for the caller to hash.
func UserFromLegacy(w wire.LegacyUser) (User, error) {
	last, err := parseWireTime(w.LastLoggedIn)
	if err != nil {
		return User{}, err
	}
	status, err := parseStatus(w.Status)
	if err != nil {
		return User{}, err
	}
	mfa := MFAStatus(w.MFAStatus)
	if mfa == "" {
		mfa = MFANone
	}
	return User{
		Name:                     w.Name,
		Email:                    w.Email,
		Admin:                    flag(w.Admin, false),
		ProfileUpdatable:         flag(w.ProfileUpdatable, true),
		InternalPasswordDisabled: flag(w.InternalPasswordDisabled, false),
		DisableUIAccess:          flag(w.DisableUIAccess, false),
		Realm:                    orRealm(w.Realm),
		Status:                   status,
		MFA:                      mfa,
		Groups:                   slices.Clone(w.Groups),
		LastLoggedIn:             last,
		Roles: RoleFlags{
			PolicyViewer:   flag(w.PolicyViewer, false),
			PolicyManager:  flag(w.PolicyManager, false),
			WatchManager:   flag(w.WatchManager, false),
			ReportsManager: flag(w.ReportsManager, false),
		},
	}, nil
}

// UserToLegacy renders the legacy view. Role flags the legacy form does not
// know are dropped.
func UserToLegacy(u User) wire.LegacyUser {
	return wire.LegacyUser{
		Name:                     u.Name,
		Email:                    u.Email,
		Admin:                    ptr(u.Admin),
		ProfileUpdatable:         ptr(u.ProfileUpdatable),
		DisableUIAccess:          ptr(u.DisableUIAccess),
		InternalPasswordDisabled: ptr(u.InternalPasswordDisabled),
		Groups:                   slices.Clone(u.Groups),
		Realm:                    u.Realm,
		LastLoggedIn:             formatWireTime(u.LastLoggedIn, legacyTime),
		Status:                   legacyStatus(u.Status),
		MFAStatus:                string(u.MFA),
		PolicyViewer:             ptr(u.Roles.PolicyViewer),
		PolicyManager:            ptr(u.Roles.PolicyManager),
		WatchManager:             ptr(u.Roles.WatchManager),
		ReportsManager:           ptr(u.Roles.ReportsManager),
	}
}

// UserFromAccess reads an Access v2 user document.
func UserFromAccess(w wire.AccessUser) (User, error) {
	var last time.Time
	if w.LastLoggedIn != nil {
		var err error
		if last, err = parseWireTime(*w.LastLoggedIn); err != nil {
			return User{}, err
		}
	}
	status, err := parseStatus(w.Status)
	if err != nil {
		return User{}, err
	}
	return User{
		Name:                     w.Username,
		Email:                    w.Email,
		Admin:                    flag(w.Admin, false),
		ProfileUpdatable:         flag(w.ProfileUpdatable, true),
		InternalPasswordDisabled: flag(w.InternalPasswordDisabled, false),
		DisableUIAccess:          flag(w.DisableUIAccess, false),
		Realm:                    orRealm(w.Realm),
		Status:                   status,
		MFA:                      MFANone,
		Groups:                   slices.Clone(w.Groups),
		LastLoggedIn:             last,
		Roles: RoleFlags{
			WatchManager:     flag(w.WatchManager, false),
			PolicyViewer:     flag(w.PolicyViewer, false),
			PolicyManager:    flag(w.PolicyManager, false),
			ReportsManager:   flag(w.ReportsManager, false),
			ResourcesManager: flag(w.ResourcesManager, false),
			ManageWebhook:    flag(w.ManageWebhook, false),
			PlatformAuditor:  flag(w.PlatformAuditor, false),
		},
	}, nil
}

// UserToAccess renders the Access v2 view. effectiveAdmin comes from
// EffectiveAdmin, which needs the user's groups.
func UserToAccess(u User, effectiveAdmin bool) wire.AccessUser {
	return wire.AccessUser{
		Username:                 u.Name,
		Email:                    u.Email,
		Groups:                   slices.Clone(u.Groups),
		Admin:                    ptr(u.Admin),
		ProfileUpdatable:         ptr(u.ProfileUpdatable),
		InternalPasswordDisabled: ptr(u.InternalPasswordDisabled),
		DisableUIAccess:          ptr(u.DisableUIAccess),
		WatchManager:             ptr(u.Roles.WatchManager),
		PolicyViewer:             ptr(u.Roles.PolicyViewer),
		PolicyManager:            ptr(u.Roles.PolicyManager),
		ReportsManager:           ptr(u.Roles.ReportsManager),
		ResourcesManager:         ptr(u.Roles.ResourcesManager),
		ManageWebhook:            ptr(u.Roles.ManageWebhook),
		PlatformAuditor:          ptr(u.Roles.PlatformAuditor),
		EffectiveAdmin:           ptr(effectiveAdmin),
		LastLoggedIn:             nullableWireTime(u.LastLoggedIn),
		Realm:                    u.Realm,
		Status:                   string(u.Status),
	}
}

// GroupFromLegacy reads a legacy group document.
func GroupFromLegacy(w wire.LegacyGroup) Group {
	return Group{
		Name:            w.Name,
		Description:     w.Description,
		AutoJoin:        flag(w.AutoJoin, false),
		AdminPrivileges: flag(w.AdminPrivileges, false),
		Realm:           orRealm(w.Realm),
		RealmAttributes: w.RealmAttributes,
		Members:         slices.Clone(w.UserNames),
		Roles: RoleFlags{
			WatchManager:   flag(w.WatchManager, false),
			PolicyManager:  flag(w.PolicyManager, false),
			ReportsManager: flag(w.ReportsManager, false),
		},
	}
}

// GroupToLegacy renders the legacy view.
func GroupToLegacy(g Group) wire.LegacyGroup {
	return wire.LegacyGroup{
		Name:            g.Name,
		Description:     g.Description,
		AutoJoin:        ptr(g.AutoJoin),
		AdminPrivileges: ptr(g.AdminPrivileges),
		Realm:           g.Realm,
		RealmAttributes: g.RealmAttributes,
		UserNames:       slices.Clone(g.Members),
		WatchManager:    ptr(g.Roles.WatchManager),
		PolicyManager:   ptr(g.Roles.PolicyManager),
		ReportsManager:  ptr(g.Roles.ReportsManager),
	}
}

// GroupFromAccess reads an Access v2 group document. The group form calls
// the resources role manage_resources; internally it is the same flag as the
// user form's resources_manager.
func GroupFromAccess(w wire.AccessGroup) Group {
	return Group{
		Name:            w.Name,
		Description:     w.Description,
		AutoJoin:        flag(w.AutoJoin, false),
		AdminPrivileges: flag(w.AdminPrivileges, false),
		Realm:           orRealm(w.Realm),
		RealmAttributes: w.RealmAttributes,
		ExternalID:      w.ExternalID,
		Members:         slices.Clone(w.Members),
		Roles: RoleFlags{
			ReportsManager:   flag(w.ReportsManager, false),
			WatchManager:     flag(w.WatchManager, false),
			PolicyManager:    flag(w.PolicyManager, false),
			PolicyViewer:     flag(w.PolicyViewer, false),
			ResourcesManager: flag(w.ManageResources, false),
			ManageWebhook:    flag(w.ManageWebhook, false),
		},
	}
}

// GroupToAccess renders the Access v2 view.
func GroupToAccess(g Group) wire.AccessGroup {
	return wire.AccessGroup{
		Name:            g.Name,
		Description:     g.Description,
		AutoJoin:        ptr(g.AutoJoin),
		AdminPrivileges: ptr(g.AdminPrivileges),
		ExternalID:      g.ExternalID,
		Members:         slices.Clone(g.Members),
		ReportsManager:  ptr(g.Roles.ReportsManager),
		WatchManager:    ptr(g.Roles.WatchManager),
		PolicyManager:   ptr(g.Roles.PolicyManager),
		PolicyViewer:    ptr(g.Roles.PolicyViewer),
		ManageResources: ptr(g.Roles.ResourcesManager),
		ManageWebhook:   ptr(g.Roles.ManageWebhook),
		Realm:           g.Realm,
		RealmAttributes: g.RealmAttributes,
	}
}
