output "repository_key" {
  description = "Key of the repository the team permission covers."
  value       = artifactory_local_generic_repository.team.key
}

output "groups" {
  description = "Names of the developers group (Access v2) and the auditors group (legacy security API)."
  value = {
    developers = platform_group.developers.name
    auditors   = artifactory_group.auditors.name
  }
}

output "users" {
  description = "Names of the developer user and the managed CI user."
  value = {
    developer = artifactory_user.developer.name
    ci        = artifactory_managed_user.ci.name
  }
}

output "permission_name" {
  description = "Name of the permission over the repository."
  value       = platform_permission.team.name
}

output "keypair_name" {
  description = "Name of the signing key pair."
  value       = artifactory_keypair.signing.pair_name
}
