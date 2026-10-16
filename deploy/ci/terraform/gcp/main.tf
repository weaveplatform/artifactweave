# The long-lived foundation every GCP scenario stands on (decision 0030):
# the APIs, GitHub's trust in two service accounts, the remote state bucket,
# the DNS zone and a budget. An operator applies it once; scenarios come and
# go without touching it, because a deleted workload identity pool's ID stays
# reserved for 30 days.

data "google_project" "this" {}

data "google_client_openid_userinfo" "me" {}

locals {
  conformance_subject = "${var.github_subject_prefix}:environment:${var.conformance_environment}"
  platform_subject    = "${var.github_subject_prefix}:environment:${var.platform_environment}"
}

# Everything a scenario needs, enabled here so the deployer never needs
# service-usage administration. Disabling an API on destroy would break the
# scenarios using it, so destroy leaves them on.
resource "google_project_service" "this" {
  for_each = toset([
    "billingbudgets.googleapis.com",
    "certificatemanager.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "compute.googleapis.com",
    "dns.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "storage.googleapis.com",
    "sts.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

# GitHub Actions exchange their OIDC token here (Workload Identity
# Federation), so no workflow stores a credential. The pool and provider IDs
# predate this root (the acceptance environment created them); imports.tf
# adopts them after the pool is undeleted.
resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github-actions"
  display_name              = "GitHub Actions"
  description               = "weaveplatform/artifactweave workflows, by GitHub environment."
  depends_on                = [google_project_service.this]
}

# One provider for both environments. The condition admits only this
# repository's two environment subjects, matched exactly: GitHub's immutable
# subject carries the owner and repository IDs, so a renamed or recreated
# repository of the same name cannot match. Each service account then trusts
# one subject, not the whole provider.
resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "artifactweave"
  display_name                       = "weaveplatform/artifactweave"
  attribute_mapping = {
    "google.subject"           = "assertion.sub"
    "attribute.repository_id"  = "assertion.repository_id"
    "attribute.environment"    = "assertion.environment"
    "attribute.workflow_ref"   = "assertion.workflow_ref"
    "attribute.repository_ref" = "assertion.ref"
  }
  attribute_condition = "assertion.sub in ['${local.conformance_subject}', '${local.platform_subject}']"
  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

# The data-plane identity: object rights on the conformance bucket only,
# granted by gcp/single-bucket on its bucket. It signs download URLs through
# IAM signBlob as itself, and the operator's own login may act as it, so the
# local runner (deploy/ci/local.sh) needs no key (the organisation forbids
# keys).
resource "google_service_account" "conformance" {
  account_id   = "artifactweave-conformance"
  display_name = "artifactweave conformance"
  description  = "cloud-conformance.yml and the local runner; object rights on the single-bucket scenario's bucket only."
  depends_on   = [google_project_service.this]
}

resource "google_service_account_iam_member" "conformance_github" {
  service_account_id = google_service_account.conformance.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principal://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/subject/${local.conformance_subject}"
}

resource "google_service_account_iam_member" "conformance_signs_as_itself" {
  service_account_id = google_service_account.conformance.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.conformance.email}"
}

resource "google_service_account_iam_member" "operator_impersonates_conformance" {
  service_account_id = google_service_account.conformance.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "user:${data.google_client_openid_userinfo.me.email}"
}

# The deployer: creates and destroys scenario resources in this lab project,
# behind the cloud-platform environment's approval. Its roles cover what the
# scenarios use (buckets, load balancers and Cloud CDN, managed
# certificates) and nothing that administers IAM, projects or APIs.
resource "google_service_account" "deployer" {
  account_id   = "artifactweave-deployer"
  display_name = "artifactweave deployer"
  description  = "Scenario applies and destroys from the cloud-platform GitHub environment."
  depends_on   = [google_project_service.this]
}

resource "google_service_account_iam_member" "deployer_github" {
  service_account_id = google_service_account.deployer.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principal://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/subject/${local.platform_subject}"
}

# Scenario tests sign download URLs through signBlob as the deployer.
resource "google_service_account_iam_member" "deployer_signs_as_itself" {
  service_account_id = google_service_account.deployer.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.deployer.email}"
}

resource "google_project_iam_member" "deployer" {
  for_each = toset([
    # Scenario buckets, their IAM (cloud-cdn-fill's read grant) and the
    # objects the tests write; also the scenario state under scenarios/.
    "roles/storage.admin",
    # Backend buckets, URL maps, target proxies, forwarding rules, global
    # addresses, SSL certificates and Cloud CDN signed URL keys.
    "roles/compute.loadBalancerAdmin",
    # Managed certificates and their DNS authorisations.
    "roles/certificatemanager.editor",
    # Calling the enabled APIs as a federated identity.
    "roles/serviceusage.serviceUsageConsumer",
  ])
  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.deployer.email}"
}

# Records in the scenarios' zone only, for certificate authorisations and
# the registry and CDN hostnames.
resource "google_dns_managed_zone_iam_member" "deployer" {
  managed_zone = google_dns_managed_zone.gcp.name
  role         = "roles/dns.admin"
  member       = "serviceAccount:${google_service_account.deployer.email}"
}
