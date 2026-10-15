# Regional Cloud Storage buckets as members of one artifactweave mirror
# (decision 0028): every blob is written to each, an upload succeeds once
# write_quorum of them hold it, and reads prefer the member in this
# instance's zone. No bucket replicates on its own: a dual-region bucket's
# copies are invisible to the mirror and never count toward its quorum.

resource "random_id" "suffix" {
  byte_length = 4
}

locals {
  members = {
    for l in var.locations : l => {
      store  = "gcs_${replace(l, "-", "_")}"
      bucket = "${var.name}-${random_id.suffix.hex}-${l}"
    }
  }
}

module "member" {
  source   = "../../modules/gcp/storage"
  for_each = local.members

  name              = each.value.bucket
  location          = each.key
  principals        = { app = var.app_service_account }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  labels            = var.labels
}
