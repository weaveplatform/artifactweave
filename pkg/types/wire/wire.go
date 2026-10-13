// Package wire mirrors the JFrog JSON forms that pkg/types converts: the
// legacy v1 and v2 permission targets, the Access v2 permission, and the
// legacy and Access user and group documents. Field names match JFrog
// exactly, which is why this package (with pkg/repo) is exempt from the
// project's snake_case tag rule (decision 0002). It holds no logic; the
// conversions live in pkg/types so this package never depends on the domain
// model.
//
// Booleans whose default is not false are pointers so a converter can tell
// "absent" from "false" when a client omits them.
package wire

// PermissionTargetV1 is /artifactory/api/security/permissions/{name}.
// Patterns are single comma-separated strings and the target covers
// repositories only.
type PermissionTargetV1 struct {
	Name            string       `json:"name"`
	IncludesPattern string       `json:"includesPattern,omitempty"`
	ExcludesPattern string       `json:"excludesPattern,omitempty"`
	Repositories    []string     `json:"repositories"`
	Principals      PrincipalsV1 `json:"principals,omitzero"`
}

// PrincipalsV1 maps principal names to action letters (r n w d m mxm x).
type PrincipalsV1 struct {
	Users  map[string][]string `json:"users,omitempty"`
	Groups map[string][]string `json:"groups,omitempty"`
}

// PermissionTargetV2 is /artifactory/api/v2/security/permissions/{name}.
type PermissionTargetV2 struct {
	Name          string     `json:"name"`
	Repo          *SectionV2 `json:"repo,omitempty"`
	Build         *SectionV2 `json:"build,omitempty"`
	ReleaseBundle *SectionV2 `json:"releaseBundle,omitempty"`
}

// SectionV2 is one section of a legacy v2 target. The pattern keys are
// kebab-case inside an otherwise camelCase document, as JFrog defines them.
type SectionV2 struct {
	IncludePatterns []string  `json:"include-patterns"`
	ExcludePatterns []string  `json:"exclude-patterns"`
	Repositories    []string  `json:"repositories"`
	Actions         ActionsV2 `json:"actions,omitzero"`
}

// ActionsV2 maps principal names to legacy action names (read, annotate,
// write, delete, manage, managedXrayMeta, distribute).
type ActionsV2 struct {
	Users  map[string][]string `json:"users,omitempty"`
	Groups map[string][]string `json:"groups,omitempty"`
}

// AccessPermission is /access/api/v2/permissions/{name}.
type AccessPermission struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description,omitempty"`
	Resources   map[string]AccessResource `json:"resources"`
}

// AccessResource is one resource section of an Access v2 permission.
type AccessResource struct {
	Actions AccessActions           `json:"actions,omitzero"`
	Targets map[string]AccessTarget `json:"targets"`
}

// AccessActions maps principal names to Access action names.
type AccessActions struct {
	Users  map[string][]string `json:"users,omitempty"`
	Groups map[string][]string `json:"groups,omitempty"`
}

// AccessTarget holds the patterns of one target.
type AccessTarget struct {
	IncludePatterns []string `json:"include_patterns"`
	ExcludePatterns []string `json:"exclude_patterns"`
}

// LegacyUser is /artifactory/api/security/users/{name}. Password is
// write-only; status, realm, lastLoggedIn and mfaStatus are server-owned.
type LegacyUser struct {
	Name                     string   `json:"name"`
	Email                    string   `json:"email,omitempty"`
	Password                 string   `json:"password,omitempty"`
	Admin                    *bool    `json:"admin,omitempty"`
	ProfileUpdatable         *bool    `json:"profileUpdatable,omitempty"`
	DisableUIAccess          *bool    `json:"disableUIAccess,omitempty"`
	InternalPasswordDisabled *bool    `json:"internalPasswordDisabled,omitempty"`
	Groups                   []string `json:"groups,omitempty"`
	Realm                    string   `json:"realm,omitempty"`
	LastLoggedIn             string   `json:"lastLoggedIn,omitempty"`
	Status                   string   `json:"status,omitempty"`
	MFAStatus                string   `json:"mfaStatus,omitempty"`
	PolicyViewer             *bool    `json:"policyViewer,omitempty"`
	PolicyManager            *bool    `json:"policyManager,omitempty"`
	WatchManager             *bool    `json:"watchManager,omitempty"`
	ReportsManager           *bool    `json:"reportsManager,omitempty"`
}

// LegacyGroup is /artifactory/api/security/groups/{name}.
type LegacyGroup struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	AutoJoin        *bool    `json:"autoJoin,omitempty"`
	AdminPrivileges *bool    `json:"adminPrivileges,omitempty"`
	Realm           string   `json:"realm,omitempty"`
	RealmAttributes string   `json:"realmAttributes,omitempty"`
	UserNames       []string `json:"userNames,omitempty"`
	WatchManager    *bool    `json:"watchManager,omitempty"`
	PolicyManager   *bool    `json:"policyManager,omitempty"`
	ReportsManager  *bool    `json:"reportsManager,omitempty"`
}

// AccessUser is /access/api/v2/users/{username}. EffectiveAdmin, Realm,
// Status and LastLoggedIn are server-owned; Password is write-only.
type AccessUser struct {
	Username                 string   `json:"username"`
	Email                    string   `json:"email,omitempty"`
	Password                 string   `json:"password,omitempty"`
	Groups                   []string `json:"groups,omitempty"`
	Admin                    *bool    `json:"admin,omitempty"`
	ProfileUpdatable         *bool    `json:"profile_updatable,omitempty"`
	InternalPasswordDisabled *bool    `json:"internal_password_disabled,omitempty"`
	DisableUIAccess          *bool    `json:"disable_ui_access,omitempty"`
	WatchManager             *bool    `json:"watch_manager,omitempty"`
	PolicyViewer             *bool    `json:"policy_viewer,omitempty"`
	PolicyManager            *bool    `json:"policy_manager,omitempty"`
	ReportsManager           *bool    `json:"reports_manager,omitempty"`
	ResourcesManager         *bool    `json:"resources_manager,omitempty"`
	ManageWebhook            *bool    `json:"manage_webhook,omitempty"`
	PlatformAuditor          *bool    `json:"platform_auditor,omitempty"`
	EffectiveAdmin           *bool    `json:"effective_admin,omitempty"`
	LastLoggedIn             string   `json:"last_logged_in,omitempty"`
	Realm                    string   `json:"realm,omitempty"`
	Status                   string   `json:"status,omitempty"`
}

// AccessGroup is /access/api/v2/groups/{name}.
type AccessGroup struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	AutoJoin        *bool    `json:"auto_join,omitempty"`
	AdminPrivileges *bool    `json:"admin_privileges,omitempty"`
	ExternalID      string   `json:"external_id,omitempty"`
	Members         []string `json:"members,omitempty"`
	ReportsManager  *bool    `json:"reports_manager,omitempty"`
	WatchManager    *bool    `json:"watch_manager,omitempty"`
	PolicyManager   *bool    `json:"policy_manager,omitempty"`
	PolicyViewer    *bool    `json:"policy_viewer,omitempty"`
	ManageResources *bool    `json:"manage_resources,omitempty"`
	ManageWebhook   *bool    `json:"manage_webhook,omitempty"`
	Realm           string   `json:"realm,omitempty"`
	RealmAttributes string   `json:"realm_attributes,omitempty"`
}
