# Remote state for this bootstrap (bootstrap/gcp) and every scenario run
# (scenarios/<cloud>/<scenario>/<instance>), so an apply dispatch and a later
# destroy dispatch find the same state (decision 0030). It holds kilobytes;
# GCS has no fixed fee, so it stays when scenarios are torn down.
#
# Accepted trivy findings (deploy/check.sh), each with its reason:
# - GCP-0066: Google-managed encryption keys. State is readable only by the
#   operator and the deployer, and a customer-managed key would add a KMS key
#   ring with a monthly fee per key version.
# - GCP-0077: no access-log bucket. Cloud Audit Logs record the
#   administrative calls, and object access logs need a second bucket.
#trivy:ignore:GCP-0066
#trivy:ignore:GCP-0077
resource "google_storage_bucket" "state" {
  # Fixed: backend.tf names it, and a backend block cannot read a variable.
  name                        = "artifactweave-tfstate-${data.google_project.this.number}"
  location                    = upper(var.region)
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  # State is the one thing here that must not vanish with a mistaken
  # destroy: the bucket refuses while it holds objects.
  force_destroy = false

  # Every apply writes a new version; old ones are the undo history.
  versioning {
    enabled = true
  }

  lifecycle_rule {
    condition {
      num_newer_versions = 20
      with_state         = "ARCHIVED"
    }
    action {
      type = "Delete"
    }
  }
  lifecycle_rule {
    condition {
      days_since_noncurrent_time = 90
    }
    action {
      type = "Delete"
    }
  }

  depends_on = [google_project_service.this]
}
