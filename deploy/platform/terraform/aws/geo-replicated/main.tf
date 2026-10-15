# A primary S3 bucket that S3 cross-region replication copies into a replica
# bucket in a second region, for operators who want the cloud's own
# replication rather than artifactweave's mirror. artifactweave writes to and
# signs URLs for the primary only; the replica is S3's copy, invisible to
# artifactweave, and the failover target.

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  # As the s3 backend normalises blob.s3.prefix, so the rule matches the
  # keys it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"

  # Only committed blobs replicate. Upload parts stay in the primary, where
  # its lifecycle rules remove them.
  replicated_prefix = "${local.store_prefix}sha256/"

  bucket = "${var.name}-${random_id.suffix.hex}"
}

module "primary" {
  source = "../../modules/aws/storage"

  name              = local.bucket
  region            = var.region
  principals        = { app = var.app_role_arn }
  store_prefix      = var.store_prefix
  versioning        = true
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  tags              = var.tags
}

# A failover writes here, so it has the same rules and grant as the primary.
module "replica" {
  source = "../../modules/aws/storage"

  name              = "${local.bucket}-r"
  region            = var.replica_region
  principals        = { app = var.app_role_arn }
  store_prefix      = var.store_prefix
  versioning        = true
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  tags              = var.tags
}

# Deletes do not replicate: a blob artifactweave's garbage collection removes
# from the primary stays in the replica until an operator removes it, so a
# failover never misses a blob a lagging delete would have taken.
module "replication" {
  source = "../../modules/aws/replication"

  name                     = "${trimsuffix(substr(var.name, 0, 40), "-")}-${random_id.suffix.hex}-repl"
  source_bucket            = module.primary
  destination_bucket       = module.replica
  prefix                   = local.replicated_prefix
  replication_time_control = var.replication_time_control
  permissions_boundary_arn = var.permissions_boundary_arn
  iam_role_path            = var.iam_role_path
  tags                     = var.tags
}
