# Cloud CDN in front of one modules/gcp/storage bucket, serving
# artifactweave's blobs to URLs it signs (decision 0029): a backend bucket
# with Cloud CDN behind a global external Application Load Balancer, with a
# Certificate Manager certificate for domain_name. gcp/cdn composes it; it
# holds no provider block.
#
# Cloud CDN does not refuse unsigned requests itself: it refuses a bad or
# expired signature, and forwards an unsigned cache miss to the bucket, which
# stays private and refuses it. Only Cloud CDN's fill account may read the
# blobs, and only once grant_cdn_fill is on: Google creates that account
# with a project's first signed URL key, which is added with gcloud after
# apply so the key never reaches the state (decision 0030, Keys).

locals {
  # As the gcs backend normalises blob.gcs.prefix, so the grant matches the
  # objects it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"

  cdn_fill = "service-${data.google_project.this.number}@cloud-cdn-fill.iam.gserviceaccount.com"
  records  = var.dns_zone != null
}

# The project number names Cloud CDN's fill account.
data "google_project" "this" {}

# Cloud CDN's fill account may read the committed blobs and nothing else: not
# upload parts, not anything else in the bucket. IAM members are not
# authoritative, so this grant sits beside the storage module's own.
resource "google_storage_bucket_iam_member" "cdn_fill" {
  count = var.grant_cdn_fill ? 1 : 0

  bucket = var.origin_bucket
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${local.cdn_fill}"

  condition {
    title       = "committed-blobs"
    description = "Cloud CDN reads the blobs artifactweave has committed, under ${local.store_prefix}sha256/."
    expression  = "resource.name.startsWith(\"projects/_/buckets/${var.origin_bucket}/objects/${local.store_prefix}sha256/\")"
  }
}

# The cache key is Cloud Storage's own query parameters, the two response
# overrides artifactweave signs into each URL among them, listed here so the
# key holds them whether or not Google counts them as its own: without them,
# one item would be served with another's filename. Cloud CDN strips its
# signature parameters before the origin, and caches signed and unsigned
# responses apart, so an unsigned request never reaches a signed copy. A
# signed response is fresh for signed_url_cache_max_age_sec whatever the
# bucket's headers say; blobs never change, and the signature is checked on
# every request, cached or not. Error responses are not cached, and
# compression is off, so each blob is served byte for byte and ranged
# requests keep their offsets.
resource "google_compute_backend_bucket" "cdn" {
  name             = var.name
  description      = "artifactweave blobs from ${var.origin_bucket}"
  bucket_name      = var.origin_bucket
  enable_cdn       = true
  compression_mode = "DISABLED"

  cdn_policy {
    cache_mode                   = "CACHE_ALL_STATIC"
    default_ttl                  = 86400
    max_ttl                      = 31536000
    client_ttl                   = 86400
    negative_caching             = false
    signed_url_cache_max_age_sec = var.signed_url_cache_max_age_sec

    cache_key_policy {
      query_string_whitelist = ["response-content-disposition", "response-content-type"]
    }
  }
}

resource "google_compute_url_map" "cdn" {
  name            = var.name
  description     = "Every path to the artifactweave blobs."
  default_service = google_compute_backend_bucket.cdn.id
}

# TLS 1.2 at least, with Google's MODERN cipher profile.
resource "google_compute_ssl_policy" "cdn" {
  name            = var.name
  description     = "TLS 1.2 or later for the artifactweave CDN."
  profile         = "MODERN"
  min_tls_version = "TLS_1_2"
}

# A Google-managed certificate for domain_name, authorised by a DNS record
# rather than by the load balancer answering, so it can be issued before the
# host resolves. A global external Application Load Balancer takes
# Certificate Manager certificates through a map.
resource "google_certificate_manager_dns_authorization" "cdn" {
  name        = var.name
  description = "Authorises the artifactweave CDN certificate for ${var.domain_name}."
  domain      = var.domain_name
  labels      = var.labels
}

resource "google_dns_record_set" "authorization" {
  count = local.records ? 1 : 0

  managed_zone = var.dns_zone
  name         = google_certificate_manager_dns_authorization.cdn.dns_resource_record[0].name
  type         = google_certificate_manager_dns_authorization.cdn.dns_resource_record[0].type
  rrdatas      = [google_certificate_manager_dns_authorization.cdn.dns_resource_record[0].data]
  ttl          = 300
}

# Issuance starts when the certificate is created, so it waits for the
# authorisation record rather than retrying on Google's schedule.
resource "google_certificate_manager_certificate" "cdn" {
  name        = var.name
  description = "The artifactweave CDN host, ${var.domain_name}."
  labels      = var.labels

  managed {
    domains            = [var.domain_name]
    dns_authorizations = [google_certificate_manager_dns_authorization.cdn.id]
  }

  depends_on = [google_dns_record_set.authorization]
}

resource "google_certificate_manager_certificate_map" "cdn" {
  name        = var.name
  description = "The artifactweave CDN host's certificate."
  labels      = var.labels
}

resource "google_certificate_manager_certificate_map_entry" "cdn" {
  name         = var.name
  description  = "${var.domain_name} to its certificate."
  map          = google_certificate_manager_certificate_map.cdn.name
  hostname     = var.domain_name
  certificates = [google_certificate_manager_certificate.cdn.id]
  labels       = var.labels
}

resource "google_compute_target_https_proxy" "cdn" {
  name            = var.name
  description     = "HTTPS for the artifactweave CDN host, ${var.domain_name}."
  url_map         = google_compute_url_map.cdn.id
  certificate_map = "//certificatemanager.googleapis.com/${google_certificate_manager_certificate_map.cdn.id}"
  ssl_policy      = google_compute_ssl_policy.cdn.id

  depends_on = [google_certificate_manager_certificate_map_entry.cdn]
}

resource "google_compute_global_address" "cdn" {
  name        = var.name
  description = "The artifactweave CDN host, ${var.domain_name}."
  ip_version  = "IPV4"
  labels      = var.labels
}

# HTTPS only: there is no port 80 rule, so a URL never travels in the clear.
resource "google_compute_global_forwarding_rule" "cdn" {
  name                  = var.name
  description           = "HTTPS for the artifactweave CDN host, ${var.domain_name}."
  load_balancing_scheme = "EXTERNAL_MANAGED"
  ip_protocol           = "TCP"
  port_range            = "443"
  ip_address            = google_compute_global_address.cdn.id
  target                = google_compute_target_https_proxy.cdn.id
  labels                = var.labels
}

resource "google_dns_record_set" "host" {
  count = local.records ? 1 : 0

  managed_zone = var.dns_zone
  name         = "${var.domain_name}."
  type         = "A"
  rrdatas      = [google_compute_global_address.cdn.address]
  ttl          = 300
}
