# How the module reads its inputs, on a mocked plan: the host it serves, the
# certificate and records a host of your own brings, the checks that fail at
# plan rather than at apply, and the outputs a scenario reads.

mock_provider "aws" {}

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
  target          = aws_route53_record.validation
  override_during = plan
  values = {
    fqdn = "_0a1b2c3d.cdn.example.com"
  }
}

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

run "without_a_domain_the_distribution_host_serves" {
  command = plan

  assert {
    condition = (
      output.domain == "d111111abcdef8.cloudfront.net" &&
      one(aws_cloudfront_distribution.cdn.viewer_certificate).cloudfront_default_certificate &&
      length(aws_cloudfront_distribution.cdn.aliases) == 0 &&
      length(aws_acm_certificate.cdn) == 0 && length(aws_route53_record.alias) == 0
    )
    error_message = "with no domain_name, artifactweave must sign for the *.cloudfront.net host, with no certificate or records of its own"
  }
}

run "a_domain_gets_a_certificate_in_us_east_1_and_alias_records" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
    zone_id     = "Z0123456789EXAMPLE"
  }

  assert {
    condition = (
      output.domain == "cdn.example.com" &&
      aws_cloudfront_distribution.cdn.aliases == toset(["cdn.example.com"]) &&
      aws_acm_certificate.cdn[0].region == "us-east-1" &&
      aws_acm_certificate.cdn[0].validation_method == "DNS" &&
      aws_acm_certificate_validation.cdn[0].region == "us-east-1" &&
      aws_acm_certificate_validation.cdn[0].validation_record_fqdns == toset(["_0a1b2c3d.cdn.example.com"])
    )
    error_message = "a host of your own needs a DNS-validated certificate in us-east-1, the only region CloudFront takes one from"
  }

  assert {
    condition = (
      [aws_route53_record.validation[0].zone_id, aws_route53_record.validation[0].name, aws_route53_record.validation[0].type] == ["Z0123456789EXAMPLE", "_0a1b2c3d.cdn.example.com.", "CNAME"] &&
      aws_route53_record.validation[0].records == toset(["_4e5f6a7b.acm-validations.aws."])
    )
    error_message = "the certificate's validation record must go into zone_id as ACM asks for it"
  }

  assert {
    condition = alltrue([
      for type in ["A", "AAAA"] :
      aws_route53_record.alias[type].zone_id == "Z0123456789EXAMPLE" &&
      aws_route53_record.alias[type].name == "cdn.example.com" &&
      one(aws_route53_record.alias[type].alias).name == "d111111abcdef8.cloudfront.net" &&
      one(aws_route53_record.alias[type].alias).zone_id == "Z2FDTNDATAQYW2"
    ])
    error_message = "domain_name must resolve, over IPv4 and IPv6, to the distribution"
  }
}

run "price_class_reaches_the_distribution" {
  command = plan

  variables {
    price_class = "PriceClass_100"
  }

  assert {
    condition     = aws_cloudfront_distribution.cdn.price_class == "PriceClass_100"
    error_message = "the distribution must serve from the edge locations price_class names"
  }
}

run "price_class_defaults_to_every_edge" {
  command = plan

  assert {
    condition     = aws_cloudfront_distribution.cdn.price_class == "PriceClass_All"
    error_message = "with no price_class, every edge location serves"
  }
}

run "store_prefix_is_normalised_as_the_backend_does" {
  command = plan

  variables {
    store_prefix = "/artifacts"
  }

  assert {
    condition     = output.bucket_policy_statement.Resource == "arn:aws:s3:::example-0a1b2c3d/artifacts/sha256/*"
    error_message = "store_prefix must be normalised as the backend does, without a leading slash and with a trailing one"
  }
}

run "outputs_name_the_distribution_and_the_keys" {
  command = plan

  assert {
    condition = (
      [output.distribution_domain, output.distribution_id, output.distribution_arn] == [
        "d111111abcdef8.cloudfront.net", "EDFDVBD6EXAMPLE", "arn:aws:cloudfront::111122223333:distribution/EDFDVBD6EXAMPLE",
      ] &&
      output.key_pair_ids == { artifactweave = "K2JCJMDEHXQW5F" }
    )
    error_message = "the outputs must name the distribution and each public key's ID by its name"
  }
}

run "names_start_with_name" {
  command = plan

  assert {
    condition = (
      aws_cloudfront_origin_access_control.blobs.name == "example-0a1b2c3d" &&
      aws_cloudfront_cache_policy.blobs.name == "example-0a1b2c3d" &&
      aws_cloudfront_key_group.signers.name == "example-0a1b2c3d" &&
      aws_cloudfront_public_key.signers["artifactweave"].name_prefix == "example-0a1b2c3d-artifactweave-"
    )
    error_message = "every CloudFront name must start with name, which is how a reaper finds what a lost state left behind"
  }
}

run "name_is_checked" {
  command = plan

  variables {
    name = "has a space"
  }

  expect_failures = [var.name]
}

run "at_least_one_public_key" {
  command = plan

  variables {
    public_keys = {}
  }

  expect_failures = [var.public_keys]
}

run "at_most_five_public_keys" {
  command = plan

  variables {
    public_keys = { for i in range(6) : "k${i}" => "-----BEGIN PUBLIC KEY-----\nk${i}\n-----END PUBLIC KEY-----\n" }
  }

  expect_failures = [var.public_keys]
}

run "public_key_names_are_checked" {
  command = plan

  variables {
    public_keys = { "Not A Name" = "-----BEGIN PUBLIC KEY-----\nexample\n-----END PUBLIC KEY-----\n" }
  }

  expect_failures = [var.public_keys]
}

run "a_private_key_is_refused" {
  command = plan

  variables {
    public_keys = { artifactweave = "-----BEGIN PRIVATE KEY-----\nexample\n-----END PRIVATE KEY-----\n" }
  }

  expect_failures = [var.public_keys]
}

run "domain_name_is_checked" {
  command = plan

  variables {
    domain_name = "https://cdn.example.com/"
    zone_id     = "Z0123456789EXAMPLE"
  }

  expect_failures = [var.domain_name]
}

run "domain_name_needs_zone_id" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
  }

  expect_failures = [var.zone_id]
}

run "zone_id_needs_domain_name" {
  command = plan

  variables {
    zone_id = "Z0123456789EXAMPLE"
  }

  expect_failures = [var.zone_id]
}

run "price_class_is_checked" {
  command = plan

  variables {
    price_class = "PriceClass_Cheapest"
  }

  expect_failures = [var.price_class]
}

run "tags_default_to_none" {
  command = plan

  variables {
    tags = {}
  }

  assert {
    condition     = length(aws_cloudfront_distribution.cdn.tags) == 0
    error_message = "with no tags, the distribution carries none"
  }
}
