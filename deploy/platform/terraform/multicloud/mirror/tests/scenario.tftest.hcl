# How the scenario composes modules/aws/storage, modules/gcp/storage and
# modules/azure/storage into one mirror, checked on a plan against mocked
# providers (decision 0030, S10). Each store's own baseline is its module's
# tests.

mock_provider "aws" {}
mock_provider "google" {}
mock_provider "azurerm" {}
mock_provider "random" {}

# Bucket and account names, and so the rendered configuration, come from the
# random suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "0a1b2c3d"
  }
}

variables {
  project_id          = "example-project"
  subscription_id     = "00000000-0000-0000-0000-000000000000"
  name                = "artifactweave-example"
  home_cloud          = "aws"
  aws_region          = "eu-west-2"
  gcp_location        = "europe-west2"
  azure_location      = "uksouth"
  app_role_arn        = "arn:aws:iam::111122223333:role/artifactweave"
  app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
  app_principal_id    = "00000000-0000-0000-0000-00000000000a"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    project_id          = "example-project"
    subscription_id     = "00000000-0000-0000-0000-000000000000"
    name                = "artifactweave-example"
    home_cloud          = "aws"
    aws_region          = "eu-west-2"
    gcp_location        = "europe-west2"
    azure_location      = "uksouth"
    app_role_arn        = "arn:aws:iam::111122223333:role/artifactweave"
    app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
    app_principal_id    = "00000000-0000-0000-0000-00000000000a"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX for multicloud, the labels it sets, the
# scenarios resource group the Azure deployer's rights end at, and GCP as the
# home cloud, as ci-vars.sh sets it.
run "ci_run" {
  command = plan

  variables {
    name                = "artifactweave-awscn-pr1234-123456789012-m-t"
    home_cloud          = "gcp"
    resource_group_name = "scenarios-rg"
    app_role_arn        = "arn:aws:iam::111122223333:role/artifactweave/artifactweave-deployer"
    app_service_account = "ci-deployer@example-project.iam.gserviceaccount.com"
    force_destroy       = true
    labels = {
      artifactweave-scenario = "mirror"
      instance               = "pr1234-123456789012-m-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = output.aws_bucket == "artifactweave-awscn-pr1234-123456789012-m-t-0a1b2c3d" && output.gcp_bucket == output.aws_bucket
    error_message = "the buckets must be named <name>-<suffix>, inside the artifactweave- names the AWS deployer may manage"
  }

  assert {
    condition     = length(output.aws_bucket) <= 63
    error_message = "a CI run's bucket names must fit S3's and Cloud Storage's 63 characters"
  }

  assert {
    condition     = output.azure_account == "artifactweaveaws0a1b2c3d" && can(regex("^[a-z0-9]{3,24}$", output.azure_account))
    error_message = "the account must be the name's letters and digits, cut to 16, then the suffix, within Azure's 24 lowercase letters and digits"
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 0 && output.resource_group == "scenarios-rg"
    error_message = "given a resource group, the root must create none and put the account in it"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the label reap reads from the state"
  }

  assert {
    condition = output.mirror_members == [
      { store = "gcs_europe_west2", zone = "gcp-europe-west2", signed_url_prefix = "storage.googleapis.com/artifactweave-awscn-pr1234-123456789012-m-t-0a1b2c3d/" },
      { store = "s3_eu_west_2", zone = "aws-eu-west-2", signed_url_prefix = "artifactweave-awscn-pr1234-123456789012-m-t-0a1b2c3d.s3.eu-west-2.amazonaws.com/" },
      { store = "azure_uksouth", zone = "azure-uksouth", signed_url_prefix = "artifactweaveaws0a1b2c3d.blob.core.windows.net/artifactweave/" },
    ]
    error_message = "mirror_members must list the home cloud's member first, then the others, with the prefix each one's signed URLs start with, which test/platform reads"
  }

  assert {
    condition     = output.storage_host == "storage.googleapis.com"
    error_message = "storage_host must be the home cloud's member"
  }

  assert {
    condition = (
      yamldecode(output.artifactweave_config).blob.mirror == {
        members      = ["gcs_europe_west2", "s3_eu_west_2", "azure_uksouth"]
        zone         = "gcp-europe-west2"
        write_quorum = 2
      }
    )
    error_message = "the mirror must list the home cloud's member first, take its zone, and default write_quorum to 2 of 3"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.stores.gcs_europe_west2.gcs.google_access_id == "ci-deployer@example-project.iam.gserviceaccount.com"
    error_message = "the Cloud Storage store must sign as the app service account"
  }
}

run "home_cloud_leads" {
  command = plan

  variables {
    home_cloud = "azure"
  }

  assert {
    condition     = [for m in output.mirror_members : m.store] == ["azure_uksouth", "s3_eu_west_2", "gcs_europe_west2"]
    error_message = "the home cloud's member must come first, then the others in the order aws, gcp, azure"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.zone == "azure-uksouth" && output.storage_host == "artifactweaveexa0a1b2c3d.blob.core.windows.net"
    error_message = "the zone and storage_host must be the home cloud's member's"
  }
}

run "every_store_has_its_own_zone" {
  command = plan

  assert {
    condition     = length(distinct([for s in values(yamldecode(output.artifactweave_config).blob.stores) : s.zone])) == 3
    error_message = "each member must sit in a zone of its own, named after its cloud"
  }
}

run "inputs_reach_every_store" {
  command = plan

  variables {
    store_prefix = "artifacts/"
    write_quorum = 3
    container    = "blobs"
  }

  assert {
    condition = (
      yamldecode(output.artifactweave_config).blob.stores.s3_eu_west_2.s3.prefix == "artifacts/" &&
      yamldecode(output.artifactweave_config).blob.stores.gcs_europe_west2.gcs.prefix == "artifacts/" &&
      yamldecode(output.artifactweave_config).blob.stores.azure_uksouth.azure.prefix == "artifacts/" &&
      yamldecode(output.artifactweave_config).blob.stores.azure_uksouth.azure.container == "blobs" &&
      yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 3
    )
    error_message = "store_prefix must reach every store, container the Azure one, and write_quorum the mirror"
  }
}

run "resource_group_is_created_without_one" {
  command = plan

  variables {
    labels = { team = "platform" }
  }

  assert {
    condition = (
      azurerm_resource_group.this[0].name == "artifactweave-example-rg" &&
      azurerm_resource_group.this[0].location == "uksouth" &&
      azurerm_resource_group.this[0].tags == tomap({ team = "platform" }) &&
      output.resource_group == "artifactweave-example-rg"
    )
    error_message = "without resource_group_name the root must create <name>-rg in azure_location, labelled"
  }
}

run "home_cloud_is_checked" {
  command = plan

  variables {
    home_cloud = "oci"
  }

  expect_failures = [var.home_cloud]
}

run "write_quorum_cannot_exceed_the_members" {
  command = plan

  variables {
    write_quorum = 4
  }

  expect_failures = [var.write_quorum]
}

run "name_must_leave_room_for_the_suffix" {
  command = plan

  variables {
    name = "artifactweave-awscn-a-name-much-too-long-for-a-bucket-name"
  }

  expect_failures = [var.name]
}

run "labels_follow_cloud_storage_rules" {
  command = plan

  variables {
    labels = { Team = "Platform" }
  }

  expect_failures = [var.labels]
}

run "app_identities_are_checked" {
  command = plan

  variables {
    app_role_arn        = "artifactweave"
    app_service_account = "artifactweave"
    app_principal_id    = "artifactweave"
  }

  expect_failures = [var.app_role_arn, var.app_service_account, var.app_principal_id]
}
