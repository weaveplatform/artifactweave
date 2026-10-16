# How the scenario composes modules/aws/storage and modules/aws/cdn, checked
# on a plan against mocked providers (decision 0030, S10). The bucket's and
# the distribution's own baselines are the modules' tests.

mock_provider "aws" {}
mock_provider "random" {}

# Every name, and so the rendered configuration, comes from the random
# suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "0a1b2c3d"
  }
}

# The distribution's host and ARN, and the public key's ID, are computed.
override_resource {
  target          = module.cdn.aws_cloudfront_distribution.cdn
  override_during = plan
  values = {
    id             = "EDFDVBD6EXAMPLE"
    arn            = "arn:aws:cloudfront::111122223333:distribution/EDFDVBD6EXAMPLE"
    domain_name    = "d111111abcdef8.cloudfront.net"
    hosted_zone_id = "Z2FDTNDATAQYW2"
  }
}

override_resource {
  target          = module.cdn.aws_cloudfront_public_key.signers
  override_during = plan
  values = {
    id = "K2JCJMDEHXQW5F"
  }
}

override_resource {
  target          = module.cdn.aws_acm_certificate.cdn
  override_during = plan
  values = {
    arn = "arn:aws:acm:us-east-1:111122223333:certificate/0a1b2c3d-example"
    domain_validation_options = [{
      domain_name           = "cdn-pr1234-123456789012-ac-t.aws.example.com"
      resource_record_name  = "_0a1b2c3d.cdn-pr1234-123456789012-ac-t.aws.example.com."
      resource_record_type  = "CNAME"
      resource_record_value = "_4e5f6a7b.acm-validations.aws."
    }]
  }
}

variables {
  name                  = "artifactweave-example"
  region                = "eu-west-2"
  app_role_arn          = "arn:aws:iam::111122223333:role/artifactweave"
  cloudfront_public_key = "-----BEGIN PUBLIC KEY-----\nexample\n-----END PUBLIC KEY-----\n"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    name                  = "artifactweave-example"
    region                = "eu-west-2"
    app_role_arn          = "arn:aws:iam::111122223333:role/artifactweave"
    cloudfront_public_key = "-----BEGIN PUBLIC KEY-----\nexample\n-----END PUBLIC KEY-----\n"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX, the labels it sets, the key file it writes
# and the host ci-vars.sh gives it in the bootstrap's zone.
run "ci_run" {
  command = plan

  variables {
    name                        = "artifactweave-awscn-pr1234-123456789012-ac-t"
    region                      = "eu-west-2"
    app_role_arn                = "arn:aws:iam::111122223333:role/ci/deployer"
    cloudfront_private_key_file = "/home/runner/work/_temp/scenario-aws-cdn-pr1234-123456789012-ac-t/secrets/cloudfront-private-key.pem"
    zone_id                     = "Z0123456789EXAMPLE"
    domain_name                 = "cdn-pr1234-123456789012-ac-t.aws.example.com"
    force_destroy               = true
    tags = {
      artifactweave-scenario = "cdn"
      instance               = "pr1234-123456789012-ac-t"
      expires-at             = "1767225600"
    }
  }

  # The CloudFront names are the bucket's name too (modules/aws/cdn, run
  # names_start_with_name), so this also keeps the origin access control's
  # within its 64 characters, and every name starts with the instance's,
  # which reap finds a lost instance's pieces by.
  assert {
    condition     = output.bucket == "artifactweave-awscn-pr1234-123456789012-ac-t-0a1b2c3d" && length(output.bucket) <= 63
    error_message = "the bucket must be named <name>-<suffix> and fit S3's 63 characters"
  }

  assert {
    condition     = output.storage_host == "cdn-pr1234-123456789012-ac-t.aws.example.com"
    error_message = "storage_host must be the instance's own CDN host, which test/platform checks redirects against"
  }

  assert {
    condition     = output.origin_host == "artifactweave-awscn-pr1234-123456789012-ac-t-0a1b2c3d.s3.eu-west-2.amazonaws.com"
    error_message = "origin_host must be the bucket's endpoint, where test/platform checks X-JFrog-Download-Redirect-To: S3 lands"
  }

  assert {
    condition = yamldecode(output.artifactweave_config).blob.s3.cdn.cloudfront == {
      domain           = "cdn-pr1234-123456789012-ac-t.aws.example.com"
      key_pair_id      = "K2JCJMDEHXQW5F"
      private_key_file = "/home/runner/work/_temp/scenario-aws-cdn-pr1234-123456789012-ac-t/secrets/cloudfront-private-key.pem"
    }
    error_message = "the configuration must sign for the instance's host, with the key the job generated"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the tag reap reads from the state"
  }
}

run "the_bucket_policy_lets_the_distribution_read_the_blobs" {
  command = plan

  assert {
    condition = jsondecode(module.storage.policy).Statement[3] == {
      Sid       = "CloudFrontReadsBlobs"
      Effect    = "Allow"
      Principal = { Service = "cloudfront.amazonaws.com" }
      Action    = "s3:GetObject"
      Resource  = "arn:aws:s3:::artifactweave-example-0a1b2c3d/sha256/*"
      Condition = { StringEquals = { "AWS:SourceArn" = "arn:aws:cloudfront::111122223333:distribution/EDFDVBD6EXAMPLE" } }
    }
    error_message = "the bucket's policy must carry the CDN module's read grant for this distribution, after the storage module's own statements"
  }
}

run "the_distribution_reads_this_bucket" {
  command = plan

  assert {
    condition     = module.cdn.bucket_policy_statement.Resource == "arn:aws:s3:::${output.bucket}/sha256/*"
    error_message = "the distribution must front the scenario's own bucket"
  }
}

run "the_store_prefix_reaches_the_configuration_and_the_grant" {
  command = plan

  variables {
    store_prefix = "/artifacts"
  }

  assert {
    condition = (
      yamldecode(output.artifactweave_config).blob.s3.prefix == "/artifacts" &&
      jsondecode(module.storage.policy).Statement[3].Resource == "arn:aws:s3:::artifactweave-example-0a1b2c3d/artifacts/sha256/*"
    )
    error_message = "store_prefix must reach blob.s3.prefix, and CloudFront's grant must cover the blobs under it"
  }
}

run "name_must_leave_room_for_the_suffix" {
  command = plan

  variables {
    name = "artifactweave-a-name-that-is-too-long-for-a-bucket-name"
  }

  expect_failures = [var.name]
}

run "app_role_arn_is_checked" {
  command = plan

  variables {
    app_role_arn = "artifactweave"
  }

  expect_failures = [var.app_role_arn]
}
