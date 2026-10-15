# How the module reads its inputs, on a mocked plan: the checks that fail at
# plan rather than at apply, and the outputs a scenario reads.

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "111122223333"
    }
  }
}

# The role's ARN is computed, which a mocked provider would leave unknown at
# plan.
override_resource {
  target          = aws_iam_role.replication
  override_during = plan
  values = {
    arn = "arn:aws:iam::111122223333:role/example-0a1b2c3d-repl"
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

run "outputs_name_the_role_and_the_prefix" {
  command = plan

  variables {
    prefix = "artifacts/sha256/"
  }

  assert {
    condition = [output.role_arn, output.role_name, output.prefix] == [
      "arn:aws:iam::111122223333:role/example-0a1b2c3d-repl", "example-0a1b2c3d-repl", "artifacts/sha256/",
    ]
    error_message = "the outputs must name the role S3 assumes and the prefix that replicates"
  }
}

run "the_rule_assumes_the_role" {
  command = plan

  assert {
    condition     = aws_s3_bucket_replication_configuration.replication.role == "arn:aws:iam::111122223333:role/example-0a1b2c3d-repl"
    error_message = "the replication rule must run as this module's role"
  }
}

run "an_unversioned_source_is_refused" {
  command = plan

  variables {
    source_bucket = {
      bucket    = "example-0a1b2c3d"
      arn       = "arn:aws:s3:::example-0a1b2c3d"
      region    = "eu-west-2"
      versioned = false
    }
  }

  expect_failures = [var.source_bucket]
}

run "an_unversioned_destination_is_refused" {
  command = plan

  variables {
    destination_bucket = {
      bucket    = "example-0a1b2c3d-r"
      arn       = "arn:aws:s3:::example-0a1b2c3d-r"
      region    = "eu-west-1"
      versioned = false
    }
  }

  expect_failures = [var.destination_bucket]
}

run "a_bucket_cannot_replicate_into_itself" {
  command = plan

  variables {
    destination_bucket = {
      bucket    = "example-0a1b2c3d"
      arn       = "arn:aws:s3:::example-0a1b2c3d"
      region    = "eu-west-2"
      versioned = true
    }
  }

  expect_failures = [var.destination_bucket]
}

run "name_is_checked" {
  command = plan

  variables {
    name = "a-role-name-longer-than-the-sixty-four-characters-iam-allows-for-one"
  }

  expect_failures = [var.name]
}

run "iam_role_path_is_checked" {
  command = plan

  variables {
    iam_role_path = "example"
  }

  expect_failures = [var.iam_role_path]
}
