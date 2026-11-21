output "fixture" {
  value = {
    policy_key     = artifactory_package_cleanup_policy.packages.key
    repository_key = artifactory_local_maven_repository.packages.key
    revision       = var.revision
  }
  description = "Resource keys and configuration revision for native runtime assertions."
}
