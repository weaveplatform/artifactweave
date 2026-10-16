# Outputs only the test harness reads (decision 0030, S5). A consumer can
# delete this file.

output "expires_at" {
  description = "For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise."
  value       = lookup(var.labels, "expires-at", "")
}

output "signed_url_key_name" {
  description = "For the test harness: the signed URL key's name, which scenario-ci.sh and lab-vars.sh hand to cloud-cdn-key.sh to add the key after the first apply."
  value       = var.signed_url_key_name
}
