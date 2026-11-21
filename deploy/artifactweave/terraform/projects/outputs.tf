output "fixture" {
  description = "Non-secret identities for direct API and resource-effect assertions."
  value = {
    projects     = local.projects
    users        = { for name, user in artifactory_user.member : name => user.name }
    repositories = { for name, repository in artifactory_local_generic_repository.owned : name => repository.key }
    group        = platform_group.owner.name
    reader       = project_role.reader.name
    writer       = project_role.writer.name
    client_role  = "${var.resource_prefix}-client"
    environment  = project_environment.owner.id
    revision     = var.revision
  }
}
