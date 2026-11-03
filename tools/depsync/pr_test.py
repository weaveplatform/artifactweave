import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import pr


SHA = "a" * 40
REPO = "weaveplatform/artifactweave"
BRANCH = "dependabot/docker/acceptance-clients"


def pull():
    return {"number": 123, "state": "open", "user": {"login": "dependabot[bot]"},
            "base": {"ref": "main"}, "head": {"repo": {"full_name": REPO}, "ref": BRANCH, "sha": SHA}}


class AutomationTests(unittest.TestCase):
    def test_eligibility_requires_author_branch_repository_base_and_current_head(self):
        self.assertTrue(pr.eligible(pull(), REPO, BRANCH, SHA))
        variants = []
        for key, value in [("state", "closed"), ("user", {"login": "someone"}), ("base", {"ref": "release"})]:
            candidate = pull(); candidate[key] = value; variants.append(candidate)
        for key, value in [("repo", {"full_name": "fork/artifactweave"}), ("sha", "b" * 40), ("ref", "feature")]:
            candidate = pull(); candidate["head"][key] = value; variants.append(candidate)
        for candidate in variants:
            self.assertFalse(pr.eligible(candidate, REPO, BRANCH, SHA))

    def test_pr_cannot_replace_automation_templates_or_scripts(self):
        catalog = json.loads(pr.CATALOG.read_text())
        for name in ["tools/depsync/sync.py", "tools/depsync/catalog.json", "tools/clientrecipes/main.go", "../outside"]:
            with self.assertRaises(ValueError):
                pr.allowed_changes([{"filename": name, "status": "modified"}], catalog)
        pr.allowed_changes([{"filename": "go.mod", "status": "modified"},
                            {"filename": ".github/workflows/quality-gate.yml", "status": "modified"}], catalog)

    def refresh_with(self, *, moved=False, closed=False, stale_fetch=False, changed=True, concurrent_push=False):
        calls = []
        candidate = pull()
        def api(endpoint, payload=None, **kwargs):
            calls.append((endpoint, payload, kwargs))
            if "/pulls?" in endpoint: return [pull()]
            if endpoint.endswith("/files"): return [[{"filename": "go.mod", "status": "modified"}]]
            if endpoint.endswith("/pulls/123"):
                current = copy.deepcopy(candidate)
                if moved: current["head"]["sha"] = "b" * 40
                if closed: current["state"] = "closed"
                return current
            if endpoint.endswith("/git/trees"): return {"sha": "tree"}
            if endpoint.endswith("/git/commits"): return {"sha": "commit"}
            if "/git/refs/heads/" in endpoint:
                if concurrent_push: raise subprocess.CalledProcessError(1, ["gh", "api"])
                return {}
            raise AssertionError(endpoint)
        def git(*args):
            if args[:2] == ("rev-parse", "FETCH_HEAD"):
                return (("b" * 40 if stale_fetch else SHA) + "\n").encode()
            if args[0] == "rev-parse": return b"tree\n"
            return b""
        with patch.object(pr, "api", side_effect=api), patch.object(pr, "git", side_effect=git), patch.object(pr, "render", return_value={"docs/client-recipes/go.md": "updated"} if changed else {}) as render:
            if concurrent_push:
                with self.assertRaises(subprocess.CalledProcessError):
                    pr.refresh(REPO, BRANCH, SHA, "/trusted/clientrecipes")
            else:
                pr.refresh(REPO, BRANCH, SHA, "/trusted/clientrecipes")
            if stale_fetch: render.assert_not_called()
        return calls

    def test_stale_or_closed_pr_never_writes(self):
        for options in [{"moved": True}, {"closed": True}, {"stale_fetch": True}, {"changed": False}]:
            calls = self.refresh_with(**options)
            self.assertFalse(any(payload is not None for _, payload, _ in calls))

    def test_commit_preserves_parent_and_ref_update_cannot_overwrite_concurrent_push(self):
        for concurrent in [False, True]:
            calls = self.refresh_with(concurrent_push=concurrent)
            commit = next(payload for endpoint, payload, _ in calls if endpoint.endswith("/git/commits"))
            self.assertEqual([SHA], commit["parents"])
            update = next((payload, kwargs) for endpoint, payload, kwargs in calls if "/git/refs/heads/" in endpoint)
            self.assertEqual(({"sha": "commit", "force": False}, {"method": "PATCH"}), update)

    def test_snapshot_rejects_symlink_blobs_without_reading_their_targets(self):
        catalog = {"recipes": {}, "documents": {}, "references": [], "pins": {}, "images": []}
        name = "docs/implementation/package-format-inventory.json"
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                if args[0] == "ls-tree":
                    paths = pr.generated_paths(catalog) | {name}
                    return b"".join((("120000" if path == name else "100644") + " blob " + ("link" if path == name else "regular") + "\t" + path + "\0").encode() for path in paths)
                if args[-1] == "regular": return b"{}"
                raise AssertionError("symlink content must not be read")
            with self.assertRaisesRegex(ValueError, "regular file"):
                pr.snapshot(SHA, Path(directory), catalog, git=git)


if __name__ == "__main__":
    unittest.main()
