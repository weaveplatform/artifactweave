output "repository_keys" {
  description = "Repositories whose single-target configurations are qualified."
  value = {
    push = artifactory_local_generic_repository.source.key
    pull = artifactory_remote_generic_repository.cache.key
  }
}
