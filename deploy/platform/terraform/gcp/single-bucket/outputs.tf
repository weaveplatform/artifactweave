output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for the bucket. artifactweave authenticates with Application Default Credentials, as the app service account, which also signs download URLs."
  value = yamlencode({
    blob = {
      backend = "gcs"
      gcs = merge(
        { bucket = module.bucket.bucket, google_access_id = var.app_service_account },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
      )
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's signed URLs point: Cloud Storage's path-style endpoint."
  value       = "storage.googleapis.com"
}

output "bucket" {
  description = "The bucket's name (blob.gcs.bucket)."
  value       = module.bucket.bucket
}
