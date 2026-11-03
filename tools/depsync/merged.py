"""Reconcile generated dependency references after updates reach main.

The workflow checks out trusted main and builds the renderer before obtaining
write credentials. Repairs use an immutable base and a dedicated PR; neither
main nor an existing branch is force-pushed.
"""

import json
import os
from urllib.parse import quote, urlencode

from pr import api, eligible, generated_paths, git, render
from sync import CATALOG


PREFIX = "deps/references/"


def current(repo, base, sha):
    ref = api("repos/" + repo + "/git/ref/heads/" + quote(base, safe="/"))
    return ref["object"]["sha"] == sha


def retire(repo, base, sha, keep=None):
    # A newer push owns the next reconciliation, including retirement decisions.
    if not current(repo, base, sha):
        return
    catalog = json.loads(CATALOG.read_text())
    prefix = "repos/" + repo
    query = urlencode({"state": "open", "base": base, "per_page": 100})
    for page in api(prefix + "/pulls?" + query, paginate=True):
        for pull in page:
            head = pull.get("head") or {}
            branch = head.get("ref", "")
            if branch == keep or not branch.startswith(PREFIX):
                continue
            if not eligible(pull, repo, branch, head.get("sha"), base):
                continue
            endpoint = prefix + "/pulls/" + str(pull["number"])
            files = [change for page in api(endpoint + "/files", paginate=True) for change in page]
            if any(f["status"] != "modified" or f["filename"] not in generated_paths(catalog) for f in files):
                raise ValueError("repair PR contains changes outside generated references")
            latest = api(endpoint)
            if eligible(latest, repo, branch, head["sha"], base) and current(repo, base, sha):
                api(endpoint, {"state": "closed"}, method="PATCH")


def reconcile(repo, base, binary):
    sha = git("rev-parse", "HEAD").decode().strip()
    if not current(repo, base, sha):
        print("Default branch moved; the next push reconciles its current tree")
        return
    catalog = json.loads(CATALOG.read_text())
    changed = render(sha, binary, catalog)
    if not changed:
        print("Merged dependency references are current")
        retire(repo, base, sha)
        return
    if not set(changed) <= generated_paths(catalog):
        raise ValueError("renderer produced an unrecognized output")

    branch = PREFIX + sha
    prefix = "repos/" + repo
    query = urlencode({"head": repo.split("/")[0] + ":" + branch, "state": "open", "base": base})
    pulls = api(prefix + "/pulls?" + query)
    if pulls:
        if len(pulls) != 1 or not eligible(pulls[0], repo, branch, pulls[0]["head"]["sha"], base):
            raise ValueError("repair branch belongs to an unexpected PR")
        print("Existing repair: " + pulls[0]["html_url"])
        retire(repo, base, sha, keep=branch)
        return

    tree = api(prefix + "/git/trees", {
        "base_tree": git("rev-parse", sha + "^{tree}").decode().strip(),
        "tree": [{"path": path, "mode": "100644", "type": "blob", "content": content}
                 for path, content in sorted(changed.items())],
    })
    # Recover a retry interrupted between branch creation and PR creation, but
    # never replace a pre-existing branch with different content or ancestry.
    refs = api(prefix + "/git/matching-refs/heads/" + quote(branch, safe="/"))
    refs = [ref for ref in refs if ref["ref"] == "refs/heads/" + branch]
    if refs:
        commit = api(prefix + "/git/commits/" + refs[0]["object"]["sha"])
        if commit["tree"]["sha"] != tree["sha"] or [p["sha"] for p in commit["parents"]] != [sha]:
            raise ValueError("existing repair branch has different content or ancestry")
    else:
        commit = api(prefix + "/git/commits", {
            "message": "chore(deps): reconcile merged dependency references",
            "tree": tree["sha"], "parents": [sha],
        })
        if not current(repo, base, sha):
            print("Default branch moved during rendering; leaving branches untouched")
            return
        api(prefix + "/git/refs", {"ref": "refs/heads/" + branch, "sha": commit["sha"]})
    if not current(repo, base, sha):
        print("Default branch moved before publication; the next push reconciles it")
        return
    pull = api(prefix + "/pulls", {
        "head": branch, "base": base,
        "title": "chore(deps): reconcile merged dependency references",
        "body": "Regenerated dependency references and client evidence fingerprints from merged commit `"
                + sha + "`. Only declared generated outputs are changed.\n\n"
                "Raised by dependency-references.yml. The App commit triggers fresh CI; "
                "existing review, quality-gate and native-client merge rules still apply.",
    })
    print("Published repair: " + pull["html_url"])
    retire(repo, base, sha, keep=branch)


if __name__ == "__main__":
    reconcile(os.environ["REPO"], os.environ.get("BASE_BRANCH", "main"), os.environ["CLIENTRECIPES"])
