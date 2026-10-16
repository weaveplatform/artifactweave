output "cdn_repository_key" {
  description = "Key of the repository with cdnRedirect."
  value       = artifactory_local_generic_repository.cdn.key
}

output "direct_repository_key" {
  description = "Key of the repository with downloadRedirect."
  value       = artifactory_local_generic_repository.direct.key
}
