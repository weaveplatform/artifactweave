# How the scenario composes the bucket module into a mirror, checked on a
# plan against mocked providers (decision 0030). The buckets' own security
# invariants are modules/aws/storage's tests.

mock_provider "aws" {
  mock_data "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::111122223333:role/artifactweave/artifactweave-conformance"
    }
  }
}
mock_provider "random" {}

# Bucket names, and so the rendered configuration, come from the random
# suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "0a1b2c3d"
  }
}

run "two_regions_one_mirror" {
  command = plan

  assert {
    condition     = output.buckets == { "eu-west-2" = "artifactweave-mr-0a1b2c3d-euw2", "eu-west-1" = "artifactweave-mr-0a1b2c3d-euw1" }
    error_message = "there must be one member bucket in each region, named after the suffix and the region's code"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.backend == "mirror"
    error_message = "artifactweave must run the two buckets as a mirror"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.members == ["s3_eu_west_2", "s3_eu_west_1"]
    error_message = "the mirror's members must be the two stores, the primary region first"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.zone == "eu-west-2"
    error_message = "this instance's zone must be the primary region, so reads prefer it"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 1
    error_message = "the default write quorum is one copy, so uploads continue with a region down"
  }

  assert {
    condition = alltrue([
      for name, store in yamldecode(output.artifactweave_config).blob.stores :
      store.backend == "s3" && store.zone == store.s3.region && name == "s3_${replace(store.s3.region, "-", "_")}"
    ])
    error_message = "each store must be an s3 store whose zone and region are its bucket's"
  }

  assert {
    condition     = output.storage_host == "artifactweave-mr-0a1b2c3d-euw2.s3.eu-west-2.amazonaws.com"
    error_message = "storage_host must be the primary bucket's virtual-hosted endpoint"
  }

  assert {
    condition     = output.mirror_members[1].signed_url_prefix == "artifactweave-mr-0a1b2c3d-euw1.s3.eu-west-1.amazonaws.com/"
    error_message = "mirror_members must list the secondary member second, with the prefix its presigned URLs start with"
  }

  assert {
    condition     = output.app_role_arn == "arn:aws:iam::111122223333:role/artifactweave/artifactweave-conformance"
    error_message = "outside a scenario run the bucket policies grant the bootstrap's conformance role"
  }
}

# A scenario run applies as the deployer: it names the app role, so nothing
# looks up the conformance role, and the buckets stay inside the deployer's
# artifactweave- prefix and S3's 63 characters.
run "scenario_run_grants_the_named_role" {
  command = plan

  variables {
    name         = "artifactweave-awscn-pr160-38712345678-mr-t"
    app_role_arn = "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer"
    scenario_labels = {
      artifactweave-scenario = "multi-region"
      instance               = "pr160-38712345678-mr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(data.aws_iam_role.conformance) == 0
    error_message = "a named app role must skip the conformance lookup, which the deployer cannot read"
  }

  assert {
    condition     = output.app_role_arn == "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer"
    error_message = "the bucket policies must grant the named app role"
  }

  assert {
    condition     = alltrue([for r, b in output.buckets : startswith(b, "artifactweave-") && length(b) <= 63])
    error_message = "a scenario bucket must stay inside the deployer's artifactweave- prefix and S3's 63-character limit"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "the expires_at output must carry the tag reap reads from state"
  }
}

run "regions_must_differ" {
  command = plan

  variables {
    secondary_region = "eu-west-2"
  }

  expect_failures = [var.secondary_region]
}
