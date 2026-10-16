output "repository_key" {
  description = "Key of the local generic repository."
  value       = artifactory_local_generic_repository.this.key
}

output "item_path" {
  description = "Path of the file the properties are set on, or null while with_properties is false."
  value       = var.with_properties ? artifactory_item_properties.this[0].item_path : null
}

output "package_repository_keys" {
  description = "Keys of the local npm, Go, Maven and Gradle repositories, by format."
  value = {
    npm    = artifactory_local_npm_repository.this.key
    go     = artifactory_local_go_repository.this.key
    maven  = artifactory_local_maven_repository.this.key
    gradle = artifactory_local_gradle_repository.this.key
  }
}
