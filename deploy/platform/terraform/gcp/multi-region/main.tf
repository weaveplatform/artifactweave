# Two regional GCS buckets as members of one artifactweave mirror (decision
# 0028): every blob is written to both, an upload succeeds once write_quorum
# of them hold it, and reads prefer the member in this instance's zone. The
# reference for regional resilience under artifactweave's control. Neither
# bucket replicates on its own: a dual-region bucket's copies are invisible
# to the mirror and never count toward its quorum. The APIs and the
# conformance service account live in gcp/bootstrap; apply it first.

resource "random_id" "suffix" {
  byte_length = 4
}

# The service account artifactweave runs as. Locally that is the data-plane
# account gcp/bootstrap created. A scenario run names the deployer instead:
# it applies this root and runs artifactweave in the same job, and it cannot
# read service accounts, so the lookup is skipped when the email is given.
data "google_service_account" "conformance" {
  count      = var.app_service_account == "" ? 1 : 0
  account_id = var.conformance_service_account
}

locals {
  app_service_account = var.app_service_account != "" ? var.app_service_account : data.google_service_account.conformance[0].email
  # The mirror's members in order: this instance's zone first.
  regions = [var.primary_region, var.secondary_region]
  members = {
    for r in local.regions : r => {
      store = "gcs_${replace(r, "-", "_")}"
      # 63 characters at most: awscn- (6), an instance (32), the suffix (8)
      # and a region (europe-west2, 12) with their hyphens make 60.
      bucket = "${var.name}-${random_id.suffix.hex}-${r}"
    }
  }
}

module "member" {
  source   = "../../modules/gcp/storage"
  for_each = local.members

  name                = each.value.bucket
  location            = each.key
  store_prefix        = var.store_prefix
  app_service_account = local.app_service_account
  labels              = var.scenario_labels
}
