output "repository_key" {
  description = "Key of the local generic repository."
  value       = artifactory_local_generic_repository.this.key
}

output "item_path" {
  description = "Path of the file the properties are set on, or null while with_properties is false."
  value       = var.with_properties ? artifactory_item_properties.this[0].item_path : null
}
