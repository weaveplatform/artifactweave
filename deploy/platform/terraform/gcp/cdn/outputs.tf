output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: one gcs store, the bucket, with Cloud CDN in front of it. It authenticates with Application Default Credentials, as the app service account, which also signs storage URLs, and signs Cloud CDN URLs with the key at cloud_cdn_key_file."
  value = yamlencode({
    blob = {
      backend = "gcs"
      gcs = merge(
        { bucket = module.storage.bucket, google_access_id = var.app_service_account },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
        {
          cdn = {
            cloud_cdn = {
              domain   = module.cdn.domain
              key_name = var.signed_url_key_name
              key_file = var.cloud_cdn_key_file
            }
          }
        },
      )
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect or cdnRedirect repository's signed URLs point: the CDN host, domain_name."
  value       = module.cdn.domain
}

output "origin_host" {
  description = "Cloud Storage's path-style endpoint, where a downloadRedirect repository's signed URLs point when a client sends X-JFrog-Download-Redirect-To: S3."
  value       = "storage.googleapis.com"
}

output "bucket" {
  description = "The bucket's name (blob.gcs.bucket)."
  value       = module.storage.bucket
}

output "backend_bucket" {
  description = "The backend bucket's name, which gcloud compute backend-buckets add-signed-url-key takes."
  value       = module.cdn.backend_bucket
}

output "ip_address" {
  description = "The load balancer's address, which domain_name's A record names."
  value       = module.cdn.ip_address
}

output "dns_records" {
  description = "The records domain_name needs, as name, type and data: the certificate's DNS authorisation and the host's A record. With dns_zone they are created there; without it, add them where the domain is served."
  value       = module.cdn.dns_records
}
