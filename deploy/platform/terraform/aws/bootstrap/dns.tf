# aws.artifact.weaveplatform.run, delegated from GoDaddy (which serves
# weaveplatform.run) by NS records naming this zone's name servers. Created
# by CLI on 2026-10-14 and imported here (imports.tf). Recreating the zone
# would assign a new delegation set and silently break the delegation, so
# Terraform refuses to destroy it. A reusable delegation set would survive a
# recreation, but one can be attached only when a zone is created; if the
# zone ever must be rebuilt, make its name servers reusable first with
# `aws route53 create-reusable-delegation-set --hosted-zone-id`.
resource "aws_route53_zone" "aws" {
  name    = "aws.artifact.weaveplatform.run"
  comment = "artifactweave bootstrap zone; import into Terraform in #128"

  lifecycle {
    prevent_destroy = true
  }
}
