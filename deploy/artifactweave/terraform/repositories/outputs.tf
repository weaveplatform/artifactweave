output "repository_key" {
  description = "Key of the local generic repository."
  value       = artifactory_local_generic_repository.this.key
}

output "local_generic_repositories" {
  description = "Package type of each local generic repository, by key, as the repositories data source lists them."
  value       = { for r in data.artifactory_repositories.generic.repos : r.key => r.package_type }
}

output "repository_properties" {
  description = "Properties set on the generic repository itself."
  value       = artifactory_item_properties.repository.properties
}

output "item_path" {
  description = "Path of the file the properties are set on, or null while with_properties is false."
  value       = var.with_properties ? artifactory_item_properties.this[0].item_path : null
}

output "package_repository_keys" {
  description = "Keys of the local package repositories (npm, Go, Maven, Gradle, PyPI, NuGet, Helm, Helm OCI, Terraform module, Terraform provider and Terraform backend), by format."
  value = {
    npm                = artifactory_local_npm_repository.this.key
    go                 = artifactory_local_go_repository.this.key
    maven              = artifactory_local_maven_repository.this.key
    gradle             = artifactory_local_gradle_repository.this.key
    pypi               = artifactory_local_pypi_repository.this.key
    nuget              = artifactory_local_nuget_repository.this.key
    helm               = artifactory_local_helm_repository.this.key
    helmoci            = artifactory_local_helmoci_repository.this.key
    terraform_module   = artifactory_local_terraform_module_repository.this.key
    terraform_provider = artifactory_local_terraform_provider_repository.this.key
    terraform_backend  = artifactory_local_terraformbackend_repository.this.key
  }
}

output "gpg_keypair_name" {
  description = "Name of the GPG key pair to associate with the Terraform provider repository as its primaryKeyPairRef."
  value       = artifactory_keypair.terraform_provider_signing.pair_name
}

output "virtual_repository_keys" {
  description = "Keys of the virtual npm repository and of the virtual Maven repository, which has no members."
  value = {
    npm   = artifactory_virtual_npm_repository.this.key
    maven = artifactory_virtual_maven_repository.empty.key
  }
}
