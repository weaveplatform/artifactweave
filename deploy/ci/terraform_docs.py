"""Refresh generated README tables on current, trusted Terraform dependency PRs.

Run from a checkout of the default branch. PR files are read as data through
git objects; PR scripts, configuration, hooks and Terraform providers never run.
"""

import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import tempfile
from urllib.parse import quote, urlencode

ROOT = Path(__file__).resolve().parents[2]


def api(endpoint, payload=None, *, method=None, paginate=False):
    args = ["gh", "api", endpoint]
    if paginate:
        args += ["--paginate", "--slurp"]
    if payload is not None:
        args += ["--method", method or "POST", "--input", "-"]
    result = subprocess.run(
        args, input=json.dumps(payload) if payload is not None else None,
        stdout=subprocess.PIPE, text=True, check=True,
    )
    return json.loads(result.stdout)


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def eligible(pr, repo, branch, sha):
    head = pr.get("head") or {}
    return (
        pr.get("state") == "open"
        and pr.get("user", {}).get("login") == "dependabot[bot]"
        and pr.get("base", {}).get("ref") == "main"
        and (head.get("repo") or {}).get("full_name") == repo
        and head.get("ref") == branch
        and head.get("sha") == sha
        and branch.startswith("dependabot/terraform/")
    )


def changed_roots(files):
    roots = set()
    for change in files:
        name = change["filename"]
        path = PurePosixPath(name)
        if (
            change["status"] not in {"modified", "added"}
            or str(path) != name or ".." in path.parts
            or not name.startswith(("deploy/platform/terraform/", "deploy/ci/terraform/"))
            or not (name.endswith(".tf") or path.name in {".terraform.lock.hcl", "README.md"})
        ):
            raise ValueError("dependency PR contains changes outside Terraform inputs and READMEs")
        if path.name != "README.md":
            roots.add(str(path.parent))
    return roots


def render(sha, roots, binary, *, git=git):
    changed = {}
    with tempfile.TemporaryDirectory() as directory:
        for root in sorted(roots):
            target = Path(directory) / root
            target.mkdir(parents=True)
            entries = git("ls-tree", "-z", sha, "--", root + "/").split(b"\0")
            for entry in filter(None, entries):
                metadata, name = entry.decode().split("\t", 1)
                path = PurePosixPath(name)
                if str(path.parent) != root or not (
                    path.suffix == ".tf" or path.name in {"README.md", ".terraform.lock.hcl"}
                ):
                    continue
                mode, kind, blob = metadata.split()
                if mode != "100644" or kind != "blob":
                    raise ValueError("Terraform documentation inputs must be regular files")
                (target / path.name).write_bytes(git("cat-file", "blob", blob))
            readme = target / "README.md"
            if not readme.exists() or b"BEGIN_TF_DOCS" not in readme.read_bytes():
                continue
            original = readme.read_bytes()
            subprocess.run(
                [binary, "--config", str(ROOT / ".terraform-docs.yml"), str(target)],
                check=True, stdout=subprocess.DEVNULL,
            )
            if readme.read_bytes() != original:
                changed[root + "/README.md"] = readme.read_text()
    return changed


def refresh(repo, branch, sha, binary):
    prefix = "repos/" + repo
    query = urlencode({"head": repo.split("/")[0] + ":" + branch, "state": "open", "per_page": 100})
    prs = api(prefix + "/pulls?" + query)
    prs = [pr for pr in prs if eligible(pr, repo, branch, sha)]
    if len(prs) != 1:
        print("No current, trusted Terraform dependency PR; nothing to refresh")
        return
    pr = prs[0]
    endpoint = prefix + "/pulls/" + str(pr["number"])
    pages = api(endpoint + "/files", paginate=True)
    roots = changed_roots([change for page in pages for change in page])
    git("fetch", "--no-tags", "origin", "refs/pull/" + str(pr["number"]) + "/head")
    if git("rev-parse", "FETCH_HEAD").decode().strip() != sha:
        print("PR moved while fetching; the next gate run decides")
        return
    changed = render(sha, roots, binary)
    if not changed:
        print("Terraform READMEs are already current")
        return
    if not eligible(api(endpoint), repo, branch, sha):
        print("PR moved or closed during generation; leaving it untouched")
        return
    tree = api(prefix + "/git/trees", {
        "base_tree": git("rev-parse", sha + "^{tree}").decode().strip(),
        "tree": [{"path": path, "mode": "100644", "type": "blob", "content": content}
                 for path, content in sorted(changed.items())],
    })
    commit = api(prefix + "/git/commits", {
        "message": "docs(deploy): refresh Terraform provider references",
        "tree": tree["sha"], "parents": [sha],
    })
    # A concurrent Dependabot/human push makes this non-fast-forward and is
    # refused by GitHub. The App token makes this new commit rerun PR checks.
    api(prefix + "/git/refs/heads/" + quote(branch, safe="/"),
        {"sha": commit["sha"], "force": False}, method="PATCH")
    print("Updated " + str(len(changed)) + " README(s); waiting for the new quality gate")


if __name__ == "__main__":
    refresh(os.environ["REPO"], os.environ["HEAD_BRANCH"], os.environ["HEAD_SHA"],
            os.environ["TERRAFORM_DOCS"])
