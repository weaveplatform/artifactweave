# One GCS bucket that Google replicates across two regions (a dual-region),
# for operators who want the cloud's own replication rather than
# artifactweave's mirror. artifactweave sees one store; Google copies every
# object to the second region. Apply gcp/bootstrap first: it holds the APIs,
# the conformance service account and GitHub's trust.

resource "random_id" "suffix" {
  byte_length = 4
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - GCP-0066: Google-managed encryption keys; a customer-managed key would add
#   a KMS key ring in both regions that the scenario never exercises.
# - GCP-0077: no access-log bucket. Cloud Audit Logs record the
#   administrative calls, and object access logs need a second bucket.
# - GCP-0078: no versioning. Blobs are content-addressed and never
#   rewritten, and versioning would keep every deleted upload part
#   (research 18).
#trivy:ignore:GCP-0066
#trivy:ignore:GCP-0077
#trivy:ignore:GCP-0078
resource "google_storage_bucket" "blobs" {
  name = "${var.name}-${random_id.suffix.hex}"
  # A predefined dual-region (EUR4 is Finland and the Netherlands), or a
  # multi-region such as EU with data_locations naming the two regions of a
  # configurable dual-region.
  location      = upper(var.location)
  storage_class = "STANDARD"
  # Default replication targets 99.9% of new objects within an hour; turbo
  # replication, dual-regions only, targets 15 minutes and is billed per GB
  # written (research 20).
  rpo                         = var.turbo_replication ? "ASYNC_TURBO" : "DEFAULT"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  # Scenario runs (platform-scenarios.yml) label the bucket with the
  # scenario, the instance and expires-at, which reap matches.
  labels = var.scenario_labels
  # Reference data only: destroy removes the objects with the bucket.
  force_destroy = true

  dynamic "custom_placement_config" {
    for_each = length(var.data_locations) > 0 ? [var.data_locations] : []
    content {
      data_locations = [for l in custom_placement_config.value : upper(l)]
    }
  }

  # The backend composes temporary parts and deletes them; with soft delete
  # every deleted part would be billed, in both regions, for the retention
  # period (research 18).
  soft_delete_policy {
    retention_duration_seconds = 0
  }

  # Backstops for runs that stop before cleaning up: abandoned upload parts
  # and conformance prefixes.
  lifecycle_rule {
    condition {
      age            = 2
      matches_prefix = ["${var.store_prefix}_uploads/"]
    }
    action {
      type = "Delete"
    }
  }
  lifecycle_rule {
    condition {
      age            = 1
      matches_prefix = ["conformance/"]
    }
    action {
      type = "Delete"
    }
  }
}

# The service account artifactweave runs as: the bootstrap's conformance
# account locally, the deployer in a scenario run (which applies this root
# and runs artifactweave in the same job, and cannot read service accounts,
# so the lookup is skipped when the email is given).
data "google_service_account" "conformance" {
  count      = var.app_service_account == "" ? 1 : 0
  account_id = var.conformance_service_account
}

locals {
  app_service_account = var.app_service_account != "" ? var.app_service_account : data.google_service_account.conformance[0].email
}

# Object admin on this bucket only. The replica in the second region is the
# same bucket, so the grant covers it too.
resource "google_storage_bucket_iam_member" "app" {
  bucket = google_storage_bucket.blobs.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${local.app_service_account}"
}
