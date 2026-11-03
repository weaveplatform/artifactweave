"""Finish generated repairs when approval arrives after their quality gate."""

import json
import os
import subprocess
from urllib.parse import urlencode

from merged import PREFIX
from pr import api, eligible, generated_paths
from sync import CATALOG


def ready(pull, status, repo, base):
    head = pull.get("head") or {}
    branch = head.get("ref", "")
    return (branch.startswith(PREFIX) and eligible(pull, repo, branch, head.get("sha"), base)
            and status.get("headRefOid") == head["sha"]
            and status.get("reviewDecision") == "APPROVED"
            and any(check.get("name") == "Quality gate" and check.get("conclusion") == "SUCCESS"
                    and check.get("status") == "COMPLETED" for check in status.get("statusCheckRollup", [])))


def merge_ready(repo, base):
    query = urlencode({"state": "open", "base": base, "per_page": 100})
    catalog = json.loads(CATALOG.read_text())
    for page in api("repos/" + repo + "/pulls?" + query, paginate=True):
        for pull in page:
            head = pull.get("head") or {}
            branch = head.get("ref", "")
            if not branch.startswith(PREFIX) or not eligible(pull, repo, branch, head.get("sha"), base):
                continue
            number = str(pull["number"])
            status = json.loads(subprocess.check_output([
                "gh", "pr", "view", number, "--repo", repo,
                "--json", "headRefOid,reviewDecision,statusCheckRollup",
            ], text=True))
            if not ready(pull, status, repo, base):
                continue
            files = [change for page in api("repos/" + repo + "/pulls/" + number + "/files", paginate=True) for change in page]
            # Preserve the existing manual merge policy for native client edits.
            if any(f["status"] != "modified" or f["filename"] not in generated_paths(catalog)
                   or f["filename"].startswith("test/acceptance/clients/") for f in files):
                continue
            subprocess.run(["gh", "pr", "merge", number, "--repo", repo, "--squash", "--delete-branch",
                            "--match-head-commit", head["sha"]], check=True)


if __name__ == "__main__":
    merge_ready(os.environ["REPO"], os.environ.get("BASE_BRANCH", "main"))
