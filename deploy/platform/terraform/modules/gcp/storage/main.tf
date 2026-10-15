# One private GCS bucket and object rights on it for the service accounts in
# var.principals. Scenarios call it once per store; it holds no provider
# block, so the caller's google provider and project apply (decision 0030).

locals {
  # As the gcs backend normalises blob.gcs.prefix, so the lifecycle rules
  # match the objects it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - GCP-0066: Google-managed encryption keys; a customer-managed key would
#   add a KMS key ring and its grants to manage.
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
  rpo                         = var.rpo
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = var.force_destroy
  labels                      = var.labels

  dynamic "custom_placement_config" {
    for_each = length(var.data_locations) > 0 ? [1] : []
    content {
      data_locations = [for l in var.data_locations : upper(l)]
    }
  }

  # The backend composes temporary parts and deletes them; with soft delete
  # every deleted part would be billed for the retention period
  # (research 18).
  soft_delete_policy {
    retention_duration_seconds = 0
  }

  # A backstop for runs that stop before cleaning up: abandoned upload
  # parts. The rules keep this order; the tests address them by position.
  lifecycle_rule {
    condition {
      age            = 2
      matches_prefix = ["${local.store_prefix}_uploads/"]
    }
    action {
      type = "Delete"
    }
  }

  dynamic "lifecycle_rule" {
    for_each = var.expiring_prefixes
    content {
      condition {
        age            = lifecycle_rule.value
        matches_prefix = [lifecycle_rule.key]
      }
      action {
        type = "Delete"
      }
    }
  }
}

# Object admin on this bucket only: create, list, read, compose and delete
# objects, and nothing on the bucket itself. Accepted trivy finding GCP-0007
# (a privileged role for a service account): objectAdmin is bound to this
# one bucket, not the project, and the backend needs all of it, deleting the
# temporary parts it composes included.
#trivy:ignore:GCP-0007
resource "google_storage_bucket_iam_member" "app" {
  for_each = var.principals
  bucket   = google_storage_bucket.blobs.name
  role     = "roles/storage.objectAdmin"
  member   = "serviceAccount:${each.value}"
}
