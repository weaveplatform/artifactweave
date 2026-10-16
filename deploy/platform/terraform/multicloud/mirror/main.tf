# One store on each of AWS, GCP and Azure as members of one artifactweave
# mirror (decision 0028): every blob is written to each, an upload succeeds
# once write_quorum of them hold it, and reads prefer the member in the cloud
# artifactweave runs in. Artifactory's sharding provider cannot do this: its
# shards are one storage kind, so its copies never leave one provider. No
# store replicates on its own, because a cloud's own copies are invisible to
# the mirror and never count toward its quorum.

# One suffix for all three, so the buckets and the account read as one
# deployment.
resource "random_id" "suffix" {
  byte_length = 4
}

resource "azurerm_resource_group" "this" {
  count    = var.resource_group_name == null ? 1 : 0
  name     = "${var.name}-rg"
  location = var.azure_location
  tags     = var.labels
}

locals {
  bucket = "${var.name}-${random_id.suffix.hex}"
  # 24 characters at most: up to 16 of the name's and the suffix (8). There
  # is one account, so unlike the azure scenarios it needs no location code.
  account             = "${substr(lower(replace(var.name, "/[^A-Za-z0-9]/", "")), 0, 16)}${random_id.suffix.hex}"
  resource_group_name = var.resource_group_name != null ? var.resource_group_name : azurerm_resource_group.this[0].name
}

module "aws" {
  source = "../../modules/aws/storage"

  name              = local.bucket
  region            = var.aws_region
  principals        = { app = var.app_role_arn }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  tags              = var.labels
}

module "gcp" {
  source = "../../modules/gcp/storage"

  name              = local.bucket
  location          = var.gcp_location
  principals        = { app = var.app_service_account }
  store_prefix      = var.store_prefix
  expiring_prefixes = var.expiring_prefixes
  force_destroy     = var.force_destroy
  labels            = var.labels
}

module "azure" {
  source = "../../modules/azure/storage"

  account_name        = local.account
  resource_group_name = local.resource_group_name
  location            = var.azure_location
  container           = var.container
  store_prefix        = var.store_prefix
  principals          = merge({ app = { principal_id = var.app_principal_id } }, var.additional_principals)
  expiring_prefixes   = var.expiring_prefixes
  tags                = var.labels
}

locals {
  prefix = var.store_prefix == "" ? {} : { prefix = var.store_prefix }
  # Each member by cloud: its store in artifactweave.yaml, which names the
  # cloud in its zone so the three never share one, and the host and prefix
  # its signed URLs start with. Each store reaches its cloud through that
  # cloud's own credential chain.
  members = {
    aws = {
      store = "s3_${replace(var.aws_region, "-", "_")}"
      config = {
        backend = "s3"
        zone    = "aws-${var.aws_region}"
        s3      = merge({ bucket = module.aws.bucket, region = var.aws_region }, local.prefix)
      }
      host              = module.aws.host
      signed_url_prefix = module.aws.signed_url_prefix
    }
    gcp = {
      store = "gcs_${replace(var.gcp_location, "-", "_")}"
      config = {
        backend = "gcs"
        zone    = "gcp-${var.gcp_location}"
        gcs     = merge({ bucket = module.gcp.bucket, google_access_id = var.app_service_account }, local.prefix)
      }
      host              = "storage.googleapis.com"
      signed_url_prefix = module.gcp.signed_url_prefix
    }
    azure = {
      store = "azure_${var.azure_location}"
      config = {
        backend = "azure"
        zone    = "azure-${var.azure_location}"
        azure   = merge({ account = module.azure.account, container = module.azure.container, credential = "default" }, local.prefix)
      }
      host              = module.azure.host
      signed_url_prefix = module.azure.signed_url_prefix
    }
  }
  # The home cloud first, then the others in a fixed order: the mirror's
  # member order, which breaks ties in read order.
  order = concat([var.home_cloud], [for c in ["aws", "gcp", "azure"] : c if c != var.home_cloud])
}
