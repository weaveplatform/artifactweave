locals {
  projects = {
    owner    = "${var.resource_prefix}-owner"
    consumer = "${var.resource_prefix}-consumer"
    outsider = "${var.resource_prefix}-outsider"
  }
  users = toset(["owner", "quota", "consumer", "outsider"])
}

# Keep the planned legacy resource under the pinned provider as well as its
# replacement alias: both must round-trip without recreating a project.
resource "project" "owner" {
  key                        = local.projects.owner
  display_name               = "${var.resource_prefix} owner revision ${var.revision}"
  description                = "Fixture-owned hard/soft quota and effective project authority"
  max_storage_in_gibibytes   = 1
  block_deployments_on_limit = var.revision == 1
  email_notification         = false

  admin_privileges {
    manage_members           = true
    manage_resources         = true
    manage_remote_repository = true
    index_resources          = false
  }
}

resource "project_project" "client" {
  for_each     = toset(["consumer", "outsider"])
  key          = local.projects[each.key]
  display_name = "${var.resource_prefix} ${each.key}"
  description  = "Fixture-owned standard sharing ${each.key}"

  admin_privileges {
    manage_members           = true
    manage_resources         = true
    manage_remote_repository = false
    index_resources          = false
  }
}

resource "platform_group" "owner" {
  name        = "${var.resource_prefix}-team"
  description = "Project role membership changes must affect the existing user"
}

resource "artifactory_user" "member" {
  for_each = local.users
  name     = "${var.resource_prefix}-${each.key}"
  email    = "${var.resource_prefix}-${each.key}@fixture.invalid"
  password = var.user_password
  groups   = each.key == "owner" ? [platform_group.owner.name] : []
}

resource "project_role" "reader" {
  name         = "${var.resource_prefix}-reader"
  type         = "CUSTOM"
  project_key  = project.owner.key
  environments = ["DEV"]
  actions      = ["READ_REPOSITORY"]
}

resource "project_role" "writer" {
  name         = "${var.resource_prefix}-writer"
  type         = "CUSTOM"
  project_key  = project.owner.key
  environments = ["DEV"]
  actions      = ["READ_REPOSITORY", "DEPLOY_CACHE_REPOSITORY", "DELETE_OVERWRITE_REPOSITORY"]
}

resource "project_role" "client" {
  for_each     = project_project.client
  name         = "${var.resource_prefix}-client"
  type         = "CUSTOM"
  project_key  = each.value.key
  environments = ["DEV"]
  actions      = each.key == "consumer" ? ["READ_REPOSITORY", "DEPLOY_CACHE_REPOSITORY"] : ["READ_REPOSITORY"]
}

resource "project_user" "owner" {
  project_key = project.owner.key
  name        = artifactory_user.member["owner"].name
  roles       = [project_role.reader.name]
}

resource "project_user" "quota" {
  project_key = project.owner.key
  name        = artifactory_user.member["quota"].name
  roles       = [project_role.writer.name]
}

resource "project_user" "client" {
  for_each    = project_project.client
  project_key = each.value.key
  name        = artifactory_user.member[each.key].name
  roles       = [project_role.client[each.key].name]
}

resource "project_group" "owner" {
  project_key = project.owner.key
  name        = platform_group.owner.name
  roles       = var.revision == 1 ? [project_role.writer.name] : [project_role.reader.name]
}

resource "project_environment" "owner" {
  project_key = project.owner.key
  name        = var.revision == 1 ? "lab" : "lab-next"
}

resource "artifactory_local_generic_repository" "owned" {
  for_each             = toset(["attached", "shared", "all", "foreign"])
  key                  = each.key == "attached" ? "${var.resource_prefix}-attached-local" : "${local.projects[each.key == "foreign" ? "outsider" : "owner"]}-${each.key}-local"
  description          = "Fixture-owned ${each.key} repository"
  project_key          = each.key == "attached" ? null : each.key == "foreign" ? project_project.client["outsider"].key : project.owner.key
  project_environments = ["DEV"]

  # The standalone attachment resource owns these fields. Direct API checks
  # verify their current values; ignoring them here must never hide a bad attach.
  lifecycle {
    ignore_changes = [project_key, project_environments]
  }
}

resource "project_repository" "attached" {
  key         = artifactory_local_generic_repository.owned["attached"].key
  project_key = project.owner.key
}

resource "project_share_repository" "consumer" {
  count              = var.revision == 1 ? 1 : 0
  repo_key           = artifactory_local_generic_repository.owned["shared"].key
  target_project_key = project_project.client["consumer"].key
  read_only          = true
}

resource "project_share_repository_with_all" "all" {
  repo_key  = artifactory_local_generic_repository.owned["all"].key
  read_only = var.revision == 1
}
