# The bucket's security invariants, checked on a plan against a mocked
# provider: no credentials and no calls to AWS (decision 0030). Scenarios that
# call this module check how they compose it, not these again.

mock_provider "aws" {}

variables {
  name         = "artifactweave-awscn-r1-0a1b2c3d-euw1"
  region       = "eu-west-1"
  store_prefix = "local/"
  app_role_arn = "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer"
}

run "bucket_is_private" {
  command = plan

  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.blobs.block_public_acls,
      aws_s3_bucket_public_access_block.blobs.block_public_policy,
      aws_s3_bucket_public_access_block.blobs.ignore_public_acls,
      aws_s3_bucket_public_access_block.blobs.restrict_public_buckets,
    ])
    error_message = "the bucket must block every form of public access"
  }

  assert {
    condition     = one(aws_s3_bucket_ownership_controls.blobs.rule).object_ownership == "BucketOwnerEnforced"
    error_message = "the bucket must disable ACLs, so no object ACL can make a blob public"
  }

  assert {
    condition     = jsondecode(aws_s3_bucket_policy.blobs.policy).Statement[0].Condition.Bool["aws:SecureTransport"] == "false" && jsondecode(aws_s3_bucket_policy.blobs.policy).Statement[0].Effect == "Deny"
    error_message = "the bucket policy must refuse requests without TLS"
  }
}

run "every_resource_is_in_the_region_given" {
  command = plan

  assert {
    condition = alltrue([
      aws_s3_bucket.blobs.region == "eu-west-1",
      aws_s3_bucket_ownership_controls.blobs.region == "eu-west-1",
      aws_s3_bucket_public_access_block.blobs.region == "eu-west-1",
      aws_s3_bucket_server_side_encryption_configuration.blobs.region == "eu-west-1",
      aws_s3_bucket_lifecycle_configuration.blobs.region == "eu-west-1",
      aws_s3_bucket_policy.blobs.region == "eu-west-1",
    ])
    error_message = "the bucket and its settings must all be managed in the region given, not the provider's default"
  }

  assert {
    condition     = output.host == "artifactweave-awscn-r1-0a1b2c3d-euw1.s3.eu-west-1.amazonaws.com"
    error_message = "host must be the bucket's virtual-hosted endpoint in its own region, where presigned URLs point"
  }
}

# Each rule is checked at its position in main.tf: rendering a failed
# condition over the whole rule list crashes Terraform with mocked values.
run "abandoned_parts_expire" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].filter).prefix == "local/_uploads/" && one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].expiration).days == 2
    error_message = "the first lifecycle rule must delete the upload parts an interrupted run leaves under _uploads/ after 2 days"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[1].abort_incomplete_multipart_upload).days_after_initiation == 1
    error_message = "the second lifecycle rule must abort multipart uploads after a day; S3 bills them until aborted"
  }
}

run "role_reaches_this_bucket_only" {
  command = plan

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.blobs.policy).Statement :
      s.Effect == "Deny" || (
        try(s.Principal.AWS, "") == "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer" &&
        alltrue([for r in flatten([s.Resource]) : startswith(r, "arn:aws:s3:::artifactweave-awscn-r1-0a1b2c3d-euw1")])
      )
    ])
    error_message = "the bucket policy grants the app role this bucket only"
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.blobs.policy).Statement :
      s.Effect == "Deny" || !contains(flatten([s.Action]), "s3:*")
    ])
    error_message = "the app role gets object and listing rights, not s3:*"
  }
}

run "bucket_name_is_checked" {
  command = plan

  variables {
    name = "Not_A_Bucket"
  }

  expect_failures = [var.name]
}

run "tags_reach_the_bucket" {
  command = plan

  variables {
    tags = { artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = aws_s3_bucket.blobs.tags["expires-at"] == "1767225600" && aws_s3_bucket.blobs.tags["instance"] == "r1"
    error_message = "the bucket must carry the scenario tags reap matches"
  }
}
