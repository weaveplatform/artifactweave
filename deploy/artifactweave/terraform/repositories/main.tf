# Repositories on artifactweave with the jfrog/artifactory provider: a local
# generic repository and properties on one file in it.

resource "artifactory_local_generic_repository" "this" {
  key         = var.repository_key
  description = "managed by Terraform"
}

# The provider cannot deploy content, so the file the properties go on is
# deployed between a first apply with with_properties = false and the full one.
resource "artifactory_item_properties" "this" {
  count      = var.with_properties ? 1 : 0
  repo_key   = artifactory_local_generic_repository.this.key
  item_path  = var.item_path
  properties = var.properties
}
