output "region" {
  description = "Region of the state bucket and the scenarios (CONFORMANCE_AWS_REGION and PLATFORM_AWS_REGION)."
  value       = var.region
}

output "oidc_provider_arn" {
  description = "GitHub's OIDC provider in the account."
  value       = aws_iam_openid_connect_provider.github.arn
}

output "conformance_role_arn" {
  description = "The data-plane role (CONFORMANCE_AWS_ROLE_ARN); aws/single-bucket's bucket policy grants it its bucket."
  value       = aws_iam_role.conformance.arn
}

output "deployer_role_arn" {
  description = "The deployer role (PLATFORM_AWS_ROLE_ARN)."
  value       = aws_iam_role.deployer.arn
}

output "scenario_boundary_arn" {
  description = "The permissions boundary every role a scenario creates must carry."
  value       = aws_iam_policy.scenario_boundary.arn
}

output "state_bucket" {
  description = "Remote state bucket for this bootstrap and every scenario run (PLATFORM_AWS_STATE_BUCKET)."
  value       = aws_s3_bucket.state.bucket
}

output "dns_zone_id" {
  description = "The Route 53 hosted zone scenarios add records to (PLATFORM_AWS_DNS_ZONE_ID)."
  value       = aws_route53_zone.aws.zone_id
}

output "dns_name_servers" {
  description = "Name servers for the NS records at GoDaddy (aws.artifact under weaveplatform.run)."
  value       = aws_route53_zone.aws.name_servers
}
