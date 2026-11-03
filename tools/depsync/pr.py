"""Commit derived references to current same-repository dependency PRs.

Run only from trusted default-branch automation. PR git blobs are inert inputs;
neither PR code, templates, hooks nor Go dependencies are executed. The trusted
clientrecipes binary is built before this program receives a write token.
"""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import quote, urlencode

from sync import CATALOG, ROOT, safe_path, sync


def api(endpoint, payload=None, *, method=None, paginate=False):
    args = ["gh", "api", endpoint]
    if paginate:
        args += ["--paginate", "--slurp"]
    if payload is not None:
        args += ["--method", method or "POST", "--input", "-"]
    result = subprocess.run(args, input=json.dumps(payload) if payload is not None else None,
                            stdout=subprocess.PIPE, text=True, check=True)
    return json.loads(result.stdout)


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def eligible(pr, repo, branch, sha, base="main"):
    head = pr.get("head") or {}
    author = pr.get("user", {}).get("login")
    trusted = (author == "dependabot[bot]" and branch.startswith("dependabot/")) or (
        author == "weaveplatform-release-please[bot]" and branch in {
            "deps/refresh", "deps/jfrog-providers", "deps/jfrog-providers-major"})
    return (trusted and pr.get("state") == "open" and pr.get("base", {}).get("ref") == base
            and (head.get("repo") or {}).get("full_name") == repo
            and head.get("ref") == branch and head.get("sha") == sha)


def generated_paths(catalog):
    recipes = set(catalog["recipes"])
    return (set(catalog["documents"]) | {rule["path"] for rule in catalog["references"]}
            | {"docs/client-recipes/src/" + name + ".json" for name in recipes}
            | {"docs/client-recipes/" + name + ".md" for name in recipes}
            | {"docs/client-recipes/index.md", "docs/client-recipes/evidence-lock.json"})


def allowed_changes(files, catalog):
    allowed = generated_paths(catalog) | {p["path"] for p in catalog["pins"].values()} | {
        "go.mod", "go.sum", ".github/golangci-lint-version", ".github/deps-refresh.pins"}
    for change in files:
        name = change["filename"]
        safe_path(ROOT, name)
        action = name.startswith((".github/workflows/", ".github/actions/")) and name.endswith((".yml", ".yaml"))
        terraform = name.startswith(("deploy/platform/terraform/", "deploy/ci/terraform/", "deploy/artifactweave/terraform/")) and (
            name.endswith(".tf") or name.endswith("/.terraform.lock.hcl") or name.endswith("/README.md"))
        if change["status"] != "modified" or not (name in allowed or action or terraform):
            raise ValueError("dependency PR changes unrecognized input: " + name)


def snapshot(sha, target, catalog, *, git=git):
    entries = {}
    for record in filter(None, git("ls-tree", "-r", "-z", sha).split(b"\0")):
        metadata, name = record.decode().split("\t", 1)
        mode, kind, blob = metadata.split()
        entries[name] = (mode, kind, blob)

    def copy(name):
        path = safe_path(target, name)
        mode, kind, blob = entries[name]
        if mode not in {"100644", "100755"} or kind != "blob":
            raise ValueError("dependency snapshot requires a regular file: " + name)
        data = git("cat-file", "blob", blob)
        if len(data) > 16 * 1024 * 1024:
            raise ValueError("dependency input exceeds 16 MiB: " + name)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)

    paths = generated_paths(catalog) | {p["path"] for p in catalog["pins"].values()} | set(catalog["images"])
    paths.add("docs/implementation/package-format-inventory.json")
    for name in sorted(paths):
        copy(name)
    for name in catalog["recipes"]:
        recipe = json.loads((target / ("docs/client-recipes/src/" + name + ".json")).read_text())
        for path in recipe["version_sources"] + [r["path"] for r in recipe["evidence"]]:
            copy(path)


def render(sha, binary, catalog, *, git=git):
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        snapshot(sha, root, catalog, git=git)
        outputs = generated_paths(catalog)
        before = {path: safe_path(root, path).read_text() for path in outputs}
        sync(root)
        # Absolute, trusted binary; no shell and no PR Go/module execution.
        subprocess.run([str(Path(binary).resolve())], cwd=root, check=True,
                       env={"PATH": os.environ.get("PATH", ""), "GOWORK": "off"})
        return {path: safe_path(root, path).read_text() for path in sorted(outputs)
                if safe_path(root, path).read_text() != before[path]}


def refresh(repo, branch, sha, binary, base="main"):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("expected a full immutable head SHA")
    catalog = json.loads(CATALOG.read_text())
    prefix = "repos/" + repo
    query = urlencode({"head": repo.split("/")[0] + ":" + branch, "state": "open", "per_page": 100})
    prs = [pr for pr in api(prefix + "/pulls?" + query) if eligible(pr, repo, branch, sha, base)]
    if len(prs) != 1:
        print("No current trusted dependency PR; nothing to refresh")
        return
    endpoint = prefix + "/pulls/" + str(prs[0]["number"])
    files = [change for page in api(endpoint + "/files", paginate=True) for change in page]
    allowed_changes(files, catalog)
    git("fetch", "--no-tags", "origin", "refs/pull/" + str(prs[0]["number"]) + "/head")
    if git("rev-parse", "FETCH_HEAD").decode().strip() != sha:
        print("PR moved while fetching; the next gate run decides")
        return
    changed = render(sha, binary, catalog)
    if not changed:
        print("Dependency references are already current")
        return
    if not eligible(api(endpoint), repo, branch, sha, base):
        print("PR moved or closed during generation; leaving it untouched")
        return
    tree = api(prefix + "/git/trees", {
        "base_tree": git("rev-parse", sha + "^{tree}").decode().strip(),
        "tree": [{"path": path, "mode": "100644", "type": "blob", "content": content}
                 for path, content in sorted(changed.items())],
    })
    commit = api(prefix + "/git/commits", {
        "message": "chore(deps): synchronize dependency references",
        "tree": tree["sha"], "parents": [sha],
    })
    # A concurrent push makes this non-fast-forward; GitHub rejects it. App
    # commits trigger fresh PR checks, unlike commits made with GITHUB_TOKEN.
    api(prefix + "/git/refs/heads/" + quote(branch, safe="/"),
        {"sha": commit["sha"], "force": False}, method="PATCH")
    print("Updated " + str(len(changed)) + " references; waiting for the new quality gate")


if __name__ == "__main__":
    refresh(os.environ["REPO"], os.environ["HEAD_BRANCH"], os.environ["HEAD_SHA"],
            os.environ["CLIENTRECIPES"], os.environ.get("BASE_BRANCH", "main"))
