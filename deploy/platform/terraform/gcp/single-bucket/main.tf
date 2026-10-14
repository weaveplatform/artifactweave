# An acceptance bucket for artifactweave's GCS backend: the local container
# (compose.yaml) and the real-cloud conformance tests (cloud-conformance.yml)
# both use it, under separate prefixes. The APIs, the conformance service
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

# The data-plane service account gcp/bootstrap created and GitHub's
# cloud-conformance environment acts as.
data "google_service_account" "conformance" {
  account_id = var.conformance_service_account
}

# Object admin on this bucket only: create, read, compose and delete objects.
resource "google_storage_bucket_iam_member" "app" {
  bucket = google_storage_bucket.blobs.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${data.google_service_account.conformance.email}"
}
