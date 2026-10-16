# How the module reads its inputs, on a mocked plan: the prefix
# normalisation the backend applies, the checks that fail at plan rather than
# at apply, and the defaults a consumer gets.

mock_provider "aws" {}

variables {
  name       = "example-0a1b2c3d-euw1"
  region     = "eu-west-1"
  principals = { app = "arn:aws:iam::111122223333:role/example-app" }
}

run "store_prefix_defaults_to_the_bucket_root" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].filter).prefix == "_uploads/"
    error_message = "with no store_prefix, upload parts live under _uploads/ at the bucket's root"
  }
}

run "store_prefix_gains_a_trailing_slash" {
  command = plan

  variables {
    store_prefix = "local"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].filter).prefix == "local/_uploads/"
    error_message = "store_prefix must be normalised as the backend does, with a trailing slash"
  }
}

run "store_prefix_loses_a_leading_slash" {
  command = plan

  variables {
    store_prefix = "/local/"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].filter).prefix == "local/_uploads/"
    error_message = "store_prefix must be normalised as the backend does, without a leading slash"
  }
}

run "force_destroy_is_off_by_default" {
  command = plan

  assert {
    condition     = aws_s3_bucket.blobs.force_destroy == false
    error_message = "a consumer's terraform destroy must not empty a bucket unless asked"
  }
}

run "force_destroy_reaches_the_bucket" {
  command = plan

  variables {
    force_destroy = true
  }

  assert {
    condition     = aws_s3_bucket.blobs.force_destroy
    error_message = "force_destroy = true must let destroy remove the bucket with its objects"
  }
}

run "outputs_name_the_bucket_and_its_endpoint" {
  command = plan

  assert {
    condition = [output.bucket, output.arn, output.region, output.versioned, output.host, output.signed_url_prefix] == [
      "example-0a1b2c3d-euw1", "arn:aws:s3:::example-0a1b2c3d-euw1", "eu-west-1", false,
      "example-0a1b2c3d-euw1.s3.eu-west-1.amazonaws.com", "example-0a1b2c3d-euw1.s3.eu-west-1.amazonaws.com/",
    ]
    error_message = "the outputs must name the bucket, its ARN and region, whether it is versioned, and the virtual-hosted endpoint presigned URLs use"
  }
}

run "name_is_checked" {
  command = plan

  variables {
    name = "Not_A_Bucket"
  }

  expect_failures = [var.name]
}

run "principals_must_name_a_role" {
  command = plan

  variables {
    principals = {}
  }

  expect_failures = [var.principals]
}

run "principals_must_be_role_arns" {
  command = plan

  variables {
    principals = { app = "arn:aws:iam::111122223333:user/someone" }
  }

  expect_failures = [var.principals]
}

run "expiring_prefix_days_are_whole_and_positive" {
  command = plan

  variables {
    expiring_prefixes = { "scratch/" = 0 }
  }

  expect_failures = [var.expiring_prefixes]
}

run "policy_statements_need_their_own_sid" {
  command = plan

  variables {
    policy_statements = [{ Sid = "TLSOnly", Effect = "Allow", Principal = "*", Action = "s3:GetObject", Resource = "*" }]
  }

  expect_failures = [var.policy_statements]
}

run "policy_statements_need_a_sid" {
  command = plan

  variables {
    policy_statements = [{ Effect = "Allow", Principal = { Service = "cloudfront.amazonaws.com" }, Action = "s3:GetObject", Resource = "*" }]
  }

  expect_failures = [var.policy_statements]
}
