output "bucket" {
  description = "Bucket for blob.gcs.bucket."
  value       = google_storage_bucket.blobs.name
}

output "location" {
  description = "The bucket's dual-region (or multi-region, with data_locations)."
  value       = google_storage_bucket.blobs.location
}

output "rpo" {
  description = "DEFAULT (99.9% of new objects replicated within an hour) or ASYNC_TURBO (15 minutes)."
  value       = google_storage_bucket.blobs.rpo
}

output "service_account" {
  description = "The service account artifactweave runs as (the bootstrap's conformance account unless app_service_account names another), for blob.gcs.google_access_id."
  value       = local.app_service_account
}

output "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix)."
  value       = var.store_prefix
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for this bucket. It is one store: the second region is Google's copy, not a mirror member."
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
  description = "Host a downloadRedirect repository's signed URLs point at. One signed URL serves every location of the bucket."
  value       = "storage.googleapis.com"
}

output "expires_at" {
  description = "The expires-at label (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
