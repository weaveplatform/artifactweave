# The acceptance bucket's security invariants, checked on a plan against
# mocked providers: no credentials and no calls to AWS (decision 0030).

mock_provider "aws" {
  mock_data "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::111122223333:role/artifactweave/artifactweave-conformance"
    }
  }
}
mock_provider "random" {}

# The bucket name, and so the bucket policy, comes from the random suffix,
# which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "0a1b2c3d"
  }
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

# Each rule is checked at its position in main.tf: rendering a failed
# condition over the whole rule list crashes Terraform with mocked values.
run "abandoned_parts_expire" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].filter).prefix == "${var.store_prefix}_uploads/" && one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].expiration).days == 2
    error_message = "the first lifecycle rule must delete the upload parts an interrupted run leaves under _uploads/ after 2 days"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[1].filter).prefix == "conformance/" && one(aws_s3_bucket_lifecycle_configuration.blobs.rule[1].expiration).days == 1
    error_message = "the second lifecycle rule must delete what an interrupted conformance run leaves under conformance/ after a day"
  }

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[2].abort_incomplete_multipart_upload).days_after_initiation == 1
    error_message = "the third lifecycle rule must abort multipart uploads after a day; S3 bills them until aborted"
  }
}

run "role_reaches_this_bucket_only" {
  command = plan

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.blobs.policy).Statement :
      s.Effect == "Deny" || (
        try(s.Principal.AWS, "") == "arn:aws:iam::111122223333:role/artifactweave/artifactweave-conformance" &&
        alltrue([for r in flatten([s.Resource]) : startswith(r, "arn:aws:s3:::artifactweave-acc-0a1b2c3d")])
      )
    ])
    error_message = "the bucket policy grants the conformance role this bucket only"
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.blobs.policy).Statement :
      s.Effect == "Deny" || !contains(flatten([s.Action]), "s3:*")
    ])
    error_message = "the conformance role gets object and listing rights, not s3:*"
  }
}

# A scenario run applies as the deployer: it names the app role, so nothing
# looks up the conformance role, and it tags the bucket for reap.
run "scenario_run_grants_the_named_role" {
  command = plan

  variables {
    name         = "artifactweave-awscn-r123"
    app_role_arn = "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer"
    scenario_labels = {
      artifactweave-scenario = "single-bucket"
      instance               = "r123"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(data.aws_iam_role.conformance) == 0
    error_message = "a named app role must skip the conformance lookup, which the deployer cannot read"
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_s3_bucket_policy.blobs.policy).Statement :
      s.Effect == "Deny" || try(s.Principal.AWS, "") == "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer"
    ])
    error_message = "the bucket policy must grant the named app role"
  }

  assert {
    condition     = aws_s3_bucket.blobs.tags["expires-at"] == "1767225600" && aws_s3_bucket.blobs.tags["instance"] == "r123"
    error_message = "the bucket must carry the scenario tags reap matches"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "the expires_at output must carry the tag reap reads from state"
  }

  assert {
    condition     = output.storage_host == "artifactweave-awscn-r123-0a1b2c3d.s3.eu-west-2.amazonaws.com"
    error_message = "storage_host must be the bucket's virtual-hosted endpoint, where presigned URLs point"
  }

  assert {
    condition     = startswith(aws_s3_bucket.blobs.bucket, "artifactweave-") && length(aws_s3_bucket.blobs.bucket) <= 63
    error_message = "a scenario bucket must stay inside the deployer's artifactweave- prefix and S3's 63-character limit"
  }
}
