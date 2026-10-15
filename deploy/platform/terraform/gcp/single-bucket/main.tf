# An acceptance bucket for artifactweave's GCS backend: a local run (make
# up, deploy/ci/local.sh) and the real-cloud conformance tests
# (cloud-conformance.yml) both use it, under separate prefixes. The APIs, the conformance service
# account and GitHub's trust in it live in gcp/bootstrap, which outlives this
# scenario; apply the bootstrap first.

resource "random_id" "suffix" {
  byte_length = 4
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - GCP-0066: Google-managed encryption keys are enough for acceptance data;
#   a customer-managed key would add a KMS key ring the tests never exercise.
# - GCP-0077: no access-log bucket. Cloud Audit Logs record the
#   administrative calls, and object access logs need a second bucket.
# - GCP-0078: no versioning. Blobs are content-addressed and never
#   rewritten, and versioning would keep every deleted upload part, the cost
#   soft_delete_policy below turns off (research 18).
#trivy:ignore:GCP-0066
#trivy:ignore:GCP-0077
#trivy:ignore:GCP-0078
resource "google_storage_bucket" "blobs" {
  name                        = "${var.name}-${random_id.suffix.hex}"
  location                    = upper(var.region)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  # Scenario runs (platform-scenarios.yml) label the bucket with the
  # scenario, the instance and expires-at, which reap matches.
  labels = var.scenario_labels
  # Acceptance data only: destroy removes the objects with the bucket.
  force_destroy = true

  # The backend composes temporary parts and deletes them; with soft delete
  # every deleted part would be billed for the retention period
  # (research 18).
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

# The service account artifactweave runs as. Locally that is the data-plane
# account gcp/bootstrap created, which GitHub's cloud-conformance environment
# also acts as. A scenario run names the deployer instead: it applies this
# root and runs artifactweave in the same job, and it cannot read service
# accounts, so the lookup is skipped when the email is given.
data "google_service_account" "conformance" {
  count      = var.app_service_account == "" ? 1 : 0
  account_id = var.conformance_service_account
}

locals {
  app_service_account = var.app_service_account != "" ? var.app_service_account : data.google_service_account.conformance[0].email
}

# Object admin on this bucket only: create, read, compose and delete objects.
resource "google_storage_bucket_iam_member" "app" {
  bucket = google_storage_bucket.blobs.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${local.app_service_account}"
}
