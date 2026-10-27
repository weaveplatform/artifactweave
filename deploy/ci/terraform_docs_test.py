"""Offline regression tests for dependency PR documentation repair."""

import copy
import importlib.util
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "terraform_docs", Path(__file__).with_name("terraform_docs.py")
)
docs = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docs)
ROOT = Path(__file__).resolve().parents[2]
REPO = "weaveplatform/artifactweave"
BRANCH = "dependabot/terraform/deploy/ci/terraform/azure/providers"
SHA = "a" * 40


def pull_request():
    return {
        "number": 1,
        "state": "open",
        "user": {"login": "dependabot[bot]"},
        "base": {"ref": "main"},
        "head": {"ref": BRANCH, "sha": SHA, "repo": {"full_name": REPO}},
    }


class TerraformDocsTests(unittest.TestCase):
    def test_only_current_same_repository_dependabot_heads_are_eligible(self):
        pr = pull_request()
        self.assertTrue(docs.eligible(pr, REPO, BRANCH, SHA))
        cases = [
            ("state", "closed"),
            ("user.login", "someone"),
            ("base.ref", "release"),
            ("head.repo.full_name", "someone/fork"),
            ("head.ref", "untrusted"),
            ("head.sha", "b" * 40),
        ]
        for field, value in cases:
            with self.subTest(field=field):
                changed = copy.deepcopy(pr)
                target = changed
                *parents, key = field.split(".")
                for parent in parents:
                    target = target[parent]
                target[key] = value
                self.assertFalse(docs.eligible(changed, REPO, BRANCH, SHA))
        pr["head"]["repo"] = None
        self.assertFalse(docs.eligible(pr, REPO, BRANCH, SHA))

    def test_provider_updates_select_roots_and_reject_executable_changes(self):
        root = "deploy/ci/terraform/azure"
        files = [
            {"filename": root + "/.terraform.lock.hcl", "status": "modified"},
            {"filename": root + "/README.md", "status": "modified"},
        ]
        self.assertEqual(docs.changed_roots(files), {root})
        for name in [
            ".github/workflows/quality-gate.yml",
            "deploy/check.sh",
            root + "/../evil.tf",
            root + "/.terraform-docs.yml",
        ]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                docs.changed_roots(files + [{"filename": name, "status": "modified"}])
        with self.assertRaises(ValueError):
            docs.changed_roots([{"filename": root + "/main.tf", "status": "renamed"}])

    def test_real_azure_drift_is_regenerated_without_touching_prose(self):
        binary = shutil.which("terraform-docs")
        self.assertIsNotNone(binary, "pinned terraform-docs must be on PATH")
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            root = "deploy/ci/terraform/azure"
            target = fixture / root
            target.mkdir(parents=True)
            for source in (ROOT / root).iterdir():
                if source.suffix == ".tf" or source.name in {"README.md", ".terraform.lock.hcl"}:
                    shutil.copyfile(source, target / source.name)
            readme = target / "README.md"
            subprocess.run([binary, "--config", str(ROOT / ".terraform-docs.yml"), str(target)],
                           check=True, stdout=subprocess.DEVNULL)
            expected = readme.read_text()
            stale, count = re.subn(r"(?m)^\| azurerm \| [0-9]+\.[0-9]+\.[0-9]+ \|$", "| azurerm | 0.0.0 |", expected)
            self.assertEqual(count, 1)
            readme.write_text(stale)
            subprocess.run(["git", "init", "-q", directory], check=True)
            # Both fixture commits must work without a runner's Git identity.
            subprocess.run(["git", "-C", directory, "config", "user.name", "test"], check=True)
            subprocess.run(["git", "-C", directory, "config", "user.email", "test@example.com"], check=True)
            subprocess.run(["git", "-C", directory, "add", "."], check=True)
            subprocess.run(
                ["git", "-C", directory, "commit", "-qm", "fixture"], check=True
            )
            def git(*args):
                return subprocess.check_output(["git", "-C", directory, *args])
            changed = docs.render("HEAD", {root}, binary, git=git)
            self.assertEqual(changed, {root + "/README.md": expected})
            self.assertIn("| azurerm | 0.0.0 |", readme.read_text())
            readme.write_text(expected)
            subprocess.run(["git", "-C", directory, "add", "."], check=True)
            subprocess.run(["git", "-C", directory, "commit", "-qm", "fresh"], check=True)
            self.assertEqual(docs.render("HEAD", {root}, binary, git=git), {})

    def test_repair_commits_only_readmes_and_updates_branch_without_force(self):
        writes = []
        def api(endpoint, payload=None, **kwargs):
            if payload is not None:
                writes.append((endpoint, payload, kwargs))
                return {"sha": "c" * 40}
            if endpoint.endswith("/files"):
                return [[{"filename": "deploy/ci/terraform/azure/.terraform.lock.hcl", "status": "modified"}]]
            if "/pulls?" in endpoint:
                return [pull_request()]
            return pull_request()
        def git(*args):
            return (SHA + "\n").encode()
        with patch.object(docs, "api", side_effect=api), patch.object(docs, "git", side_effect=git), \
                patch.object(docs, "render", return_value={"deploy/ci/terraform/azure/README.md": "fresh"}):
            docs.refresh(REPO, BRANCH, SHA, "terraform-docs")
        self.assertEqual(len(writes), 3)
        tree = writes[0][1]["tree"]
        self.assertEqual(tree, [{"path": "deploy/ci/terraform/azure/README.md", "mode": "100644",
                                 "type": "blob", "content": "fresh"}])
        self.assertEqual(writes[1][1]["parents"], [SHA])
        self.assertIs(writes[2][1]["force"], False)
        self.assertEqual(writes[2][2]["method"], "PATCH")

    def test_stale_head_or_unchanged_docs_never_write(self):
        for moved in [False, True]:
            with self.subTest(moved=moved):
                pr = pull_request()
                calls = []
                def api(endpoint, payload=None, **kwargs):
                    self.assertIsNone(payload, "no mutation is allowed")
                    calls.append(endpoint)
                    if endpoint.endswith("/files"):
                        return [[{"filename": "deploy/ci/terraform/azure/main.tf", "status": "modified"}]]
                    if "/pulls?" in endpoint:
                        return [pull_request()]
                    pr["head"]["sha"] = "b" * 40
                    return pr
                with patch.object(docs, "api", side_effect=api), \
                        patch.object(docs, "git", return_value=(SHA + "\n").encode()), \
                        patch.object(docs, "render", return_value={"x/README.md": "fresh"} if moved else {}):
                    docs.refresh(REPO, BRANCH, SHA, "terraform-docs")

    def test_fetch_race_or_generator_failure_never_publish(self):
        for scenario in ["moved while fetching", "generator failed"]:
            with self.subTest(scenario=scenario):
                def api(endpoint, payload=None, **kwargs):
                    self.assertIsNone(payload, "no mutation is allowed")
                    if "/pulls?" in endpoint:
                        return [pull_request()]
                    return [[{"filename": "deploy/ci/terraform/azure/main.tf", "status": "modified"}]]
                fetched = "b" * 40 if scenario == "moved while fetching" else SHA
                with patch.object(docs, "api", side_effect=api), \
                        patch.object(docs, "git", return_value=(fetched + "\n").encode()), \
                        patch.object(docs, "render", side_effect=RuntimeError("generator failed")) as render:
                    if scenario == "generator failed":
                        with self.assertRaises(RuntimeError):
                            docs.refresh(REPO, BRANCH, SHA, "terraform-docs")
                    else:
                        docs.refresh(REPO, BRANCH, SHA, "terraform-docs")
                        render.assert_not_called()

    def test_symlink_inputs_are_rejected_without_reading_target(self):
        def git(*args):
            self.assertEqual(args[0], "ls-tree")
            return b"120000 blob aaaaaaaa\tdeploy/ci/terraform/azure/main.tf\0"
        with self.assertRaises(ValueError):
            docs.render(SHA, {"deploy/ci/terraform/azure"}, "terraform-docs", git=git)


if __name__ == "__main__":
    unittest.main()
