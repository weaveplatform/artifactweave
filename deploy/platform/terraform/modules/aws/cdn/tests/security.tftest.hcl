# The distribution's security baseline (decision 0029), checked on a plan
# against a mocked provider: no credentials and no calls to AWS. Scenarios
# that call this module check how they compose it, not these again. One
# assertion per run, so a failure names the invariant it breaks.

mock_provider "aws" {}

# Computed IDs and ARNs, which a mocked provider would leave unknown at plan.
override_resource {
  target          = aws_cloudfront_distribution.cdn
  override_during = plan
  values = {
    id             = "EDFDVBD6EXAMPLE"
    arn            = "arn:aws:cloudfront::111122223333:distribution/EDFDVBD6EXAMPLE"
    domain_name    = "d111111abcdef8.cloudfront.net"
    hosted_zone_id = "Z2FDTNDATAQYW2"
  }
}

override_resource {
  target          = aws_cloudfront_origin_access_control.blobs
  override_during = plan
  values = {
    id = "E2QWRUHAPOMQZL"
  }
}

override_resource {
  target          = aws_cloudfront_key_group.signers
  override_during = plan
  values = {
    id = "4b4f2r1c-example"
  }
}

override_resource {
  target          = aws_cloudfront_public_key.signers
  override_during = plan
  values = {
    id = "K2JCJMDEHXQW5F"
  }
}

override_resource {
  target          = aws_acm_certificate.cdn
  override_during = plan
  values = {
    arn = "arn:aws:acm:us-east-1:111122223333:certificate/0a1b2c3d-example"
    domain_validation_options = [{
      domain_name           = "cdn.example.com"
      resource_record_name  = "_0a1b2c3d.cdn.example.com."
      resource_record_type  = "CNAME"
      resource_record_value = "_4e5f6a7b.acm-validations.aws."
    }]
  }
}

override_resource {
  target          = aws_acm_certificate_validation.cdn
  override_during = plan
  values = {
    certificate_arn = "arn:aws:acm:us-east-1:111122223333:certificate/0a1b2c3d-example"
  }
}

variables {
  name = "example-0a1b2c3d"
  origin_bucket = {
    bucket = "example-0a1b2c3d"
    arn    = "arn:aws:s3:::example-0a1b2c3d"
    region = "eu-west-2"
  }
  store_prefix = "local"
  public_keys = {
    artifactweave = "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEexample\n-----END PUBLIC KEY-----\n"
  }
}

run "the_bucket_is_read_through_origin_access_control" {
  command = plan

  assert {
    condition = (
      aws_cloudfront_origin_access_control.blobs.origin_access_control_origin_type == "s3" &&
      aws_cloudfront_origin_access_control.blobs.signing_behavior == "always" &&
      aws_cloudfront_origin_access_control.blobs.signing_protocol == "sigv4" &&
      one(aws_cloudfront_distribution.cdn.origin).origin_access_control_id == "E2QWRUHAPOMQZL" &&
      one(aws_cloudfront_distribution.cdn.origin).domain_name == "example-0a1b2c3d.s3.eu-west-2.amazonaws.com"
    )
    error_message = "CloudFront must sign every request to the bucket's regional endpoint, so the bucket can stay private"
  }
}

run "the_origin_path_is_empty" {
  command = plan

  assert {
    condition     = one(aws_cloudfront_distribution.cdn.origin).origin_path == ""
    error_message = "the origin must be the bucket's root, so a URL's path is the object key and artifactweave needs no path_prefix"
  }
}

run "only_signed_urls_are_served" {
  command = plan

  assert {
    condition = (
      tolist(one(aws_cloudfront_distribution.cdn.default_cache_behavior).trusted_key_groups) == tolist(["4b4f2r1c-example"]) &&
      length(aws_cloudfront_distribution.cdn.ordered_cache_behavior) == 0
    )
    error_message = "the distribution's only behaviour must require a URL signed by a key in the module's key group"
  }
}

run "the_key_group_trusts_every_key_given" {
  command = plan

  override_resource {
    target          = aws_cloudfront_public_key.signers["current"]
    override_during = plan
    values = {
      id = "K2JCJMDEHXQW5F"
    }
  }

  override_resource {
    target          = aws_cloudfront_public_key.signers["previous"]
    override_during = plan
    values = {
      id = "K3PREVIOUSEXAMP"
    }
  }

  variables {
    public_keys = {
      current  = "-----BEGIN PUBLIC KEY-----\ncurrent\n-----END PUBLIC KEY-----\n"
      previous = "-----BEGIN PUBLIC KEY-----\nprevious\n-----END PUBLIC KEY-----\n"
    }
  }

  assert {
    condition = (
      toset(aws_cloudfront_key_group.signers.items) == toset(["K2JCJMDEHXQW5F", "K3PREVIOUSEXAMP"]) &&
      [for k in ["current", "previous"] : aws_cloudfront_public_key.signers[k].encoded_key] == [
        "-----BEGIN PUBLIC KEY-----\ncurrent\n-----END PUBLIC KEY-----\n",
        "-----BEGIN PUBLIC KEY-----\nprevious\n-----END PUBLIC KEY-----\n",
      ]
    )
    error_message = "the key group must hold one public key per entry of public_keys, so a key can be rotated in before the old one goes"
  }
}

run "viewers_use_https_for_get_and_head_only" {
  command = plan

  assert {
    condition = (
      one(aws_cloudfront_distribution.cdn.default_cache_behavior).viewer_protocol_policy == "https-only" &&
      toset(one(aws_cloudfront_distribution.cdn.default_cache_behavior).allowed_methods) == toset(["GET", "HEAD"])
    )
    error_message = "the distribution must answer only GET and HEAD, and only over HTTPS, where a signed URL cannot be read in transit"
  }
}

run "cloudfront_reads_the_committed_blobs_only" {
  command = plan

  assert {
    condition = output.bucket_policy_statement == {
      Sid       = "CloudFrontReadsBlobs"
      Effect    = "Allow"
      Principal = { Service = "cloudfront.amazonaws.com" }
      Action    = "s3:GetObject"
      Resource  = "arn:aws:s3:::example-0a1b2c3d/local/sha256/*"
      Condition = { StringEquals = { "AWS:SourceArn" = "arn:aws:cloudfront::111122223333:distribution/EDFDVBD6EXAMPLE" } }
    }
    error_message = "the bucket policy statement must let this distribution, and no other, read the committed blobs under store_prefix and nothing else"
  }
}

run "a_custom_domain_gets_tls_1_2" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
    zone_id     = "Z0123456789EXAMPLE"
  }

  assert {
    condition = (
      one(aws_cloudfront_distribution.cdn.viewer_certificate).minimum_protocol_version == "TLSv1.2_2021" &&
      one(aws_cloudfront_distribution.cdn.viewer_certificate).ssl_support_method == "sni-only" &&
      one(aws_cloudfront_distribution.cdn.viewer_certificate).acm_certificate_arn == "arn:aws:acm:us-east-1:111122223333:certificate/0a1b2c3d-example"
    )
    error_message = "a host of your own must be served with the validated certificate, by SNI, with TLS 1.2 at least"
  }
}

run "tags_reach_the_distribution_and_the_certificate" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
    zone_id     = "Z0123456789EXAMPLE"
    tags        = { artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition = (
      aws_cloudfront_distribution.cdn.tags == tomap({ artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" }) &&
      aws_acm_certificate.cdn[0].tags == tomap({ artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" })
    )
    error_message = "the distribution and the certificate, the resources here that take tags, must carry the tags given"
  }
}
