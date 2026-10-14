# An acceptance bucket for artifactweave's GCS backend: the local container
# (compose.yaml) and the real-cloud conformance tests (cloud-conformance.yml)
# both use it, under separate prefixes.

resource "google_project_service" "this" {
  # iamcredentials signs URLs and mints impersonated tokens; sts exchanges
  # GitHub's OIDC token for one.
  for_each = toset([
    "storage.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "cloudresourcemanager.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

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

  depends_on = [google_project_service.this]
}

resource "google_service_account" "app" {
  account_id   = var.name
  display_name = "artifactweave acceptance"
  description  = "Used by the local artifactweave container and the real-cloud conformance tests."
  depends_on   = [google_project_service.this]
}

# Object admin on this bucket only: create, read, compose and delete objects.
resource "google_storage_bucket_iam_member" "app" {
  bucket = google_storage_bucket.blobs.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.app.email}"
}

data "google_client_openid_userinfo" "me" {}

# No keys (the organisation forbids them). The local container acts as the
# service account by impersonation from the operator's own login (up.sh
# writes the impersonated credentials file), and signs download URLs through
# IAM signBlob as itself.
resource "google_service_account_iam_member" "operator_impersonates" {
  service_account_id = google_service_account.app.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "user:${data.google_client_openid_userinfo.me.email}"
}

resource "google_service_account_iam_member" "signs_as_itself" {
  service_account_id = google_service_account.app.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.app.email}"
}

# GitHub Actions runs of one repository exchange their OIDC token for the
# service account (Workload Identity Federation): cloud-conformance.yml needs
# no stored credential.
resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github-actions"
  display_name              = "GitHub Actions"
  depends_on                = [google_project_service.this]
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "artifactweave"
  display_name                       = "weaveplatform/artifactweave"
  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.repository" = "assertion.repository"
    "attribute.ref"        = "assertion.ref"
  }
  attribute_condition = "assertion.repository == '${var.github_repository}'"
  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

resource "google_service_account_iam_member" "github" {
  service_account_id = google_service_account.app.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository/${var.github_repository}"
}
