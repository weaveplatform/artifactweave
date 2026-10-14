#!/usr/bin/env bash
# Creates or updates the two GitHub environments the cloud trusts name
# (decision 0030). Idempotent: PUT replaces each environment's settings.
#
#   cloud-conformance  data-plane jobs (cloud-conformance.yml); no reviewers
#   cloud-platform     deployer jobs; the maintainer approves every run, from
#                      any branch, including runs they dispatched themselves
#
# PLATFORM_REVIEWER names the approving GitHub user (default ShocOne).
set -euo pipefail
reviewer=${PLATFORM_REVIEWER:-ShocOne}
reviewer_id=$(gh api "users/$reviewer" --jq .id)

gh api -X PUT 'repos/{owner}/{repo}/environments/cloud-conformance' >/dev/null
echo "environment cloud-conformance"

gh api -X PUT 'repos/{owner}/{repo}/environments/cloud-platform' --input - >/dev/null <<JSON
{
  "reviewers": [{"type": "User", "id": $reviewer_id}],
  "prevent_self_review": false,
  "deployment_branch_policy": null
}
JSON
echo "environment cloud-platform (reviewer $reviewer)"
