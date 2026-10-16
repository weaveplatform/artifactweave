output "domain" {
  description = "The host artifactweave signs URLs for (blob.gcs.cdn.cloud_cdn.domain), domain_name."
  value       = var.domain_name
  # With dns_zone, the host answers only once its record exists.
  depends_on = [google_dns_record_set.host, google_compute_global_forwarding_rule.cdn]
}

output "backend_bucket" {
  description = "The backend bucket's name, which gcloud compute backend-buckets add-signed-url-key takes."
  value       = google_compute_backend_bucket.cdn.name
}

output "ip_address" {
  description = "The load balancer's address, which domain_name's A record names."
  value       = google_compute_global_address.cdn.address
}

output "cdn_fill_service_account" {
  description = "Cloud CDN's fill account, which grant_cdn_fill lets read the committed blobs; Google creates it with the project's first signed URL key."
  value       = local.cdn_fill
}

output "cdn_fill_member" {
  description = "The IAM member grant_cdn_fill has given read access to the committed blobs, Cloud CDN's fill account; null until it is on."
  value       = one(google_storage_bucket_iam_member.cdn_fill[*].member)
}

output "dns_records" {
  description = "The records domain_name needs, as name, type and data: the certificate's DNS authorisation and the host's A record. With dns_zone they are created there; without it, add them wherever the domain is served."
  value = [
    {
      name    = google_certificate_manager_dns_authorization.cdn.dns_resource_record[0].name
      type    = google_certificate_manager_dns_authorization.cdn.dns_resource_record[0].type
      rrdatas = [google_certificate_manager_dns_authorization.cdn.dns_resource_record[0].data]
    },
    {
      name    = "${var.domain_name}."
      type    = "A"
      rrdatas = [google_compute_global_address.cdn.address]
    },
  ]
}
