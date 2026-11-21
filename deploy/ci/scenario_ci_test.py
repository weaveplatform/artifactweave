#!/usr/bin/env python3
"""Exercise the real Azure cleanup verifier without making cloud calls."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
DRIVER = ROOT / "deploy/ci/scenario-ci.sh"
FAKE_AZURE = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

mode = os.environ["FIXTURE_AZURE_MODE"]
root = Path(os.environ["FIXTURE_AZURE_DIR"])
tag = os.environ["RUN_TAG"]
command = sys.argv[1:]
if command[:3] == ["storage", "blob", "list"]:
    if mode == "state read failure":
        sys.exit(21)
    if mode == "persistent state":
        print("scenarios/azure/cdn/" + tag + "-t")
elif command[:2] == ["resource", "list"]:
    counter = root / "inventory-scans"
    count = int(counter.read_text()) + 1 if counter.exists() else 1
    counter.write_text(str(count))
    if mode == "inventory read failure":
        sys.exit(22)
    if mode == "invalid inventory":
        print("invalid json")
    else:
        kind = "Microsoft.Cdn/profiles"
        instance = tag + "-t"
        if mode == "persistent storage":
            kind = "Microsoft.Storage/storageAccounts"
        if mode == "foreign profile":
            instance = tag + "suffix-t"
        present = mode in {"persistent profile", "persistent storage", "foreign profile"}
        present = present or (mode == "delayed inventory" and count < 3)
        print(json.dumps([{
            "id": "/subscriptions/fixture/resourceGroups/fixture/providers/" + kind + "/fixture",
            "type": kind,
            "name": "fixture",
            "tags": {"instance": instance, "artifactweave-scenario": "cdn"},
        }] if present else []))
elif command[:4] == ["network", "dns", "zone", "list"]:
    if mode == "DNS zone read failure":
        sys.exit(23)
    if mode in {"persistent DNS", "DNS without expiry", "DNS empty expiry", "DNS records read failure", "invalid DNS records"}:
        print("fixture-group\tfixture.test")
elif command[:4] == ["network", "dns", "record-set", "list"]:
    if mode == "DNS records read failure":
        sys.exit(24)
    if mode == "invalid DNS records":
        print("invalid json")
        sys.exit(0)
    metadata = {"instance": tag + "-t", "artifactweave-scenario": "cdn", "expires-at": "1"}
    if mode == "DNS without expiry":
        del metadata["expires-at"]
    if mode == "DNS empty expiry":
        metadata["expires-at"] = ""
    print(json.dumps([{
        "type": "Microsoft.Network/dnsZones/TXT",
        "name": "_dnsauth.fixture",
        "metadata": metadata,
    }]))
else:
    sys.stderr.write("unexpected fixture command\n")
    sys.exit(99)
'''
FAKE_SLEEP = '''#!/usr/bin/env python3
import os
from pathlib import Path
p = Path(os.environ["FIXTURE_AZURE_DIR"]) / "waits"
with p.open("a") as output:
    output.write("wait" + chr(10))
'''


class AzureCleanupVerifierTest(unittest.TestCase):
    def test_cloud_verification(self):
        cases = [
            ("clean", True),
            ("delayed inventory", True),
            ("foreign profile", True),
            ("persistent profile", False),
            ("persistent storage", False),
            ("persistent state", False),
            ("persistent DNS", False),
            ("DNS without expiry", False),
            ("DNS empty expiry", False),
            ("state read failure", False),
            ("inventory read failure", False),
            ("invalid inventory", False),
            ("DNS zone read failure", False),
            ("DNS records read failure", False),
            ("invalid DNS records", False),
        ]
        for mode, want_clean in cases:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                fixture = Path(temporary)
                binary = fixture / "bin"
                binary.mkdir()
                for name, source in {"az": FAKE_AZURE, "sleep": FAKE_SLEEP}.items():
                    path = binary / name
                    path.write_text(source)
                    path.chmod(0o700)
                env = os.environ | {
                    "PATH": str(binary) + os.pathsep + os.environ["PATH"],
                    "CLOUD": "azure",
                    "SCENARIO": "cdn",
                    "RUN_TAG": "pr1803-fixture-c",
                    "PLATFORM_AZURE_STATE_ACCOUNT": "fixture",
                    "PLATFORM_AZURE_RESOURCE_GROUP": "fixture",
                    "FIXTURE_AZURE_MODE": mode,
                    "FIXTURE_AZURE_DIR": str(fixture),
                    "GITHUB_STEP_SUMMARY": str(fixture / "summary"),
                }
                result = subprocess.run(
                    ["bash", str(DRIVER), "check-clean"],
                    cwd=ROOT,
                    env=env,
                    capture_output=True,
                    text=True,
                    timeout=20,
                    check=False,
                )
                self.assertEqual(
                    result.returncode == 0,
                    want_clean,
                    result.stdout + result.stderr,
                )
                self.assertNotEqual(result.returncode, 99, result.stderr)
                counter = fixture / "inventory-scans"
                scans = int(counter.read_text()) if counter.exists() else 0
                self.assertLessEqual(scans, 13, "cleanup verification must have a fixed retry bound")
                waits = fixture / "waits"
                wait_count = len(waits.read_text().splitlines()) if waits.exists() else 0
                self.assertLessEqual(wait_count, 12, "cleanup verification must have a fixed wait bound")
                if mode == "delayed inventory":
                    self.assertEqual(scans, 3, "success requires observing the empty inventory")
                    self.assertEqual(wait_count, 2)
                if mode in {"clean", "foreign profile"}:
                    self.assertEqual(wait_count, 0, "an empty owned inventory needs no retry")
                if mode == "persistent profile":
                    self.assertIn("billed by the hour", result.stdout)
                if "failure" in mode or "invalid" in mode:
                    self.assertEqual(wait_count, 0, "read errors must fail immediately")


if __name__ == "__main__":
    unittest.main()
