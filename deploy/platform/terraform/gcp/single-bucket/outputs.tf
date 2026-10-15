output "bucket" {
  description = "Bucket for blob.gcs.bucket and ARTIFACTWEAVE_TEST_GCS_BUCKET."
  value       = google_storage_bucket.blobs.name
}

output "service_account" {
  description = "The service account artifactweave runs as (the bootstrap's conformance account unless app_service_account names another), for blob.gcs.google_access_id and ARTIFACTWEAVE_TEST_GCS_ACCESS_ID."
  value       = local.app_service_account
}

output "store_prefix" {
  description = "Object prefix the local run writes under (blob.gcs.prefix)."
  value       = var.store_prefix
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for this bucket, signing download URLs as the app service account."
  value = yamlencode({
    blob = {
      backend = "gcs"
      gcs = {
        bucket           = google_storage_bucket.blobs.name
        prefix           = var.store_prefix
        google_access_id = local.app_service_account
      }
    }
  })
}

output "storage_host" {
  description = "Host a downloadRedirect repository's signed URLs point at."
  value       = "storage.googleapis.com"
}

output "expires_at" {
  description = "The expires-at label (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
