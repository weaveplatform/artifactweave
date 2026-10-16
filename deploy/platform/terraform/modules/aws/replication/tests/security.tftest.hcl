# The replication role's security baseline (decision 0030), checked on a plan
# against a mocked provider: no credentials and no calls to AWS. Scenarios
# that call this module check how they compose it, not these again. One
# assertion per run, so a failure names the invariant it breaks.

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

run "only_s3_may_assume_the_role" {
  command = plan

  assert {
    condition = (
      length(jsondecode(aws_iam_role.replication.assume_role_policy).Statement) == 1 &&
      jsondecode(aws_iam_role.replication.assume_role_policy).Statement[0].Principal == { Service = "s3.amazonaws.com" } &&
      jsondecode(aws_iam_role.replication.assume_role_policy).Statement[0].Action == "sts:AssumeRole"
    )
    error_message = "the role's trust must let S3, and nothing else, assume it"
  }
}

run "trust_names_the_source_bucket_and_its_account" {
  command = plan

  assert {
    condition = jsondecode(aws_iam_role.replication.assume_role_policy).Statement[0].Condition == {
      StringEquals = { "aws:SourceAccount" = "111122223333" }
      ArnLike      = { "aws:SourceArn" = "arn:aws:s3:::example-0a1b2c3d" }
    }
    error_message = "S3 may assume the role only for the source bucket, in this account, so no other bucket can use it"
  }
}

run "role_reads_the_source_and_writes_the_destination_only" {
  command = plan

  assert {
    condition = [for s in jsondecode(aws_iam_role_policy.replication.policy).Statement : [s.Effect, s.Resource]] == [
      ["Allow", "arn:aws:s3:::example-0a1b2c3d"],
      ["Allow", "arn:aws:s3:::example-0a1b2c3d/*"],
      ["Allow", "arn:aws:s3:::example-0a1b2c3d-r/*"],
    ]
    error_message = "the role must reach the source bucket, its objects and the destination's objects, and nothing else"
  }
}

run "role_gets_replication_actions_only" {
  command = plan

  assert {
    condition = [for s in jsondecode(aws_iam_role_policy.replication.policy).Statement : s.Action] == [
      ["s3:GetReplicationConfiguration", "s3:ListBucket"],
      ["s3:GetObjectVersionAcl", "s3:GetObjectVersionForReplication", "s3:GetObjectVersionTagging"],
      ["s3:ReplicateObject", "s3:ReplicateTags"],
    ]
    error_message = "the role must read the source's configuration and versions and write copies, and may not delete a copy while deletes do not replicate"
  }
}

run "boundary_and_path_reach_the_role" {
  command = plan

  variables {
    permissions_boundary_arn = "arn:aws:iam::111122223333:policy/example-boundary"
    iam_role_path            = "/example/"
  }

  assert {
    condition     = aws_iam_role.replication.permissions_boundary == "arn:aws:iam::111122223333:policy/example-boundary" && aws_iam_role.replication.path == "/example/"
    error_message = "the role must carry the permissions boundary and path given, for a deployer that may create roles only with them"
  }
}

run "tags_reach_the_role" {
  command = plan

  variables {
    tags = { artifactweave-scenario = "geo-replicated", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = aws_iam_role.replication.tags == tomap({ artifactweave-scenario = "geo-replicated", instance = "r1", expires-at = "1767225600" })
    error_message = "the role, the one resource here that takes tags, must carry the tags given"
  }
}
