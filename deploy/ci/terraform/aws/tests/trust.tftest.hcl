# The bootstrap's invariants, checked on a plan against a mocked provider: no
# credentials and no calls to AWS (decision 0030). The zone's prevent_destroy
# is a lifecycle setting a test cannot read; Terraform itself enforces it on
# any plan that would destroy the zone.

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "111122223333"
    }
  }
}

variables {
  budget_alert_email = "alerts@example.com"
}

# A mocked provider cannot import, and leaves ARNs unknown at plan, so the
# zone and the provider and boundary whose ARNs the policies quote are given
# the values AWS would return.
override_resource {
  target = aws_route53_zone.aws
  values = {
    id           = "Z06607493A97HOZP218UN"
    zone_id      = "Z06607493A97HOZP218UN"
    name_servers = ["ns-1278.awsdns-31.org"]
  }
}

override_resource {
  target          = aws_iam_openid_connect_provider.github
  override_during = plan
  values = {
    arn = "arn:aws:iam::111122223333:oidc-provider/token.actions.githubusercontent.com"
  }
}

override_resource {
  target          = aws_iam_policy.scenario_boundary
  override_during = plan
  values = {
    arn = "arn:aws:iam::111122223333:policy/artifactweave/artifactweave-scenario-boundary"
  }
}

override_resource {
  target          = aws_iam_role.deployer
  override_during = plan
  values = {
    id = "artifactweave-deployer"
  }
}

run "trust_pins_the_immutable_subject_per_role" {
  command = plan

  assert {
    condition     = jsondecode(aws_iam_role.conformance.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-conformance"
    error_message = "the conformance role must trust the cloud-conformance subject only"
  }

  assert {
    condition     = jsondecode(aws_iam_role.deployer.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == "repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-platform"
    error_message = "the deployer role must trust the cloud-platform subject only"
  }

  assert {
    condition = alltrue([
      for p in [aws_iam_role.conformance.assume_role_policy, aws_iam_role.deployer.assume_role_policy] :
      length(jsondecode(p).Statement) == 1 &&
      jsondecode(p).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:aud"] == "sts.amazonaws.com" &&
      !can(jsondecode(p).Statement[0].Condition.StringLike)
    ])
    error_message = "each trust is one statement matching the audience and subject exactly, with no wildcard (StringLike)"
  }

  assert {
    condition     = aws_iam_openid_connect_provider.github.client_id_list == toset(["sts.amazonaws.com"])
    error_message = "the OIDC provider must accept STS's audience only"
  }
}

run "deployer_is_bounded" {
  command = plan

  assert {
    condition     = aws_iam_role.deployer.permissions_boundary == "arn:aws:iam::111122223333:policy/artifactweave/artifactweave-scenario-boundary"
    error_message = "the deployer must carry the scenario permissions boundary"
  }

  assert {
    condition = anytrue([
      for s in jsondecode(aws_iam_policy.scenario_boundary.policy).Statement :
      s.Effect == "Deny" && contains(flatten([try(s.Action, [])]), "iam:CreateRole") &&
      try(s.Condition.StringNotEquals["iam:PermissionsBoundary"], "") == "arn:aws:iam::111122223333:policy/artifactweave/artifactweave-scenario-boundary"
    ])
    error_message = "the boundary must refuse any role created without the same boundary"
  }

  assert {
    condition = anytrue([
      for s in jsondecode(aws_iam_policy.scenario_boundary.policy).Statement :
      s.Effect == "Deny" && try(s.Resource, "") == "arn:aws:s3:::artifactweave-tfstate-56920abd/bootstrap/*"
    ])
    error_message = "the boundary must keep the deployer out of the bootstrap's own state"
  }

  assert {
    condition = alltrue([
      for s in jsondecode(aws_iam_role_policy.deployer.policy).Statement :
      !contains(flatten([s.Action]), "iam:*") && !contains(flatten([s.Action]), "*")
    ])
    error_message = "the deployer's own policy grants no IAM or account-wide wildcard"
  }
}

run "state_is_private_and_versioned" {
  command = plan

  assert {
    condition = alltrue([
      aws_s3_bucket_public_access_block.state.block_public_acls,
      aws_s3_bucket_public_access_block.state.block_public_policy,
      aws_s3_bucket_public_access_block.state.ignore_public_acls,
      aws_s3_bucket_public_access_block.state.restrict_public_buckets,
    ])
    error_message = "the state bucket must block every form of public access"
  }

  assert {
    condition     = one(aws_s3_bucket_versioning.state.versioning_configuration).status == "Enabled"
    error_message = "the state bucket must keep versions, so a bad apply can be undone"
  }

  assert {
    condition     = aws_s3_bucket.state.force_destroy == false && aws_s3_bucket.state.bucket == "artifactweave-tfstate-56920abd"
    error_message = "the state bucket must match backend.tf and refuse to be destroyed while it holds state"
  }
}
