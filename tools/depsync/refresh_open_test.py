import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import refresh_open


class MergeObjectTests(unittest.TestCase):
    def test_generated_conflict_preserves_both_branches_authored_inputs(self):
        self.merge_fixture(authored_conflict=False)

    def test_authored_dockerfile_conflict_is_never_resolved_wholesale(self):
        self.merge_fixture(authored_conflict=True)

    def merge_fixture(self, *, authored_conflict):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-c", "core.hooksPath=/dev/null", *args], cwd=root)
            git("init", "-q")
            git("config", "user.name", "regression")
            git("config", "user.email", "regression@example.invalid")
            git("config", "commit.gpgsign", "false")
            lock = root / "docs/client-recipes/evidence-lock.json"
            lock.parent.mkdir(parents=True)
            lock.write_text('{"original": "hash"}\n')
            docker = root / "test/acceptance/clients/example.Dockerfile"
            docker.parent.mkdir(parents=True)
            docker.write_text("FROM old\n")
            git("add", "."); git("commit", "-qm", "base")
            ancestor = git("rev-parse", "HEAD").decode().strip()
            lock.write_text('{"main": "hash"}\n')
            (root / "main-input").write_text("main dependency\n")
            if authored_conflict:
                docker.write_text("FROM main\n")
            git("add", "."); git("commit", "-qm", "main")
            base = git("rev-parse", "HEAD").decode().strip()
            git("checkout", "-q", "--detach", ancestor)
            lock.write_text('{"pr": "hash"}\n')
            (root / "pr-input").write_text("PR dependency\n")
            if authored_conflict:
                docker.write_text("FROM pr\n")
            git("add", "."); git("commit", "-qm", "dependency")
            head = git("rev-parse", "HEAD").decode().strip()
            catalog = {"documents": {}, "recipes": {}}
            with patch.object(refresh_open, "ROOT", root), patch.object(refresh_open, "git", side_effect=git):
                if authored_conflict:
                    with self.assertRaisesRegex(refresh_open.AuthoredConflict, "example.Dockerfile"):
                        refresh_open.merged_tree(head, base, catalog)
                else:
                    tree = refresh_open.merged_tree(head, base, catalog)
                    self.assertEqual(b"main dependency\n", git("show", tree + ":main-input"))
                    self.assertEqual(b"PR dependency\n", git("show", tree + ":pr-input"))
                    self.assertEqual({"main": "hash"}, json.loads(git("show", tree + ":docs/client-recipes/evidence-lock.json")))
                    self.assertEqual(head, git("rev-parse", "HEAD").decode().strip())
                    self.assertEqual(b"", git("status", "--porcelain"))


class PublishGuardTests(unittest.TestCase):
    def test_moved_pr_or_main_never_commits_or_pushes(self):
        head, base = "a" * 40, "b" * 40
        pull = {"number": 1, "state": "open", "user": {"login": "dependabot[bot]"},
                "base": {"ref": "main"}, "head": {"sha": head, "ref": "dependabot/actions/update",
                "repo": {"full_name": "org/repo"}}}
        for moved_pr in [True, False]:
            current_pull = dict(pull, state="closed") if moved_pr else pull
            def api(endpoint, **kwargs):
                return [[{"filename": "go.mod", "status": "modified"}]] if endpoint.endswith("/files") else current_pull
            def git(*args):
                if args[0] == "fetch": return b""
                if args == ("rev-parse", "FETCH_HEAD"): return head.encode()
                if args[0] == "merge-base": return ("c" * 40).encode()
                self.fail("unexpected git publication: " + str(args))
            with patch.object(refresh_open, "api", side_effect=api), patch.object(refresh_open, "git", side_effect=git), \
                    patch.object(refresh_open, "merged_tree", return_value="tree"), \
                    patch.object(refresh_open, "render", return_value={}), \
                    patch.object(refresh_open, "replace_blobs", return_value="tree"), \
                    patch.object(refresh_open, "check_workflow_blobs"), \
                    patch.object(refresh_open, "current", return_value=False), \
                    patch.object(refresh_open.subprocess, "check_output") as publish:
                refresh_open.refresh_one("org/repo", "main", base, pull, "/trusted/renderer")
                publish.assert_not_called()

    def test_workflow_permission_boundary_allows_existing_blobs_only(self):
        for merged_blob in [b"head-blob", b"base-blob", b"combined-blob"]:
            def git(*args):
                if args[0] == "diff-tree": return b".github/workflows/quality-gate.yml\0"
                return {"head": b"head-blob", "base": b"base-blob", "tree": merged_blob}[args[1]]
            with patch.object(refresh_open, "git", side_effect=git):
                if merged_blob == b"combined-blob":
                    with self.assertRaisesRegex(refresh_open.AuthoredConflict, "Workflows write"):
                        refresh_open.check_workflow_blobs("tree", "head", "base")
                else:
                    refresh_open.check_workflow_blobs("tree", "head", "base")


if __name__ == "__main__":
    unittest.main()
