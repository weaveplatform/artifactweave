output "bucket" {
  description = "Bucket for blob.gcs.bucket and ARTIFACTWEAVE_TEST_GCS_BUCKET."
  value       = google_storage_bucket.blobs.name
}

output "service_account" {
  description = "The bootstrap's conformance service account, for blob.gcs.google_access_id and ARTIFACTWEAVE_TEST_GCS_ACCESS_ID."
  value       = data.google_service_account.conformance.email
}

output "store_prefix" {
  description = "Object prefix the local container writes under (blob.gcs.prefix)."
  value       = var.store_prefix
}
