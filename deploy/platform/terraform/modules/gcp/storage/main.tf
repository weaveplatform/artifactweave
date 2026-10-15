# One private GCS bucket a scenario stores blobs in, with object rights for
# the service account artifactweave runs as. Scenarios call it once per store
# (multi-region calls it per region); it holds no provider block, so the
# caller's google provider and project apply (decision 0030).

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - GCP-0066: Google-managed encryption keys are enough for scenario data; a
#   customer-managed key would add a KMS key ring the tests never exercise.
# - GCP-0077: no access-log bucket. Cloud Audit Logs record the
#   administrative calls, and object access logs need a second bucket.
# - GCP-0078: no versioning. Blobs are content-addressed and never rewritten,
#   and versioning would keep every deleted upload part, the cost
#   soft_delete_policy below turns off (research 18).
#trivy:ignore:GCP-0066
#trivy:ignore:GCP-0077
#trivy:ignore:GCP-0078
resource "google_storage_bucket" "blobs" {
  name                        = var.name
  location                    = upper(var.location)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  labels                      = var.labels
  # Scenario data only: destroy removes the objects with the bucket.
  force_destroy = true

  # The backend composes temporary parts and deletes them; with soft delete
  # every deleted part would be billed for the retention period
  # (research 18).
  soft_delete_policy {
    retention_duration_seconds = 0
  }

  # A backstop for runs that stop before cleaning up: abandoned upload parts.
  lifecycle_rule {
    condition {
      age            = 2
      matches_prefix = ["${var.store_prefix}_uploads/"]
    }
    action {
      type = "Delete"
    }
  }
}

# Object admin on this bucket only: create, read, compose and delete objects.
# Accepted trivy finding GCP-0007 (a privileged role for a service account):
# objectAdmin is bound to this one bucket, not the project, and the backend
# needs all of it, deleting the temporary parts it composes included.
#trivy:ignore:GCP-0007
resource "google_storage_bucket_iam_member" "app" {
  bucket = google_storage_bucket.blobs.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${var.app_service_account}"
}
