"""Merge current main into dependency PRs using inert Git objects only."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
from urllib.parse import urlencode

from merged import PREFIX, current
from pr import allowed_changes, api, eligible, fully_generated_paths, generated_paths, git, render
from sync import CATALOG, ROOT


class AuthoredConflict(ValueError):
    pass


def merged_tree(head, base, catalog):
    try:
        output = git("merge-tree", "--write-tree", "--name-only", "-z", head, base)
    except subprocess.CalledProcessError as error:
        if error.returncode != 1:
            raise
        output = error.output
    records = output.decode().split("\0")
    tree = records[0]
    conflicts = records[1:records.index("")]
    authored = set(conflicts) - fully_generated_paths(catalog)
    if authored:
        raise AuthoredConflict("authored conflicts require review: " + ", ".join(sorted(authored)))
    # A generated file containing conflict markers cannot be parsed as JSON.
    # Seed only those complete outputs from main, then regenerate all of them
    # from the merged inputs. No PR worktree, hooks or scripts are executed.
    replacements = {name: git("show", base + ":" + name) for name in conflicts}
    return replace_blobs(tree, replacements)


def replace_blobs(tree, replacements):
    with tempfile.TemporaryDirectory() as directory:
        env = dict(os.environ, GIT_INDEX_FILE=str(Path(directory) / "index"))
        def run(*args, data=None):
            return subprocess.check_output(["git", *args], cwd=ROOT, env=env, input=data)
        run("read-tree", tree)
        for path, data in sorted(replacements.items()):
            blob = run("hash-object", "-w", "--stdin", data=data).decode().strip()
            run("update-index", "--add", "--cacheinfo", "100644", blob, path)
        return run("write-tree").decode().strip()


def check_workflow_blobs(tree, head, base):
    # Without explicit opt-in, the token lacks Workflows write. Existing blobs
    # can be carried from either branch; a newly combined workflow needs that
    # permission, even when Git sees no textual conflict.
    changes = git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", head, tree, "--", ".github/workflows")
    for name in filter(None, changes.decode().split("\0")):
        merged = git("ls-tree", tree, "--", name)
        if merged not in (git("ls-tree", head, "--", name), git("ls-tree", base, "--", name)):
            raise AuthoredConflict("combined workflow edit requires Workflows write permission: " + name)


def refresh_one(repo, base_branch, base, pull, binary, *, workflow_write=False):
    head, branch = pull["head"]["sha"], pull["head"]["ref"]
    endpoint = "repos/" + repo + "/pulls/" + str(pull["number"])
    catalog = json.loads(CATALOG.read_text())
    files = [change for page in api(endpoint + "/files", paginate=True) for change in page]
    allowed_changes(files, catalog)
    git("fetch", "--no-tags", "origin", "refs/pull/" + str(pull["number"]) + "/head")
    if git("rev-parse", "FETCH_HEAD").decode().strip() != head:
        print("Dependency PR moved while fetching: " + str(pull["number"]))
        return
    if git("merge-base", head, base).decode().strip() == base:
        return
    tree = merged_tree(head, base, catalog)
    outputs = render(tree, binary, catalog)
    if not set(outputs) <= generated_paths(catalog):
        raise ValueError("renderer produced an unrecognized output")
    tree = replace_blobs(tree, {name: value.encode() for name, value in outputs.items()})
    if not workflow_write:
        check_workflow_blobs(tree, head, base)
    if not eligible(api(endpoint), repo, branch, head, base_branch) or not current(repo, base_branch, base):
        print("PR or default branch moved; leaving it untouched")
        return
    env = dict(os.environ, GIT_AUTHOR_NAME="weaveplatform-release-please[bot]",
               GIT_AUTHOR_EMAIL="weaveplatform-release-please[bot]@users.noreply.github.com",
               GIT_COMMITTER_NAME="weaveplatform-release-please[bot]",
               GIT_COMMITTER_EMAIL="weaveplatform-release-please[bot]@users.noreply.github.com")
    commit = subprocess.check_output([
        "git", "commit-tree", tree, "-p", head, "-p", base,
        "-m", "chore(deps): refresh branch and generated references from main",
    ], cwd=ROOT, env=env).decode().strip()
    # A concurrent PR push is non-fast-forward and rejected. --no-verify skips
    # local push hooks, and the dependency checkout was never executed.
    git("-c", "core.hooksPath=/dev/null", "push", "--no-verify", "origin", commit + ":refs/heads/" + branch)
    print("Refreshed dependency PR #" + str(pull["number"]) + "; fresh CI will run")


def refresh_open(repo, base_branch, binary):
    base = git("rev-parse", "HEAD").decode().strip()
    query = urlencode({"state": "open", "base": base_branch, "per_page": 100})
    failures = []
    for page in api("repos/" + repo + "/pulls?" + query, paginate=True):
        for pull in page:
            head = pull.get("head") or {}
            branch = head.get("ref", "")
            if branch.startswith(PREFIX) or not eligible(pull, repo, branch, head.get("sha"), base_branch):
                continue
            if not current(repo, base_branch, base):
                print("Default branch moved; the next push refreshes open dependencies")
                return
            try:
                refresh_one(repo, base_branch, base, pull, binary,
                            workflow_write=os.environ.get("DEPENDENCY_WORKFLOW_WRITE") == "true")
            except (ValueError, subprocess.CalledProcessError) as error:
                failures.append(str(pull["number"]) + ": " + str(error))
    if failures:
        raise RuntimeError("Some dependency PRs need review:\n" + "\n".join(failures))


if __name__ == "__main__":
    refresh_open(os.environ["REPO"], os.environ.get("BASE_BRANCH", "main"), os.environ["CLIENTRECIPES"])
