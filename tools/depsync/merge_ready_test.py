import copy
import subprocess
import unittest
from unittest.mock import patch

import merge_ready
from merged_test import REPO, repair


class ReadyRepairTests(unittest.TestCase):
    def test_only_approved_current_head_with_completed_quality_gate_can_merge(self):
        pull = repair()
        status = {"headRefOid": pull["head"]["sha"], "reviewDecision": "APPROVED",
                  "statusCheckRollup": [{"name": "Quality gate", "status": "COMPLETED", "conclusion": "SUCCESS"}]}
        self.assertTrue(merge_ready.ready(pull, status, REPO, "main"))
        for field, value in [("headRefOid", "new-head"), ("reviewDecision", "REVIEW_REQUIRED"),
                             ("statusCheckRollup", [])]:
            changed = dict(status); changed[field] = value
            self.assertFalse(merge_ready.ready(pull, changed, REPO, "main"))
        for field, value in [("name", "Lint"), ("status", "IN_PROGRESS"), ("conclusion", "FAILURE")]:
            changed = copy.deepcopy(status); changed["statusCheckRollup"][0][field] = value
            self.assertFalse(merge_ready.ready(pull, changed, REPO, "main"))
        self.assertFalse(merge_ready.ready(repair(author="human"), status, REPO, "main"))
        self.assertFalse(merge_ready.ready(repair(branch="deps/refresh"), status, REPO, "main"))

    def test_merge_keeps_head_guard_and_native_client_manual_policy(self):
        for path in ["docs/client-recipes/evidence-lock.json", "test/acceptance/clients/bower.Dockerfile"]:
            def api(endpoint, **kwargs):
                return [[{"filename": path, "status": "modified"}]] if endpoint.endswith("/files") else [[repair()]]
            with patch.object(merge_ready, "api", side_effect=api), \
                    patch.object(merge_ready.subprocess, "check_output", return_value="{}"), \
                    patch.object(merge_ready, "ready", return_value=True), \
                    patch.object(merge_ready.subprocess, "run") as publish:
                merge_ready.merge_ready(REPO, "main")
                if path.startswith("test/"):
                    publish.assert_not_called()
                else:
                    args = publish.call_args.args[0]
                    self.assertIn("--match-head-commit", args)
                    self.assertEqual(repair()["head"]["sha"], args[-1])
                    self.assertNotIn("--admin", args)

    def test_one_policy_block_does_not_prevent_other_ready_repairs(self):
        first, second = repair(), repair()
        second["number"] = 43
        def api(endpoint, **kwargs):
            if endpoint.endswith("/files"):
                return [[{"filename": "docs/client-recipes/evidence-lock.json", "status": "modified"}]]
            return [[first, second]]
        with patch.object(merge_ready, "api", side_effect=api), \
                patch.object(merge_ready.subprocess, "check_output", return_value="{}"), \
                patch.object(merge_ready, "ready", return_value=True), \
                patch.object(merge_ready.subprocess, "run", side_effect=[subprocess.CalledProcessError(1, ["gh"]), None]) as publish:
            with self.assertRaisesRegex(RuntimeError, "42"):
                merge_ready.merge_ready(REPO, "main")
            self.assertEqual(2, publish.call_count)


if __name__ == "__main__":
    unittest.main()
