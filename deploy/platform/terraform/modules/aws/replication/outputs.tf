output "role_arn" {
  description = "ARN of the role S3 assumes to replicate."
  value       = aws_iam_role.replication.arn
}

output "role_name" {
  description = "Name of the role S3 assumes to replicate."
  value       = aws_iam_role.replication.name
}

output "prefix" {
  description = "The key prefix under which objects replicate; empty for the whole bucket."
  value       = var.prefix
}
