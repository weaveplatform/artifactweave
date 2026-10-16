# The module's inputs: what it refuses, how it normalises store_prefix, and
# what it hands back for DNS served elsewhere. Checked on a plan against a
# mocked provider.

mock_provider "google" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

override_resource {
  target          = google_certificate_manager_dns_authorization.cdn
  override_during = plan
  values = {
    dns_resource_record = [{
      name = "_acme-challenge.cdn.example.com."
      type = "CNAME"
      data = "0a1b2c3d-example.4.authorize.certificatemanager.goog."
    }]
  }
}

override_resource {
  target          = google_compute_global_address.cdn
  override_during = plan
  values = {
    address = "203.0.113.10"
  }
}

variables {
  name          = "example-0a1b2c3d"
  origin_bucket = "example-0a1b2c3d"
  domain_name   = "cdn.example.com"
}

run "without_dns_zone_the_records_are_handed_back" {
  command = plan

  variables {
    dns_zone = null
  }

  assert {
    condition = (
      length(google_dns_record_set.authorization) == 0 &&
      length(google_dns_record_set.host) == 0 &&
      output.dns_records == [
        { name = "_acme-challenge.cdn.example.com.", type = "CNAME", rrdatas = ["0a1b2c3d-example.4.authorize.certificatemanager.goog."] },
        { name = "cdn.example.com.", type = "A", rrdatas = ["203.0.113.10"] },
      ]
    )
    error_message = "without dns_zone the module must create no records and output the two domain_name needs"
  }
}

run "the_fill_account_is_named_by_the_project_number" {
  command = plan

  assert {
    condition     = output.cdn_fill_service_account == "service-123456789012@cloud-cdn-fill.iam.gserviceaccount.com" && output.domain == "cdn.example.com"
    error_message = "the fill account must be the project's cloud-cdn-fill service agent, and domain the host given"
  }
}

run "store_prefix_is_normalised_as_the_backend_does" {
  command = plan

  variables {
    store_prefix   = "/artifacts"
    grant_cdn_fill = true
  }

  assert {
    condition     = google_storage_bucket_iam_member.cdn_fill[0].condition[0].expression == "resource.name.startsWith(\"projects/_/buckets/example-0a1b2c3d/objects/artifacts/sha256/\")"
    error_message = "store_prefix must lose its leading slash and gain a trailing one, as blob.gcs.prefix does, so the grant covers the blobs artifactweave writes"
  }
}

run "the_bucket_root_without_a_prefix" {
  command = plan

  variables {
    grant_cdn_fill = true
  }

  assert {
    condition     = google_storage_bucket_iam_member.cdn_fill[0].condition[0].expression == "resource.name.startsWith(\"projects/_/buckets/example-0a1b2c3d/objects/sha256/\")"
    error_message = "without store_prefix the grant must cover sha256/ at the bucket's root"
  }
}

run "name_must_start_with_a_letter" {
  command = plan

  variables {
    name = "0a1b2c3d-example"
  }

  expect_failures = [var.name]
}

run "name_must_fit_63_characters" {
  command = plan

  variables {
    name = "a-name-that-is-much-too-long-for-a-compute-engine-resource-name-x"
  }

  expect_failures = [var.name]
}

run "domain_name_must_be_a_host" {
  command = plan

  variables {
    domain_name = "https://cdn.example.com/"
  }

  expect_failures = [var.domain_name]
}

run "the_cache_age_is_at_most_a_year" {
  command = plan

  variables {
    signed_url_cache_max_age_sec = 31536001
  }

  expect_failures = [var.signed_url_cache_max_age_sec]
}

run "labels_are_optional" {
  command = plan

  variables {
    labels = {}
  }

  assert {
    condition     = length(google_compute_global_forwarding_rule.cdn.labels) == 0
    error_message = "with no labels, the forwarding rule must carry none"
  }
}
