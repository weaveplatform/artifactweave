package types

import (
	"regexp"
	"slices"
	"strings"
)

// GlobalRoleActions is the global role action vocabulary, in the order the
// Access API and the platform provider's validator list it (research 07).
// Only the repository and build actions grant anything in artifactweave; the
// rest are stored and echoed so roles round-trip.
var GlobalRoleActions = []string{
	"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
	"DELETE_OVERWRITE_REPOSITORY", "MANAGE_XRAY_MD_REPOSITORY",
	"READ_RELEASE_BUNDLE", "ANNOTATE_RELEASE_BUNDLE", "CREATE_RELEASE_BUNDLE",
	"DISTRIBUTE_RELEASE_BUNDLE", "DELETE_RELEASE_BUNDLE", "MANAGE_XRAY_MD_RELEASE_BUNDLE",
	"READ_BUILD", "ANNOTATE_BUILD", "DEPLOY_BUILD", "DELETE_BUILD", "MANAGE_XRAY_MD_BUILD",
	"READ_SOURCES_PIPELINE", "TRIGGER_PIPELINE", "READ_INTEGRATIONS_PIPELINE",
	"READ_POOLS_PIPELINE",
	"REPORTS_SECURITY", "WATCHES_SECURITY", "POLICIES_SECURITY", "RULES_SECURITY",
	"READ_POLICIES_SECURITY",
}

// IsGlobalRoleAction reports whether a is in GlobalRoleActions.
func IsGlobalRoleAction(a string) bool { return slices.Contains(GlobalRoleActions, a) }

// MaxCustomGlobalRoles is how many CUSTOM_GLOBAL roles JFrog allows.
const MaxCustomGlobalRoles = 30

// BuiltinEnvironments are the global environments every instance has. They
// are not stored: they cannot be renamed or deleted, and a repository
// belongs to DEV unless told otherwise.
var BuiltinEnvironments = []string{EnvironmentDev, EnvironmentProd}

// IsBuiltinEnvironment reports whether name is DEV or PROD, compared without
// regard to case as environment names are.
func IsBuiltinEnvironment(name string) bool {
	return slices.ContainsFunc(BuiltinEnvironments, func(b string) bool {
		return strings.EqualFold(b, name)
	})
}

// environmentName is the Access API's rule for environment names.
var environmentName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ValidEnvironmentName reports whether name is 1 to 32 letters, digits,
// hyphens and underscores.
func ValidEnvironmentName(name string) bool { return environmentName.MatchString(name) }

// The built-in global roles. JFrog names them (Project Admin, Developer,
// Contributor, Viewer, Release Manager and Security Manager; Application
// Admin and the ML roles belong to products artifactweave does not serve)
// but documents their default actions only in its UI. These sets follow
// each role's documented purpose, widening from Viewer to Project Admin.
var builtinGlobalRoles = []Role{
	{
		Name: "Project Admin", Type: RoleAdmin,
		Description:  "Manages a project's members, roles and resources.",
		Environments: []string{EnvironmentDev, EnvironmentProd},
		Actions:      slices.Clone(GlobalRoleActions),
	},
	{
		Name: "Developer", Type: RolePredefined,
		Description:  "Reads, deploys, annotates and deletes artifacts and builds.",
		Environments: []string{EnvironmentDev},
		Actions: []string{
			"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
			"DELETE_OVERWRITE_REPOSITORY", "READ_RELEASE_BUNDLE",
			"READ_BUILD", "ANNOTATE_BUILD", "DEPLOY_BUILD", "DELETE_BUILD",
			"READ_SOURCES_PIPELINE", "TRIGGER_PIPELINE", "READ_INTEGRATIONS_PIPELINE",
			"READ_POOLS_PIPELINE",
		},
	},
	{
		Name: "Contributor", Type: RolePredefined,
		Description:  "Reads, deploys and annotates artifacts and builds.",
		Environments: []string{EnvironmentDev},
		Actions: []string{
			"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
			"READ_RELEASE_BUNDLE", "READ_BUILD", "ANNOTATE_BUILD", "DEPLOY_BUILD",
			"READ_SOURCES_PIPELINE", "TRIGGER_PIPELINE",
		},
	},
	{
		Name: "Viewer", Type: RolePredefined,
		Description:  "Reads artifacts, builds and Release Bundles.",
		Environments: []string{EnvironmentDev, EnvironmentProd},
		Actions: []string{
			"READ_REPOSITORY", "READ_RELEASE_BUNDLE", "READ_BUILD", "READ_SOURCES_PIPELINE",
		},
	},
	{
		Name: "Release Manager", Type: RolePredefined,
		Description:  "Creates, distributes and deletes Release Bundles.",
		Environments: []string{EnvironmentDev, EnvironmentProd},
		Actions: []string{
			"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
			"READ_RELEASE_BUNDLE", "ANNOTATE_RELEASE_BUNDLE", "CREATE_RELEASE_BUNDLE",
			"DISTRIBUTE_RELEASE_BUNDLE", "DELETE_RELEASE_BUNDLE",
			"READ_BUILD", "ANNOTATE_BUILD",
		},
	},
	{
		Name: "Security Manager", Type: RolePredefined,
		Description:  "Manages Xray data, watches, policies and reports.",
		Environments: []string{EnvironmentDev, EnvironmentProd},
		Actions: []string{
			"READ_REPOSITORY", "MANAGE_XRAY_MD_REPOSITORY", "READ_RELEASE_BUNDLE",
			"MANAGE_XRAY_MD_RELEASE_BUNDLE", "READ_BUILD", "MANAGE_XRAY_MD_BUILD",
			"REPORTS_SECURITY", "WATCHES_SECURITY", "POLICIES_SECURITY", "RULES_SECURITY",
			"READ_POLICIES_SECURITY",
		},
	},
}

// BuiltinGlobalRoles returns copies of the ADMIN and PREDEFINED global roles,
// in the order the Access API lists them.
func BuiltinGlobalRoles() []Role {
	out := make([]Role, len(builtinGlobalRoles))
	for i, r := range builtinGlobalRoles {
		r.Environments = slices.Clone(r.Environments)
		r.Actions = slices.Clone(r.Actions)
		out[i] = r
	}
	return out
}

// BuiltinGlobalRole returns the built-in global role called name.
func BuiltinGlobalRole(name string) (Role, bool) {
	for _, r := range BuiltinGlobalRoles() {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// roleActions maps the role actions that grant something in artifactweave
// onto the permission model's actions.
var roleActions = map[string]struct {
	resource ResourceType
	action   Actions
}{
	"READ_REPOSITORY":             {ResourceArtifact, ActionRead},
	"ANNOTATE_REPOSITORY":         {ResourceArtifact, ActionAnnotate},
	"DEPLOY_CACHE_REPOSITORY":     {ResourceArtifact, ActionWrite},
	"DELETE_OVERWRITE_REPOSITORY": {ResourceArtifact, ActionDelete},
	"MANAGE_XRAY_MD_REPOSITORY":   {ResourceArtifact, ActionScan},
	"READ_BUILD":                  {ResourceBuild, ActionRead},
	"ANNOTATE_BUILD":              {ResourceBuild, ActionAnnotate},
	"DEPLOY_BUILD":                {ResourceBuild, ActionWrite},
	"DELETE_BUILD":                {ResourceBuild, ActionDelete},
	"MANAGE_XRAY_MD_BUILD":        {ResourceBuild, ActionScan},
}

// RoleActions returns what a role's actions grant on one resource type. It is
// how the permission evaluator reads a role: pipeline, Release Bundle and
// security actions grant nothing here.
func RoleActions(actions []string, resource ResourceType) Actions {
	var out Actions
	for _, a := range actions {
		if m, ok := roleActions[a]; ok && m.resource == resource {
			out |= m.action
		}
	}
	return out
}
