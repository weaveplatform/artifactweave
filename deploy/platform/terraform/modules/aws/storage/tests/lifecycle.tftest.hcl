# What expires, and when (decision 0030), on a mocked plan. Each rule is
# checked at its position in main.tf: rendering a failed condition over the
# whole rule list crashes Terraform with mocked values.

mock_provider "aws" {}

variables {
  name         = "example-0a1b2c3d-euw1"
  region       = "eu-west-1"
  principals   = { app = "arn:aws:iam::111122223333:role/example-app" }
  store_prefix = "local/"
}

run "abandoned_upload_parts_expire_after_two_days" {
  command = plan

  assert {
    condition = (
      aws_s3_bucket_lifecycle_configuration.blobs.rule[0].status == "Enabled" &&
      one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].filter).prefix == "local/_uploads/" &&
      one(aws_s3_bucket_lifecycle_configuration.blobs.rule[0].expiration).days == 2
    )
    error_message = "the first rule must delete the upload parts an interrupted upload leaves under <store_prefix>_uploads/ after 2 days"
  }
}

run "multipart_uploads_abort_after_a_day" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_lifecycle_configuration.blobs.rule[1].abort_incomplete_multipart_upload).days_after_initiation == 1
    error_message = "the second rule must abort multipart uploads after a day; S3 bills their parts until aborted"
  }
}

run "expiring_prefixes_expire_after_their_days" {
  command = plan

  variables {
    expiring_prefixes = { "conformance/" = 1, "scratch/" = 7 }
  }

  assert {
    condition = [
      for r in slice(aws_s3_bucket_lifecycle_configuration.blobs.rule, 2, 4) :
      [r.id, one(r.filter).prefix, one(r.expiration).days]
      ] == [
      ["expire-conformance/", "conformance/", 1],
      ["expire-scratch/", "scratch/", 7],
    ]
    error_message = "each expiring prefix must get a rule deleting its objects after its days"
  }
}

run "an_unversioned_bucket_keeps_the_two_backstops" {
  command = plan

  assert {
    condition     = length(aws_s3_bucket_lifecycle_configuration.blobs.rule) == 2 && length(aws_s3_bucket_versioning.blobs) == 0
    error_message = "without versioning or expiring prefixes the bucket must keep only the two backstop rules, and no versioning configuration"
  }
}

run "versioning_turns_on_when_asked" {
  command = plan

  variables {
    versioning = true
  }

  assert {
    condition     = one(aws_s3_bucket_versioning.blobs[0].versioning_configuration).status == "Enabled"
    error_message = "versioning = true must enable versioning on the bucket"
  }
}

run "noncurrent_versions_expire_after_a_day_when_versioned" {
  command = plan

  variables {
    versioning = true
  }

  assert {
    condition = (
      aws_s3_bucket_lifecycle_configuration.blobs.rule[2].id == "expire-noncurrent-versions" &&
      one(aws_s3_bucket_lifecycle_configuration.blobs.rule[2].noncurrent_version_expiration).noncurrent_days == 1 &&
      one(aws_s3_bucket_lifecycle_configuration.blobs.rule[2].expiration).expired_object_delete_marker
    )
    error_message = "a versioned bucket must expire noncurrent versions after a day, and the delete markers they leave, so deleted blobs stop costing"
  }
}
