# The CDN's security baseline (decision 0029), checked on a plan against a
# mocked provider: no credentials and no calls to Google. Scenarios that call
# this module check how they compose it, not these again. One assertion per
# run, so a failure names the invariant it breaks.

mock_provider "google" {}

# The project number, which names Cloud CDN's fill account.
override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

# Computed IDs, the address and the authorisation record, which a mocked
# provider would leave unknown at plan.
override_resource {
  target          = google_compute_backend_bucket.cdn
  override_during = plan
  values = {
    id = "projects/example-project/global/backendBuckets/example-0a1b2c3d"
  }
}

override_resource {
  target          = google_compute_url_map.cdn
  override_during = plan
  values = {
    id = "projects/example-project/global/urlMaps/example-0a1b2c3d"
  }
}

override_resource {
  target          = google_compute_ssl_policy.cdn
  override_during = plan
  values = {
    id = "projects/example-project/global/sslPolicies/example-0a1b2c3d"
  }
}

override_resource {
  target          = google_certificate_manager_dns_authorization.cdn
  override_during = plan
  values = {
    id = "projects/example-project/locations/global/dnsAuthorizations/example-0a1b2c3d"
    dns_resource_record = [{
      name = "_acme-challenge.cdn.example.com."
      type = "CNAME"
      data = "0a1b2c3d-example.4.authorize.certificatemanager.goog."
    }]
  }
}

override_resource {
  target          = google_certificate_manager_certificate.cdn
  override_during = plan
  values = {
    id = "projects/example-project/locations/global/certificates/example-0a1b2c3d"
  }
}

override_resource {
  target          = google_certificate_manager_certificate_map.cdn
  override_during = plan
  values = {
    id = "projects/example-project/locations/global/certificateMaps/example-0a1b2c3d"
  }
}

override_resource {
  target          = google_compute_target_https_proxy.cdn
  override_during = plan
  values = {
    id = "projects/example-project/global/targetHttpsProxies/example-0a1b2c3d"
  }
}

override_resource {
  target          = google_compute_global_address.cdn
  override_during = plan
  values = {
    id      = "projects/example-project/global/addresses/example-0a1b2c3d"
    address = "203.0.113.10"
  }
}

variables {
  name          = "example-0a1b2c3d"
  origin_bucket = "example-0a1b2c3d"
  store_prefix  = "local"
  domain_name   = "cdn.example.com"
  dns_zone      = "example-zone"
}

run "the_backend_bucket_serves_the_origin_bucket_through_cloud_cdn" {
  command = plan

  assert {
    condition     = google_compute_backend_bucket.cdn.bucket_name == "example-0a1b2c3d" && google_compute_backend_bucket.cdn.enable_cdn
    error_message = "the backend bucket must serve the origin bucket with Cloud CDN on"
  }
}

run "every_path_reaches_the_backend_bucket" {
  command = plan

  assert {
    condition     = google_compute_url_map.cdn.default_service == "projects/example-project/global/backendBuckets/example-0a1b2c3d" && length(google_compute_url_map.cdn.host_rule) == 0 && length(google_compute_url_map.cdn.path_matcher) == 0
    error_message = "the URL map must send every path to the backend bucket, so a URL's path is the object key and artifactweave needs no path_prefix"
  }
}

run "only_https_is_served" {
  command = plan

  assert {
    condition = (
      google_compute_global_forwarding_rule.cdn.port_range == "443" &&
      google_compute_global_forwarding_rule.cdn.ip_protocol == "TCP" &&
      google_compute_global_forwarding_rule.cdn.load_balancing_scheme == "EXTERNAL_MANAGED" &&
      google_compute_global_forwarding_rule.cdn.target == "projects/example-project/global/targetHttpsProxies/example-0a1b2c3d" &&
      google_compute_global_forwarding_rule.cdn.ip_address == "projects/example-project/global/addresses/example-0a1b2c3d"
    )
    error_message = "the one forwarding rule must take HTTPS on port 443 to the HTTPS proxy, at the module's address, so a signed URL never travels in the clear"
  }
}

run "tls_1_2_at_least" {
  command = plan

  assert {
    condition = (
      google_compute_ssl_policy.cdn.min_tls_version == "TLS_1_2" &&
      google_compute_ssl_policy.cdn.profile == "MODERN" &&
      google_compute_target_https_proxy.cdn.ssl_policy == "projects/example-project/global/sslPolicies/example-0a1b2c3d"
    )
    error_message = "the HTTPS proxy must refuse TLS below 1.2, with Google's MODERN profile"
  }
}

run "the_host_is_served_with_its_managed_certificate" {
  command = plan

  assert {
    condition = (
      google_certificate_manager_certificate.cdn.managed[0].domains == tolist(["cdn.example.com"]) &&
      google_certificate_manager_certificate.cdn.managed[0].dns_authorizations == tolist(["projects/example-project/locations/global/dnsAuthorizations/example-0a1b2c3d"]) &&
      google_certificate_manager_dns_authorization.cdn.domain == "cdn.example.com" &&
      google_certificate_manager_certificate_map_entry.cdn.hostname == "cdn.example.com" &&
      google_certificate_manager_certificate_map_entry.cdn.certificates == tolist(["projects/example-project/locations/global/certificates/example-0a1b2c3d"]) &&
      google_compute_target_https_proxy.cdn.certificate_map == "//certificatemanager.googleapis.com/projects/example-project/locations/global/certificateMaps/example-0a1b2c3d" &&
      google_compute_target_https_proxy.cdn.url_map == "projects/example-project/global/urlMaps/example-0a1b2c3d"
    )
    error_message = "the HTTPS proxy must serve domain_name with a Google-managed certificate, authorised by DNS, through the module's certificate map"
  }
}

run "the_fill_account_is_not_granted_until_asked" {
  command = plan

  assert {
    condition     = length(google_storage_bucket_iam_member.cdn_fill) == 0 && output.cdn_fill_member == null
    error_message = "without grant_cdn_fill nothing may be granted: Google creates Cloud CDN's fill account only with a project's first signed URL key"
  }
}

run "the_fill_account_reads_the_committed_blobs_only" {
  command = plan

  variables {
    grant_cdn_fill = true
  }

  assert {
    condition = (
      google_storage_bucket_iam_member.cdn_fill[0].bucket == "example-0a1b2c3d" &&
      google_storage_bucket_iam_member.cdn_fill[0].role == "roles/storage.objectViewer" &&
      google_storage_bucket_iam_member.cdn_fill[0].member == "serviceAccount:service-123456789012@cloud-cdn-fill.iam.gserviceaccount.com" &&
      google_storage_bucket_iam_member.cdn_fill[0].condition[0].expression == "resource.name.startsWith(\"projects/_/buckets/example-0a1b2c3d/objects/local/sha256/\")" &&
      output.cdn_fill_member == "serviceAccount:service-123456789012@cloud-cdn-fill.iam.gserviceaccount.com"
    )
    error_message = "Cloud CDN's fill account may read the committed blobs under <store_prefix>sha256/ and nothing else, and no one else may be granted anything here"
  }
}

run "no_viewer_is_granted_the_bucket" {
  command = plan

  variables {
    grant_cdn_fill = true
  }

  # The bucket stays private: Cloud CDN does not refuse unsigned requests, so
  # a public bucket, or a grant to anyone but the fill account, would serve
  # every blob through the CDN without a signature.
  assert {
    condition     = alltrue([for m in google_storage_bucket_iam_member.cdn_fill : !contains(["allUsers", "allAuthenticatedUsers"], m.member) && endswith(m.member, "@cloud-cdn-fill.iam.gserviceaccount.com")])
    error_message = "the module may grant the bucket to Cloud CDN's fill account only, never to allUsers or allAuthenticatedUsers"
  }
}

run "the_records_go_in_dns_zone" {
  command = plan

  assert {
    condition = (
      google_dns_record_set.authorization[0].managed_zone == "example-zone" &&
      google_dns_record_set.authorization[0].name == "_acme-challenge.cdn.example.com." &&
      google_dns_record_set.authorization[0].type == "CNAME" &&
      google_dns_record_set.authorization[0].rrdatas == tolist(["0a1b2c3d-example.4.authorize.certificatemanager.goog."]) &&
      google_dns_record_set.host[0].managed_zone == "example-zone" &&
      google_dns_record_set.host[0].name == "cdn.example.com." &&
      google_dns_record_set.host[0].type == "A" &&
      google_dns_record_set.host[0].rrdatas == tolist(["203.0.113.10"])
    )
    error_message = "with dns_zone, the certificate's authorisation record and the host's A record to the module's address must go in that zone"
  }
}

run "labels_reach_every_resource_that_takes_them" {
  command = plan

  variables {
    labels = { artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition = alltrue([for l in [
      google_compute_global_address.cdn.labels,
      google_compute_global_forwarding_rule.cdn.labels,
      google_certificate_manager_dns_authorization.cdn.labels,
      google_certificate_manager_certificate.cdn.labels,
      google_certificate_manager_certificate_map.cdn.labels,
      google_certificate_manager_certificate_map_entry.cdn.labels,
    ] : l == tomap({ artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" })])
    error_message = "the address, the forwarding rule and every Certificate Manager piece must carry the labels given"
  }
}
