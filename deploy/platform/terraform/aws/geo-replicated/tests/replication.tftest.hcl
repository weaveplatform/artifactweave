# The geo-replicated buckets' invariants, checked on a plan against mocked
# providers: no credentials and no calls to AWS (decision 0030).

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
    }
  }
  mock_data "aws_partition" {
    defaults = {
      partition = "aws"
    }
  }
  mock_data "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/artifactweave/artifactweave-conformance"
    }
  }
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

mock_provider "aws" {
  alias = "replica"
}

mock_provider "random" {}

# The bucket and role names carry random_id's hex, which a plan does not know
# otherwise.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "deadbeef"
  }
}

run "both_buckets_are_private" {
  command = plan

  assert {
    condition = alltrue([
      for b in [aws_s3_bucket_public_access_block.blobs, aws_s3_bucket_public_access_block.replica] :
      b.block_public_acls && b.block_public_policy && b.ignore_public_acls && b.restrict_public_buckets
    ])
    error_message = "both buckets must block every form of public access"
  }

  assert {
    condition = alltrue([
      for c in [aws_s3_bucket_ownership_controls.blobs, aws_s3_bucket_ownership_controls.replica] :
      c.rule[0].object_ownership == "BucketOwnerEnforced"
    ])
    error_message = "both buckets must enforce bucket ownership, so no object ACL applies"
  }
}

run "replication_copies_committed_blobs_to_the_replica" {
  command = plan

  assert {
    condition     = aws_s3_bucket_versioning.blobs.versioning_configuration[0].status == "Enabled" && aws_s3_bucket_versioning.replica.versioning_configuration[0].status == "Enabled"
    error_message = "replication needs versioning on both buckets"
  }

  assert {
    condition     = aws_s3_bucket_replication_configuration.blobs.rule[0].filter[0].prefix == "local/sha256/"
    error_message = "only committed blobs (<store_prefix>sha256/) may replicate, not upload parts or conformance prefixes"
  }

  assert {
    condition     = aws_s3_bucket_replication_configuration.blobs.rule[0].delete_marker_replication[0].status == "Disabled"
    error_message = "deletes must not replicate, so the replica keeps what a failover may need"
  }

  assert {
    condition     = length(aws_s3_bucket_replication_configuration.blobs.rule[0].destination[0].replication_time) == 0
    error_message = "Replication Time Control is billed per GB replicated, so it must be opt-in"
  }

  assert {
    condition     = aws_s3_bucket.replica.bucket == "artifactweave-geo-deadbeef-r" && aws_s3_bucket.replica.force_destroy
    error_message = "the replica bucket must be named after the primary and destroyed with the scenario"
  }
}

run "replication_time_control_is_opt_in" {
  command = plan

  variables {
    replication_time_control = true
  }

  assert {
    condition     = aws_s3_bucket_replication_configuration.blobs.rule[0].destination[0].replication_time[0].time[0].minutes == 15
    error_message = "Replication Time Control's only valid threshold is 15 minutes"
  }

  assert {
    condition     = length(aws_s3_bucket_replication_configuration.blobs.rule[0].destination[0].metrics) == 1
    error_message = "Replication Time Control needs replication metrics"
  }
}

run "replication_role_stays_inside_the_boundary" {
  command = plan

  assert {
    condition     = aws_iam_role.replication.path == "/artifactweave-scenarios/"
    error_message = "the deployer may create roles under the scenario path only"
  }

  assert {
    condition     = aws_iam_role.replication.permissions_boundary == "arn:aws:iam::123456789012:policy/artifactweave/artifactweave-scenario-boundary"
    error_message = "the replication role must carry aws/bootstrap's boundary, or the deployer cannot create it"
  }

  assert {
    condition     = length(aws_iam_role.replication.name) <= 64
    error_message = "IAM role names are at most 64 characters"
  }
}

run "same_region_is_refused" {
  command = plan

  variables {
    replica_region = "eu-west-2"
  }

  expect_failures = [var.replica_region]
}

run "bucket_names_stay_in_the_deployers_prefix" {
  command = plan

  variables {
    name = "someone-elses-bucket"
  }

  expect_failures = [var.name]
}

# A scenario run applies as the deployer: it names the app role, so nothing
# looks up the conformance role, and it tags both buckets for reap.
run "scenario_run_grants_the_named_role" {
  command = plan

  variables {
    name         = "artifactweave-awscn-pr131-1-s"
    app_role_arn = "arn:aws:iam::123456789012:role/artifactweave/artifactweave-deployer"
    scenario_labels = {
      artifactweave-scenario = "geo-replicated"
      instance               = "pr131-1-s"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(data.aws_iam_role.conformance) == 0
    error_message = "a named app role must skip the conformance lookup, which the deployer cannot read"
  }

  assert {
    condition     = aws_s3_bucket.blobs.tags["expires-at"] == "1767225600" && aws_s3_bucket.replica.tags["instance"] == "pr131-1-s"
    error_message = "both buckets must carry the scenario tags reap matches"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "the expires_at output must carry the tag reap reads from state"
  }
}
