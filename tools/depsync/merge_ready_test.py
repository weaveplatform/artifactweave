import copy
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


if __name__ == "__main__":
    unittest.main()
