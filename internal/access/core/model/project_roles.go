package model

import "slices"

// ProjectRoleActions is the project role action vocabulary (research 07 and
// ProjectRoleCreate in api/src/access/paths-projects.yaml). It differs from
// GlobalRoleActions: project management actions are valid here, while the
// global READ_POLICIES_SECURITY action is not. Validation must keep the two
// vocabularies separate rather than admitting every global action in a project.
// RoleActions still maps only implemented resource actions to permissions.
var ProjectRoleActions = []string{
	"READ_REPOSITORY", "ANNOTATE_REPOSITORY", "DEPLOY_CACHE_REPOSITORY",
	"DELETE_OVERWRITE_REPOSITORY", "MANAGE_XRAY_MD_REPOSITORY",
	"READ_RELEASE_BUNDLE", "ANNOTATE_RELEASE_BUNDLE", "CREATE_RELEASE_BUNDLE",
	"DISTRIBUTE_RELEASE_BUNDLE", "DELETE_RELEASE_BUNDLE", "MANAGE_XRAY_MD_RELEASE_BUNDLE",
	"READ_BUILD", "ANNOTATE_BUILD", "DEPLOY_BUILD", "DELETE_BUILD", "MANAGE_XRAY_MD_BUILD",
	"READ_SOURCES_PIPELINE", "TRIGGER_PIPELINE", "READ_INTEGRATIONS_PIPELINE", "READ_POOLS_PIPELINE",
	"MANAGE_INTEGRATIONS_PIPELINE", "MANAGE_SOURCES_PIPELINE", "MANAGE_POOLS_PIPELINE",
	"REPORTS_SECURITY", "WATCHES_SECURITY", "POLICIES_SECURITY", "RULES_SECURITY",
	"TRIGGER_SECURITY", "ISSUES_SECURITY", "LICENCES_SECURITY", "MANAGE_MEMBERS", "MANAGE_RESOURCES",
}

// IsProjectRoleAction reports whether a is in ProjectRoleActions. Action names
// use the documented spelling and case, including LICENCES_SECURITY.
func IsProjectRoleAction(a string) bool { return slices.Contains(ProjectRoleActions, a) }
