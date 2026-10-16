# How Cloud CDN caches the blobs (decision 0029), checked on a plan against a
# mocked provider. One assertion per run.

mock_provider "google" {}

variables {
  name          = "example-0a1b2c3d"
  origin_bucket = "example-0a1b2c3d"
  domain_name   = "cdn.example.com"
}

run "the_cache_key_holds_the_two_overrides" {
  command = plan

  assert {
    condition = (
      toset(google_compute_backend_bucket.cdn.cdn_policy[0].cache_key_policy[0].query_string_whitelist) == toset(["response-content-disposition", "response-content-type"]) &&
      length(coalesce(google_compute_backend_bucket.cdn.cdn_policy[0].cache_key_policy[0].include_http_headers, [])) == 0
    )
    error_message = "the cache key must hold response-content-disposition and response-content-type, so one item is never served with another's filename, and no header"
  }
}

run "signed_responses_are_cached_and_errors_are_not" {
  command = plan

  assert {
    condition = (
      google_compute_backend_bucket.cdn.cdn_policy[0].cache_mode == "CACHE_ALL_STATIC" &&
      google_compute_backend_bucket.cdn.cdn_policy[0].signed_url_cache_max_age_sec == 86400 &&
      google_compute_backend_bucket.cdn.cdn_policy[0].negative_caching == false
    )
    error_message = "a signed response must stay fresh for a day by default, and an error response must not be cached"
  }
}

run "the_cache_age_is_the_one_given" {
  command = plan

  variables {
    signed_url_cache_max_age_sec = 3600
  }

  assert {
    condition     = google_compute_backend_bucket.cdn.cdn_policy[0].signed_url_cache_max_age_sec == 3600
    error_message = "signed_url_cache_max_age_sec must reach the backend bucket's CDN policy"
  }
}

run "blobs_are_served_unaltered" {
  command = plan

  assert {
    condition     = google_compute_backend_bucket.cdn.compression_mode == "DISABLED"
    error_message = "Cloud CDN must serve each blob byte for byte, so ranged requests keep their offsets"
  }
}
