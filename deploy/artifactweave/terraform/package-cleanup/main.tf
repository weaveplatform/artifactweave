resource "artifactory_local_maven_repository" "packages" {
  key             = "${var.repository_prefix}-maven"
  repo_layout_ref = "maven-2-default"
}

resource "artifactory_package_cleanup_policy" "packages" {
  key                 = "${var.repository_prefix}-policy"
  description         = "Pinned provider cleanup revision ${var.revision}"
  cron_expression     = var.revision == 1 ? "0 0 2 1 1 ? 2099" : "0 0 3 1 1 ? 2099"
  duration_in_minutes = var.revision
  enabled             = var.revision == 2
  skip_trashcan       = false

  search_criteria = {
    package_types        = ["maven"]
    repos                = [artifactory_local_maven_repository.packages.key]
    included_packages    = ["**"]
    include_all_projects = false
    included_projects    = ["default"]
    keep_last_n_versions = 1
  }
}
