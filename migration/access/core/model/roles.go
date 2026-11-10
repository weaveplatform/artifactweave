package model

import (
	"regexp"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/access/api"
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

// IsBuiltinEnvironment reports whether name is DEV or PROD, compared without
// regard to case as environment names are.
func IsBuiltinEnvironment(name string) bool {
	return slices.ContainsFunc(api.BuiltinEnvironments, func(b string) bool {
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
// but documents their default actions only in its UI. Developer follows
// JFrog's one published example (manage-project-roles: Read and Write in DEV
// and PROD, the Basic actions' bundles in the global vocabulary); the others
// follow each role's documented purpose, widening from Viewer to Project
// Admin. Administrators may change a PREDEFINED role's actions and
// environments (roles.Service.UpdateRole); these are only the defaults.
var builtinGlobalRoles = []Role{
	{
		Name: "Project Admin", Type: RoleAdmin,
		Description:  "Manages a project's members, roles and resources.",
		Environments: []string{api.EnvironmentDev, api.EnvironmentProd},
		Actions:      slices.Clone(GlobalRoleActions),
	},
	{
		Name: "Developer", Type: RolePredefined,
		Description:  "Reads and writes artifacts, builds and Release Bundles.",
		Environments: []string{api.EnvironmentDev, api.EnvironmentProd},
		// Read: read repository, Release Bundle, build and pipeline. Write:
		// annotate and deploy/cache repository artifacts, annotate and create
		// Release Bundles, annotate and deploy builds.
		Actions: []string{
			"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
			"READ_RELEASE_BUNDLE", "ANNOTATE_RELEASE_BUNDLE", "CREATE_RELEASE_BUNDLE",
			"READ_BUILD", "ANNOTATE_BUILD", "DEPLOY_BUILD", "READ_SOURCES_PIPELINE",
		},
	},
	{
		Name: "Contributor", Type: RolePredefined,
		Description:  "Reads, deploys and annotates artifacts and builds.",
		Environments: []string{api.EnvironmentDev},
		Actions: []string{
			"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
			"READ_RELEASE_BUNDLE", "READ_BUILD", "ANNOTATE_BUILD", "DEPLOY_BUILD",
			"READ_SOURCES_PIPELINE", "TRIGGER_PIPELINE",
		},
	},
	{
		Name: "Viewer", Type: RolePredefined,
		Description:  "Reads artifacts, builds and Release Bundles.",
		Environments: []string{api.EnvironmentDev, api.EnvironmentProd},
		Actions: []string{
			"READ_REPOSITORY", "READ_RELEASE_BUNDLE", "READ_BUILD", "READ_SOURCES_PIPELINE",
		},
	},
	{
		Name: "Release Manager", Type: RolePredefined,
		Description:  "Creates, distributes and deletes Release Bundles.",
		Environments: []string{api.EnvironmentDev, api.EnvironmentProd},
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
		Environments: []string{api.EnvironmentDev, api.EnvironmentProd},
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
	resource api.ResourceType
	action   api.Actions
}{
	"READ_REPOSITORY":             {api.ResourceArtifact, api.ActionRead},
	"ANNOTATE_REPOSITORY":         {api.ResourceArtifact, api.ActionAnnotate},
	"DEPLOY_CACHE_REPOSITORY":     {api.ResourceArtifact, api.ActionWrite},
	"DELETE_OVERWRITE_REPOSITORY": {api.ResourceArtifact, api.ActionDelete},
	"MANAGE_XRAY_MD_REPOSITORY":   {api.ResourceArtifact, api.ActionScan},
	"READ_BUILD":                  {api.ResourceBuild, api.ActionRead},
	"ANNOTATE_BUILD":              {api.ResourceBuild, api.ActionAnnotate},
	"DEPLOY_BUILD":                {api.ResourceBuild, api.ActionWrite},
	"DELETE_BUILD":                {api.ResourceBuild, api.ActionDelete},
	"MANAGE_XRAY_MD_BUILD":        {api.ResourceBuild, api.ActionScan},
}

// RoleActions returns what a role's actions grant on one resource type. It is
// how the permission evaluator reads a role: pipeline, Release Bundle and
// security actions grant nothing here.
func RoleActions(actions []string, resource api.ResourceType) api.Actions {
	var out api.Actions
	for _, a := range actions {
		if m, ok := roleActions[a]; ok && m.resource == resource {
			out |= m.action
		}
	}
	return out
}
