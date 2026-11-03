import copy
import unittest
from unittest.mock import patch

import merged


SHA = "a" * 40
REPO = "weaveplatform/artifactweave"
BRANCH = merged.PREFIX + SHA
OUTPUT = "docs/client-recipes/evidence-lock.json"


def repair(branch=BRANCH, author="weaveplatform-release-please[bot]"):
    return {"number": 42, "state": "open", "user": {"login": author},
            "base": {"ref": "main"}, "html_url": "https://github.com/repair/42",
            "head": {"repo": {"full_name": REPO}, "ref": branch, "sha": "b" * 40}}


class MergedReferenceTests(unittest.TestCase):
    def run_reconcile(self, *, changed=True, existing=False, orphan=False, collision=False,
                      move_at=0, old=None, extra_output=False, old_path=OUTPUT):
        calls = []
        reads = 0
        def api(endpoint, payload=None, **kwargs):
            nonlocal reads
            calls.append((endpoint, payload, kwargs))
            if "/git/ref/heads/main" in endpoint:
                reads += 1
                return {"object": {"sha": "c" * 40 if move_at and reads >= move_at else SHA}}
            if "/pulls?head=" in endpoint:
                return [repair()] if existing else []
            if "/pulls?state=" in endpoint:
                return [[old]] if old else [[]]
            if endpoint.endswith("/pulls/42/files"):
                return [[{"filename": old_path, "status": "modified"}]]
            if endpoint.endswith("/pulls/42"):
                return copy.deepcopy(old)
            if endpoint.endswith("/git/trees"):
                return {"sha": "generated-tree"}
            if "/git/matching-refs/" in endpoint:
                return [{"ref": "refs/heads/" + BRANCH, "object": {"sha": "repair"}}] if orphan else []
            if endpoint.endswith("/git/commits/repair"):
                return {"tree": {"sha": "unexpected" if collision else "generated-tree"}, "parents": [{"sha": SHA}]}
            if endpoint.endswith("/git/commits"):
                return {"sha": "repair"}
            if endpoint.endswith("/git/refs"):
                return {}
            if endpoint.endswith("/pulls"):
                return repair()
            raise AssertionError((endpoint, payload))
        def git(*args):
            return (SHA if args == ("rev-parse", "HEAD") else "main-tree").encode()
        output = {OUTPUT: "updated fingerprints"} if changed else {}
        if extra_output:
            output["tools/depsync/merged.py"] = "unexpected executable"
        with patch.object(merged, "api", side_effect=api), patch.object(merged, "git", side_effect=git), \
                patch.object(merged, "render", return_value=output) as render:
            if collision or extra_output:
                with self.assertRaises(ValueError):
                    merged.reconcile(REPO, "main", "/trusted/renderer")
            else:
                merged.reconcile(REPO, "main", "/trusted/renderer")
            if move_at == 1:
                render.assert_not_called()
        return calls

    def test_stale_merged_lock_opens_only_a_generated_repair_pr(self):
        calls = self.run_reconcile()
        tree = next(p for e, p, _ in calls if e.endswith("/git/trees"))
        self.assertEqual("main-tree", tree["base_tree"])
        self.assertEqual([OUTPUT], [entry["path"] for entry in tree["tree"]])
        commit = next(p for e, p, _ in calls if e.endswith("/git/commits"))
        self.assertEqual([SHA], commit["parents"])
        ref = next(p for e, p, _ in calls if e.endswith("/git/refs"))
        self.assertEqual("refs/heads/" + BRANCH, ref["ref"])
        pull = next(p for e, p, _ in calls if e.endswith("/pulls"))
        self.assertEqual((BRANCH, "main"), (pull["head"], pull["base"]))
        self.assertFalse(any("force" in (p or {}) or k.get("method") == "PATCH" for _, p, k in calls))

    def test_current_main_does_not_create_an_empty_commit_or_pr(self):
        calls = self.run_reconcile(changed=False)
        self.assertFalse(any(p is not None for _, p, _ in calls))

    def test_existing_repair_is_idempotent(self):
        calls = self.run_reconcile(existing=True)
        self.assertFalse(any(p is not None for _, p, _ in calls))

    def test_newer_main_never_receives_a_stale_repair(self):
        for read in [1, 2, 3]:
            with self.subTest(read=read):
                calls = self.run_reconcile(move_at=read)
                self.assertFalse(any(e.endswith("/pulls") and p for e, p, _ in calls))
                self.assertFalse(any(k.get("method") == "PATCH" for _, _, k in calls))

    def test_retry_recovers_branch_created_before_interrupted_pr_creation(self):
        calls = self.run_reconcile(orphan=True)
        self.assertFalse(any(e.endswith(("/git/commits", "/git/refs")) for e, _, _ in calls))
        self.assertTrue(any(e.endswith("/pulls") and p for e, p, _ in calls))

    def test_existing_branch_with_unexpected_content_is_never_replaced(self):
        calls = self.run_reconcile(orphan=True, collision=True)
        self.assertFalse(any(e.endswith(("/git/refs", "/pulls")) for e, _, _ in calls))

    def test_renderer_cannot_publish_unlisted_files(self):
        calls = self.run_reconcile(extra_output=True)
        self.assertFalse(any(p is not None for _, p, _ in calls))

    def test_superseded_generated_repairs_are_retired_after_replacement(self):
        old = repair(merged.PREFIX + "d" * 40)
        calls = self.run_reconcile(old=old)
        created = next(i for i, (e, p, _) in enumerate(calls) if e.endswith("/pulls") and p)
        closed = next(i for i, (_, p, _) in enumerate(calls) if p == {"state": "closed"})
        self.assertLess(created, closed)

    def test_repairs_already_satisfied_on_main_are_retired(self):
        calls = self.run_reconcile(changed=False, old=repair())
        self.assertTrue(any(p == {"state": "closed"} for _, p, _ in calls))

    def test_retirement_does_not_close_human_or_unrelated_bot_work(self):
        for candidate in [repair(author="human"), repair(branch="deps/refresh")]:
            calls = self.run_reconcile(old=candidate)
            self.assertFalse(any(p == {"state": "closed"} for _, p, _ in calls))

    def test_retirement_preserves_mixed_authored_recipe_and_client_changes(self):
        for path in ["docs/client-recipes/src/bower.json", "test/acceptance/clients/bower.Dockerfile",
                     "test/acceptance/sbt_steps_test.go"]:
            calls = self.run_reconcile(old=repair(merged.PREFIX + "d" * 40), old_path=path)
            self.assertFalse(any(p == {"state": "closed"} for _, p, _ in calls))


if __name__ == "__main__":
    unittest.main()
