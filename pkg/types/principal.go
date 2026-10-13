package types

import "time"

// UserStatus is the Access account status. The legacy API spells a subset of
// it in upper case.
type UserStatus string

// User statuses.
const (
	UserInvited  UserStatus = "invited"
	UserEnabled  UserStatus = "enabled"
	UserDisabled UserStatus = "disabled"
	UserLocked   UserStatus = "locked"
)

// MFAStatus is the legacy view's multi-factor status; artifactweave has no
// MFA of its own, so it reports what an identity provider recorded or NONE.
type MFAStatus string

// MFA statuses.
const (
	MFANone     MFAStatus = "NONE"
	MFAEnabled  MFAStatus = "ENABLED"
	MFADisabled MFAStatus = "DISABLED"
)

// RealmInternal is the realm of users and groups artifactweave manages itself.
const RealmInternal = "internal"

// RoleFlags are the platform role switches JFrog keeps on users and groups.
// They are stored as one JSON document per principal (research 15 section 4)
// because the set grows with JFrog releases and none of them is queried.
type RoleFlags struct {
	WatchManager     bool `json:"watch_manager,omitempty"`
	PolicyViewer     bool `json:"policy_viewer,omitempty"`
	PolicyManager    bool `json:"policy_manager,omitempty"`
	ReportsManager   bool `json:"reports_manager,omitempty"`
	ResourcesManager bool `json:"resources_manager,omitempty"`
	ManageWebhook    bool `json:"manage_webhook,omitempty"`
	PlatformAuditor  bool `json:"platform_auditor,omitempty"`
}

// User is an account. PasswordHash is an Argon2id encoding (decision 0005)
// and never leaves the server; the wire forms carry passwords write-only.
type User struct {
	Name                     string
	Email                    string
	PasswordHash             []byte `json:"-"`
	Admin                    bool
	ProfileUpdatable         bool
	InternalPasswordDisabled bool
	DisableUIAccess          bool
	Realm                    string
	Status                   UserStatus
	MFA                      MFAStatus
	Groups                   []string
	Roles                    RoleFlags
	LastLoggedIn             time.Time
	Created                  time.Time
}

// Group is a set of users that permissions and roles can name.
type Group struct {
	Name            string
	Description     string
	AutoJoin        bool
	AdminPrivileges bool
	Realm           string
	RealmAttributes string
	ExternalID      string
	Members         []string
	Roles           RoleFlags
}

// EffectiveAdmin reports whether u is an administrator directly or through a
// group with admin privileges; the Access user view reports it as
// effective_admin.
func EffectiveAdmin(u User, groups []Group) bool {
	if u.Admin {
		return true
	}
	member := make(map[string]bool, len(u.Groups))
	for _, g := range u.Groups {
		member[g] = true
	}
	for _, g := range groups {
		if g.AdminPrivileges && member[g.Name] {
			return true
		}
	}
	return false
}
