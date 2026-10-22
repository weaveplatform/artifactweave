# This reference proves repository management. Native clients and their
# protocol-specific endpoints are exercised by the separate P9 adapters.
resource "artifactory_remote_generic_repository" "this" {
  key                                   = "${var.repository_prefix}-generic"
  url                                   = var.upstream_urls["generic"]
  description                           = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds        = 600 + var.revision
  username                              = "remote-upstream-user"
  password_wo                           = var.upstream_password
  password_wo_version                   = tostring(var.revision)
  proxy                                 = var.proxy_key
  disable_proxy                         = false
  hard_fail                             = true
  offline                               = var.revision == 2
  store_artifacts_locally               = var.revision == 1
  socket_timeout_millis                 = 5000
  missed_cache_period_seconds           = 61 + var.revision
  metadata_retrieval_timeout_secs       = 20
  assumed_offline_period_secs           = 42 + var.revision
  unused_artifacts_cleanup_period_hours = 24 + var.revision
  bypass_head_requests                  = true
  enable_cookie_management              = true
  enable_token_authentication           = true
  allow_any_host_auth                   = false
  block_mismatching_mime_types          = false
  retrieve_sha256_from_server           = true
  query_params                          = "fixture=management"
  propagate_query_params                = true
  synchronize_properties                = true
  list_remote_folder_items              = true
  custom_http_headers = [
    { name = "X-Fixture-Secret", value = var.header_secret, sensitive = true },
    { name = "X-Fixture-Public", value = "revision-${var.revision}", sensitive = false },
  ]
  content_synchronisation {
    enabled                         = true
    properties_enabled              = true
    source_origin_absence_detection = true
    statistics_enabled              = false
  }
}

resource "artifactory_remote_docker_repository" "this" {
  key                            = "${var.repository_prefix}-docker"
  url                            = var.upstream_urls["docker"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  block_pushing_schema1          = true
  external_dependencies_enabled  = true
  external_dependencies_patterns = ["${var.upstream_urls["docker"]}/blobs/**"]
  project_id                     = "fixture-project"
}

resource "artifactory_remote_oci_repository" "this" {
  key                            = "${var.repository_prefix}-oci"
  url                            = var.upstream_urls["oci"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  external_dependencies_enabled  = true
  external_dependencies_patterns = ["${var.upstream_urls["oci"]}/blobs/**"]
}

resource "artifactory_remote_helmoci_repository" "this" {
  key                            = "${var.repository_prefix}-helmoci"
  url                            = var.upstream_urls["helmoci"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  external_dependencies_enabled  = true
  external_dependencies_patterns = ["${var.upstream_urls["helmoci"]}/blobs/**"]
}

resource "artifactory_remote_npm_repository" "this" {
  key                            = "${var.repository_prefix}-npm"
  url                            = var.upstream_urls["npm"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
}

resource "artifactory_remote_go_repository" "this" {
  key                            = "${var.repository_prefix}-go"
  url                            = var.upstream_urls["go"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  vcs_git_provider               = "GITHUB"
}

resource "artifactory_remote_maven_repository" "this" {
  key                              = "${var.repository_prefix}-maven"
  url                              = var.upstream_urls["maven"]
  description                      = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds   = 600 + var.revision
  handle_releases                  = true
  handle_snapshots                 = false
  fetch_jars_eagerly               = true
  fetch_sources_eagerly            = true
  reject_invalid_jars              = true
  max_unique_snapshots             = 5
  remote_repo_checksum_policy_type = "generate-if-absent"
  suppress_pom_consistency_checks  = false
}

resource "artifactory_remote_gradle_repository" "this" {
  key                              = "${var.repository_prefix}-gradle"
  url                              = var.upstream_urls["gradle"]
  description                      = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds   = 600 + var.revision
  handle_releases                  = true
  handle_snapshots                 = false
  fetch_jars_eagerly               = true
  fetch_sources_eagerly            = true
  reject_invalid_jars              = true
  max_unique_snapshots             = 5
  remote_repo_checksum_policy_type = "generate-if-absent"
  suppress_pom_consistency_checks  = false
}

resource "artifactory_remote_ivy_repository" "this" {
  key                              = "${var.repository_prefix}-ivy"
  url                              = var.upstream_urls["ivy"]
  description                      = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds   = 600 + var.revision
  handle_releases                  = true
  handle_snapshots                 = false
  fetch_jars_eagerly               = true
  fetch_sources_eagerly            = true
  reject_invalid_jars              = true
  max_unique_snapshots             = 5
  remote_repo_checksum_policy_type = "generate-if-absent"
  suppress_pom_consistency_checks  = false
}

resource "artifactory_remote_sbt_repository" "this" {
  key                              = "${var.repository_prefix}-sbt"
  url                              = var.upstream_urls["sbt"]
  description                      = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds   = 600 + var.revision
  handle_releases                  = true
  handle_snapshots                 = false
  fetch_jars_eagerly               = true
  fetch_sources_eagerly            = true
  reject_invalid_jars              = true
  max_unique_snapshots             = 5
  remote_repo_checksum_policy_type = "generate-if-absent"
  suppress_pom_consistency_checks  = false
}

resource "artifactory_remote_pypi_repository" "this" {
  key                            = "${var.repository_prefix}-pypi"
  url                            = var.upstream_urls["pypi"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  pypi_registry_url              = "${var.upstream_urls["pypi"]}/registry"
  pypi_repository_suffix         = "simple/"
}

resource "artifactory_remote_nuget_repository" "this" {
  key                            = "${var.repository_prefix}-nuget"
  url                            = var.upstream_urls["nuget"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  feed_context_path              = "api/v2"
  download_context_path          = "api/v2/package"
  v3_feed_url                    = "${var.upstream_urls["nuget"]}/v3/index.json"
  symbol_server_url              = "${var.upstream_urls["nuget"]}/symbols"
  force_nuget_authentication     = true
}

resource "artifactory_remote_helm_repository" "this" {
  key                            = "${var.repository_prefix}-helm"
  url                            = var.upstream_urls["helm"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  helm_charts_base_url           = "${var.upstream_urls["helm"]}/charts"
  external_dependencies_enabled  = true
  external_dependencies_patterns = ["${var.upstream_urls["helm"]}/charts/**"]
}

resource "artifactory_remote_terraform_repository" "this" {
  key                            = "${var.repository_prefix}-terraform"
  url                            = var.upstream_urls["terraform"]
  description                    = "remote management revision ${var.revision}"
  retrieval_cache_period_seconds = 600 + var.revision
  terraform_registry_url         = "${var.upstream_urls["terraform"]}/modules"
  terraform_providers_url        = "${var.upstream_urls["terraform"]}/providers"
}

