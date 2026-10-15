# The bucket's security baseline (decision 0030), checked on a plan against a
# mocked provider: no credentials and no calls to AWS. Scenarios that call
# this module check how they compose it, not these again. One assertion per
# run, so a failure names the invariant it breaks.

mock_provider "aws" {}

variables {
  name   = "example-0a1b2c3d-euw1"
  region = "eu-west-1"
  principals = {
    app      = "arn:aws:iam::111122223333:role/example-app"
    operator = "arn:aws:iam::111122223333:role/example-operator"
  }
  store_prefix = "local/"
}

run "public_access_is_blocked" {
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
}

run "acls_are_disabled" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_ownership_controls.blobs.rule).object_ownership == "BucketOwnerEnforced"
    error_message = "the bucket must disable ACLs, so IAM alone decides access and no object ACL can make a blob public"
  }
}

run "plain_http_is_refused" {
  command = plan

  assert {
    condition = jsondecode(aws_s3_bucket_policy.blobs.policy).Statement[0] == {
      Sid       = "TLSOnly"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = ["arn:aws:s3:::example-0a1b2c3d-euw1", "arn:aws:s3:::example-0a1b2c3d-euw1/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }
    error_message = "the bucket policy must refuse every request to the bucket and its objects without TLS"
  }
}

run "objects_are_encrypted" {
  command = plan

  assert {
    condition     = one(one(aws_s3_bucket_server_side_encryption_configuration.blobs.rule).apply_server_side_encryption_by_default).sse_algorithm == "AES256"
    error_message = "the bucket must encrypt every object at rest by default"
  }
}

run "every_resource_is_in_the_region_given" {
  command = plan

  variables {
    versioning = true
  }

  assert {
    condition = alltrue([
      aws_s3_bucket.blobs.region == "eu-west-1",
      aws_s3_bucket_ownership_controls.blobs.region == "eu-west-1",
      aws_s3_bucket_public_access_block.blobs.region == "eu-west-1",
      aws_s3_bucket_server_side_encryption_configuration.blobs.region == "eu-west-1",
      aws_s3_bucket_versioning.blobs[0].region == "eu-west-1",
      aws_s3_bucket_lifecycle_configuration.blobs.region == "eu-west-1",
      aws_s3_bucket_policy.blobs.region == "eu-west-1",
    ])
    error_message = "the bucket and its settings must all be managed in the region given, not the provider's default"
  }
}

run "principals_reach_this_bucket_only" {
  command = plan

  assert {
    condition = alltrue([
      for s in slice(jsondecode(aws_s3_bucket_policy.blobs.policy).Statement, 1, 3) :
      s.Effect == "Allow" &&
      s.Principal == { AWS = ["arn:aws:iam::111122223333:role/example-app", "arn:aws:iam::111122223333:role/example-operator"] } &&
      alltrue([for r in flatten([s.Resource]) : r == "arn:aws:s3:::example-0a1b2c3d-euw1" || r == "arn:aws:s3:::example-0a1b2c3d-euw1/*"])
    ])
    error_message = "the bucket policy must grant every principal given, and nothing beyond this bucket and its objects"
  }
}

run "principals_get_object_and_listing_rights_only" {
  command = plan

  assert {
    condition = toset(flatten([for s in slice(jsondecode(aws_s3_bucket_policy.blobs.policy).Statement, 1, 3) : s.Action])) == toset([
      "s3:AbortMultipartUpload", "s3:DeleteObject", "s3:GetObject", "s3:ListMultipartUploadParts", "s3:PutObject",
      "s3:ListBucket", "s3:ListBucketMultipartUploads",
    ])
    error_message = "the principals get the object and listing actions artifactweave uses, and no bucket administration"
  }
}

run "tags_reach_the_bucket" {
  command = plan

  variables {
    tags = { artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = aws_s3_bucket.blobs.tags == tomap({ artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" })
    error_message = "the bucket, the one resource here that takes tags, must carry the tags given"
  }
}
