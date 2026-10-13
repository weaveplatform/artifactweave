output "bucket" {
  description = "Bucket for blob.gcs.bucket and ARTIFACTWEAVE_TEST_GCS_BUCKET."
  value       = google_storage_bucket.blobs.name
}

output "service_account" {
  description = "Service account for blob.gcs.google_access_id and the conformance workflow."
  value       = google_service_account.app.email
}

output "workload_identity_provider" {
  description = "Provider for google-github-actions/auth (repository variable CONFORMANCE_GCP_WIF_PROVIDER)."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "store_prefix" {
  value = var.store_prefix
}
