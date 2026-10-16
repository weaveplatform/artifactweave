# How the edge caches blobs (decision 0029), on a mocked plan: what the cache
# key holds decides whether one item can be served with another's filename,
# and whether the signature fragments the cache.

mock_provider "aws" {}

variables {
  name = "example-0a1b2c3d"
  origin_bucket = {
    bucket = "example-0a1b2c3d"
    arn    = "arn:aws:s3:::example-0a1b2c3d"
    region = "eu-west-2"
  }
  public_keys = {
    artifactweave = "-----BEGIN PUBLIC KEY-----\nexample\n-----END PUBLIC KEY-----\n"
  }
}

run "the_cache_key_is_the_path_and_the_two_overrides" {
  command = plan

  assert {
    condition = (
      one(one(one(aws_cloudfront_cache_policy.blobs.parameters_in_cache_key_and_forwarded_to_origin).query_strings_config).query_strings).items == toset(["response-content-disposition", "response-content-type"]) &&
      one(one(aws_cloudfront_cache_policy.blobs.parameters_in_cache_key_and_forwarded_to_origin).query_strings_config).query_string_behavior == "whitelist"
    )
    error_message = "the cache key must hold the two response overrides and no other query string: never the signature, or every URL would miss"
  }
}

run "no_header_cookie_or_encoding_enters_the_cache_key" {
  command = plan

  assert {
    condition = (
      one(one(aws_cloudfront_cache_policy.blobs.parameters_in_cache_key_and_forwarded_to_origin).headers_config).header_behavior == "none" &&
      one(one(aws_cloudfront_cache_policy.blobs.parameters_in_cache_key_and_forwarded_to_origin).cookies_config).cookie_behavior == "none" &&
      !one(aws_cloudfront_cache_policy.blobs.parameters_in_cache_key_and_forwarded_to_origin).enable_accept_encoding_gzip &&
      !one(aws_cloudfront_cache_policy.blobs.parameters_in_cache_key_and_forwarded_to_origin).enable_accept_encoding_brotli
    )
    error_message = "no header, cookie or Accept-Encoding may enter the cache key: each client would get its own copy of a blob"
  }
}

run "the_distribution_uses_the_cache_policy_and_serves_blobs_unaltered" {
  command = plan

  override_resource {
    target          = aws_cloudfront_cache_policy.blobs
    override_during = plan
    values = {
      id = "658327ea-example"
    }
  }

  assert {
    condition = (
      one(aws_cloudfront_distribution.cdn.default_cache_behavior).cache_policy_id == "658327ea-example" &&
      !one(aws_cloudfront_distribution.cdn.default_cache_behavior).compress
    )
    error_message = "the distribution must cache by the module's policy and serve each blob byte for byte, uncompressed, so ranges keep their offsets"
  }
}

run "edge_copies_last_a_day_by_default" {
  command = plan

  assert {
    condition = [
      aws_cloudfront_cache_policy.blobs.min_ttl, aws_cloudfront_cache_policy.blobs.default_ttl, aws_cloudfront_cache_policy.blobs.max_ttl,
    ] == [0, 86400, 31536000]
    error_message = "blobs never change, so an edge copy may live a day by default and a year at most"
  }
}
