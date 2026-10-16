# What the replication rule copies and keeps, on a mocked plan. One assertion
# per run, so a failure names the invariant it breaks.

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "111122223333"
    }
  }
}

variables {
  name = "example-0a1b2c3d-repl"
  source_bucket = {
    bucket    = "example-0a1b2c3d"
    arn       = "arn:aws:s3:::example-0a1b2c3d"
    region    = "eu-west-2"
    versioned = true
  }
  destination_bucket = {
    bucket    = "example-0a1b2c3d-r"
    arn       = "arn:aws:s3:::example-0a1b2c3d-r"
    region    = "eu-west-1"
    versioned = true
  }
}

run "rule_is_on_the_source_bucket_in_its_region" {
  command = plan

  assert {
    condition = (
      aws_s3_bucket_replication_configuration.replication.bucket == "example-0a1b2c3d" &&
      aws_s3_bucket_replication_configuration.replication.region == "eu-west-2" &&
      one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).bucket == "arn:aws:s3:::example-0a1b2c3d-r"
    )
    error_message = "the rule must be configured on the source bucket, in its region, and copy into the destination bucket"
  }
}

run "the_whole_bucket_replicates_by_default" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_replication_configuration.replication.rule[0].filter).prefix == ""
    error_message = "with no prefix, every object must replicate"
  }
}

run "only_the_prefix_given_replicates" {
  command = plan

  variables {
    prefix = "artifacts/sha256/"
  }

  assert {
    condition     = one(aws_s3_bucket_replication_configuration.replication.rule[0].filter).prefix == "artifacts/sha256/"
    error_message = "only objects under the prefix given may replicate"
  }
}

run "deletes_do_not_replicate_by_default" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_replication_configuration.replication.rule[0].delete_marker_replication).status == "Disabled"
    error_message = "deletes must not replicate unless asked, so the destination keeps what a failover may need"
  }
}

run "deletes_replicate_when_asked" {
  command = plan

  variables {
    replicate_deletes = true
  }

  assert {
    condition = (
      one(aws_s3_bucket_replication_configuration.replication.rule[0].delete_marker_replication).status == "Enabled" &&
      contains(jsondecode(aws_iam_role_policy.replication.policy).Statement[2].Action, "s3:ReplicateDelete")
    )
    error_message = "replicate_deletes = true must replicate delete markers, and grant the role the delete it needs"
  }
}

run "replication_time_control_is_opt_in" {
  command = plan

  assert {
    condition     = length(one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).replication_time) == 0 && length(one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).metrics) == 0
    error_message = "Replication Time Control is billed per GB replicated, so it must be off unless asked"
  }
}

run "replication_time_control_turns_on_with_its_metrics" {
  command = plan

  variables {
    replication_time_control = true
  }

  assert {
    condition = (
      one(one(one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).replication_time).time).minutes == 15 &&
      one(one(one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).metrics).event_threshold).minutes == 15
    )
    error_message = "Replication Time Control must use its only valid threshold, 15 minutes, with the replication metrics it needs"
  }
}

run "copies_are_standard_by_default" {
  command = plan

  assert {
    condition     = one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).storage_class == "STANDARD"
    error_message = "the destination's copies must be S3 Standard unless another class is given"
  }
}

run "storage_class_reaches_the_copies" {
  command = plan

  variables {
    storage_class = "STANDARD_IA"
  }

  assert {
    condition     = one(aws_s3_bucket_replication_configuration.replication.rule[0].destination).storage_class == "STANDARD_IA"
    error_message = "storage_class must reach the destination's copies"
  }
}
