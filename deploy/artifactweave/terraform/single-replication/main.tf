resource "artifactory_local_generic_repository" "source" {
  key = "${var.repository_prefix}-local"
}

resource "artifactory_remote_generic_repository" "cache" {
  key                 = "${var.repository_prefix}-remote"
  url                 = var.upstream_url
  username            = "replication-upstream-user-${var.revision}"
  password_wo         = var.upstream_password
  password_wo_version = tostring(var.revision)
}

resource "artifactory_local_repository_single_replication" "push" {
  repo_key                            = artifactory_local_generic_repository.source.key
  url                                 = var.target_url
  username                            = "replicator-${var.revision}"
  password                            = var.target_password
  cron_exp                            = var.revision == 1 ? "0 0 2 * * ?" : "0 30 3 * * ?"
  enabled                             = var.revision == 2
  enable_event_replication            = false
  check_binary_existence_in_filestore = false
  socket_timeout_millis               = var.revision == 1 ? 15000 : 25000
  sync_deletes                        = var.revision == 2
  sync_properties                     = var.revision == 1
  sync_statistics                     = var.revision == 2 && var.sync_statistics
  include_path_prefix_pattern         = var.revision == 1 ? "org/**" : "release/**"
  exclude_path_prefix_pattern         = "**/temporary/**"
  proxy                               = var.proxy_key
  disable_proxy                       = var.revision == 2
}

resource "artifactory_remote_repository_replication" "pull" {
  repo_key                            = artifactory_remote_generic_repository.cache.key
  cron_exp                            = var.revision == 1 ? "0 0 2 * * ?" : "0 30 3 * * ?"
  enabled                             = var.revision == 2
  enable_event_replication            = false
  check_binary_existence_in_filestore = false
  sync_deletes                        = var.revision == 2
  sync_properties                     = var.revision == 1
  include_path_prefix_pattern         = var.revision == 1 ? "org/**" : "release/**"
  exclude_path_prefix_pattern         = "**/temporary/**"
}
