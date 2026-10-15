# One Cloud Storage bucket that Cloud Storage replicates across two regions
# (a dual-region), for operators who want the cloud's own replication rather
# than artifactweave's mirror. artifactweave sees one store; Cloud Storage
# copies every object to the second region.

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  # Default replication targets 99.9% of new objects within an hour; turbo
  # replication targets 15 minutes and is billed per GB written (research
  # 20).
  rpo = var.turbo_replication ? "ASYNC_TURBO" : "DEFAULT"
}

module "bucket" {
  source = "../../modules/gcp/storage"

  name              = "${var.name}-${random_id.suffix.hex}"
  location          = var.location
  data_locations    = var.data_locations
  rpo               = local.rpo
  principals        = { app = var.app_service_account }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  labels            = var.labels
}
