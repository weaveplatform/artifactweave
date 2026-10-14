# The bootstrap's invariants, checked on a plan against a mocked provider: no
# credentials and no calls to Google (decision 0030). The zone's
# prevent_destroy is a lifecycle setting a test cannot read; Terraform itself
# enforces it on any plan that would destroy the zone.

mock_provider "google" {
  mock_data "google_project" {
    defaults = {
      number = "1025324209347"
    }
  }
  mock_data "google_client_openid_userinfo" {
    defaults = {
      email = "operator@example.com"
    }
  }
}

# The budget's provider configuration (versions.tf) is mocked too, so the
# tests need no credentials at all.
mock_provider "google" {
  alias = "billing"
}

# A mocked provider cannot import, so the three resources imports.tf adopts
# are overridden with the state an import would read.
override_resource {
  target = google_dns_managed_zone.gcp
  values = {
    id           = "projects/project-0c0b2d7a-9ad7-4221-9a8/managedZones/gcp-artifact-weaveplatform-run"
    name_servers = ["ns-cloud-d1.googledomains.com."]
  }
}

override_resource {
  target = google_iam_workload_identity_pool.github
  values = {
    id   = "projects/project-0c0b2d7a-9ad7-4221-9a8/locations/global/workloadIdentityPools/github-actions"
    name = "projects/1025324209347/locations/global/workloadIdentityPools/github-actions"
  }
}

override_resource {
  target = google_iam_workload_identity_pool_provider.github
  values = {
    id   = "projects/project-0c0b2d7a-9ad7-4221-9a8/locations/global/workloadIdentityPools/github-actions/providers/artifactweave"
    name = "projects/1025324209347/locations/global/workloadIdentityPools/github-actions/providers/artifactweave"
  }
}

run "trust_pins_the_immutable_subject_per_environment" {
  command = plan

  assert {
    condition     = google_iam_workload_identity_pool_provider.github.attribute_condition == "assertion.sub in ['repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-conformance', 'repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-platform']"
    error_message = "the provider must admit only this repository's two environment subjects, matched exactly"
  }

  assert {
    condition     = endswith(google_service_account_iam_member.conformance_github.member, "/subject/repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-conformance")
    error_message = "the conformance service account must trust the cloud-conformance subject only"
  }

  assert {
    condition     = endswith(google_service_account_iam_member.deployer_github.member, "/subject/repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-platform")
    error_message = "the deployer must trust the cloud-platform subject only"
  }

  assert {
    condition     = google_service_account_iam_member.deployer_github.role == "roles/iam.workloadIdentityUser" && google_service_account_iam_member.conformance_github.role == "roles/iam.workloadIdentityUser"
    error_message = "GitHub gets workload identity use, nothing broader"
  }
}

run "deployer_cannot_administer_the_project" {
  command = plan

  assert {
    condition = length(setintersection(
      toset([for m in google_project_iam_member.deployer : m.role]),
      toset(["roles/owner", "roles/editor", "roles/resourcemanager.projectIamAdmin", "roles/iam.securityAdmin", "roles/iam.serviceAccountAdmin", "roles/iam.serviceAccountKeyAdmin", "roles/serviceusage.serviceUsageAdmin"]),
    )) == 0
    error_message = "the deployer must not hold a role that administers the project, its IAM or its APIs"
  }
}

run "state_is_private_and_versioned" {
  command = plan

  assert {
    condition     = google_storage_bucket.state.public_access_prevention == "enforced" && google_storage_bucket.state.uniform_bucket_level_access
    error_message = "the state bucket must be private, with no object ACLs"
  }

  assert {
    condition     = one(google_storage_bucket.state.versioning).enabled
    error_message = "the state bucket must keep versions, so a bad apply can be undone"
  }

  assert {
    condition     = google_storage_bucket.state.force_destroy == false
    error_message = "the state bucket must refuse to be destroyed while it holds state"
  }
}
