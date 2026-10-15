# Outputs only the test harness reads (decision 0030, S5). A consumer can
# delete this file.

output "expires_at" {
  description = "For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise."
  value       = lookup(var.labels, "expires-at", "")
}
