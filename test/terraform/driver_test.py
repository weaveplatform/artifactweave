"""Exercise the real shell driver with owned children and loopback spies only."""

from contextlib import contextmanager
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCENARIO = "deploy/artifactweave/terraform/oidc"
FIXTURE = None
CURL = r'''#!/usr/bin/env python3
import os, pathlib, signal, socket, subprocess, sys, time
from urllib.parse import urlsplit
work = pathlib.Path(os.environ["DRIVER_TEST_ROOT"])
held = None
if os.environ["DRIVER_TEST_BEHAVIOR"] == "draining" and not (work / "listener-closed").exists():
    url = urlsplit(sys.argv[-1])
    held = socket.create_connection((url.hostname, url.port), timeout=2)
    held.sendall(b"GET /held HTTP/1.1\r\nHost: fixture\r\n\r\n")
    deadline = time.monotonic() + 3
    while not (work / "held-request").exists() and time.monotonic() < deadline: time.sleep(.01)
    assert (work / "held-request").exists()
    os.kill(int((work / "child-pid").read_text()), signal.SIGTERM)
    while not (work / "replacement-ready").exists() and time.monotonic() < deadline: time.sleep(.01)
    assert (work / "replacement-ready").exists()
result = subprocess.run([os.environ["DRIVER_TEST_REAL_CURL"], *sys.argv[1:]])
if held: held.close()
sys.exit(result.returncode)
'''
GO = r'''#!/usr/bin/env python3
import os, pathlib, shutil, sys
if sys.argv[1] == "env": print("darwin\narm64")
elif sys.argv[1] == "list": print("github.com/weaveplatform/artifactweave")
elif sys.argv[1] == "build":
    shutil.copyfile(os.environ["DRIVER_TEST_APP"], sys.argv[sys.argv.index("-o") + 1])
    pathlib.Path(sys.argv[sys.argv.index("-o") + 1]).chmod(0o700)
else: sys.exit("unexpected go invocation")
'''
TERRAFORM = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
if sys.argv[1] == "version":
    print('{"terraform_version":"1.17.1"}' if "-json" in sys.argv else "Terraform v1.17.1")
else:
    if os.environ["DRIVER_TEST_BEHAVIOR"] == "draining-provider" and sys.argv[2] == "apply":
        import signal, socket, time, urllib.request
        from urllib.parse import urlsplit
        work = pathlib.Path(os.environ["DRIVER_TEST_ROOT"])
        url = urlsplit(os.environ["TF_VAR_url"])
        held = socket.create_connection((url.hostname, url.port), timeout=2)
        held.sendall(b"GET /held HTTP/1.1\r\nHost: fixture\r\n\r\n")
        deadline = time.monotonic() + 3
        while not (work / "held-request").exists() and time.monotonic() < deadline: time.sleep(.01)
        assert (work / "held-request").exists()
        os.kill(int((work / "child-pid").read_text()), signal.SIGTERM)
        while not (work / "replacement-ready").exists() and time.monotonic() < deadline: time.sleep(.01)
        assert (work / "replacement-ready").exists()
        request = urllib.request.Request(os.environ["TF_VAR_url"] + "/provider-fixture", data=b"fixture",
                                         headers={"Authorization": "Bearer owned-provider-fixture-token"})
        try: urllib.request.urlopen(request, timeout=2).read()
        except OSError: sys.exit(1)
    if os.environ.get("DRIVER_TEST_PROBE_CLIENT") and sys.argv[2] in ("apply", "plan", "destroy"):
        import urllib.request
        urllib.request.urlopen(os.environ["TF_VAR_url"] + "/artifactory/api/v1/system/readiness", timeout=2).read()
    with (pathlib.Path(os.environ["DRIVER_TEST_ROOT"]) / "terraform.jsonl").open("a") as out:
        out.write(json.dumps(sys.argv[2:]) + "\n")
'''


class Requests:
    def __init__(self, path):
        self.path = path

    def read(self):
        return [json.loads(line) for line in self.path.read_text().splitlines()] if self.path.exists() else []

    def __iter__(self): return iter(self.read())
    def __eq__(self, other): return self.read() == other
    def __repr__(self): return repr(self.read())


@contextmanager
def owned_spy(work, port=0, replacement=False):
    record = Path(tempfile.mkdtemp(prefix="spy-", dir=work))
    args = [str(FIXTURE), "spy", str(record), str(work), str(port)]
    if replacement: args.append("replace")
    with (record / "process.log").open("w") as log:
        process = subprocess.Popen(args, stdout=log, stderr=subprocess.STDOUT)
    try:
        if not replacement:
            deadline = time.monotonic() + 3
            while not (record / "ready").exists() and time.monotonic() < deadline:
                assert process.poll() is None, "owned chi spy failed to bind"
                time.sleep(.01)
            port = int((record / "ready").read_text())
        yield port, Requests(record / "requests.jsonl")
    finally:
        if process.poll() is None: process.terminate()
        process.wait(timeout=3)


@contextmanager
def spy(work):
    with owned_spy(work) as value:
        yield value


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


@contextmanager
def replacement_spy(work, port):
    with owned_spy(work, port, replacement=True) as (_, requests):
        yield requests


class DriverTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        global FIXTURE
        if os.environ.get("DRIVER_TEST_FIXTURE_BIN"):
            FIXTURE = Path(os.environ["DRIVER_TEST_FIXTURE_BIN"])
            assert FIXTURE.is_file(), "prebuilt chi fixture missing"
            return
        cls.fixture_work = tempfile.TemporaryDirectory(prefix="provider-driver-chi-")
        cls.addClassCleanup(cls.fixture_work.cleanup)
        FIXTURE = Path(cls.fixture_work.name) / "server"
        env = dict(os.environ, GOWORK="off")
        result = subprocess.run(["go", "build", "-o", str(FIXTURE), "./test/terraform/testdata/server"],
                                cwd=ROOT, env=env, capture_output=True, text=True, timeout=90)
        if result.returncode:
            raise AssertionError("chi fixture build failed: " + result.stderr)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="provider-driver-test-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        for name, source in (("go", GO), ("terraform", TERRAFORM), ("curl", CURL)):
            path = self.bin / name
            path.write_text(source)
            path.chmod(0o700)
        (self.bin / "git").write_text('#!/usr/bin/env python3\nprint("owned-driver-regression")\n')
        (self.bin / "git").chmod(0o700)
        shutil.copyfile(FIXTURE, self.bin / "app")
        (self.bin / "app").chmod(0o700)
        self.retained = self.work / "retained"
        self.retained.mkdir()
        (self.retained / "existing-evidence").write_text("keep")

    def run_driver(self, behavior, port, extra=None, mode="apply"):
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                   DRIVER_TEST_ROOT=str(self.work), DRIVER_TEST_APP=str(self.bin / "app"),
                   DRIVER_TEST_BEHAVIOR=behavior, TERRAFORM_WORK=str(self.retained),
                   DRIVER_TEST_REAL_CURL=shutil.which("curl"),
                   TERRAFORM_PORT=str(port), TERRAFORM_PROVIDER_MIRROR=str(self.work / "mirror"),
                   GOCOVERDIR=str(self.work / "coverage"), NO_PROXY="", no_proxy="")
        env.pop("TERRAFORM_ACCESS_TOKEN", None)
        if extra:
            env.update(extra)
        result = subprocess.run(["bash", str(ROOT / "test/terraform/run.sh"), mode, SCENARIO],
                                env=env, text=True, capture_output=True, timeout=25)
        self.assertEqual((self.retained / "existing-evidence").read_text(), "keep")
        marker = self.work / "child-pid"
        if marker.exists():
            self.assertFalse(alive(int(marker.read_text())), "owned child was not reaped")
        for marker in self.retained.glob("run.*/forwarder.ready"):
            self.assertFalse(alive(json.loads(marker.read_text())["pid"]), "owned forwarder was not reaped")
        return result

    def assert_no_effects(self, result, requests):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(any(r["method"] == "POST" for r in requests), requests)
        calls = [json.loads(line)[0] for line in (self.work / "terraform.jsonl").read_text().splitlines()]
        self.assertFalse(set(calls) & {"apply", "plan", "destroy"}, calls)

    def test_failed_child_does_not_authenticate_to_healthy_listener(self):
        with spy(self.work) as (port, requests):
            self.assert_no_effects(self.run_driver("exit", port), requests)
            self.assertEqual(requests, [])

    def test_draining_child_does_not_authorize_replacement_listener(self):
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        with replacement_spy(self.work, port) as requests:
            result = self.run_driver("draining", port)
            self.assertTrue((self.work / "held-request").exists())
            self.assertTrue((self.work / "replacement-ready").exists())
            if os.environ.get("DRIVER_TEST_EVIDENCE"):
                Path(os.environ["DRIVER_TEST_EVIDENCE"]).write_text(json.dumps({
                    "baseline": "8d33fc0bc57cc3f0b1ddb7529f1051c5b65be308",
                    "parent_exit": result.returncode, "requests": list(requests),
                    "held_owned_request": True, "owned_replacement_bound": True,
                    "child_live_when_replacement_received_auth": (self.work / "replacement-auth-while-child-live").exists(),
                }, indent=2) + "\n")
            self.assert_no_effects(result, requests)

    def test_provider_reconnect_does_not_reach_draining_child_replacement(self):
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        with replacement_spy(self.work, port) as requests:
            result = self.run_driver("draining-provider", port)
            self.assertTrue((self.work / "held-request").exists())
            self.assertTrue((self.work / "replacement-ready").exists())
            self.assert_no_effects(result, requests)
            self.assertEqual(requests, [])

    def test_connected_socket_requires_child_ownership_evidence(self):
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            front = reservation.getsockname()[1]
        with spy(self.work) as (foreign, requests):
            result = self.run_driver("foreign-event", front, {"DRIVER_TEST_FOREIGN_PORT": str(foreign)})
            self.assert_no_effects(result, requests)
            self.assertEqual(requests, [])

    def test_occupied_port_does_not_authenticate_or_stop_occupant(self):
        with spy(self.work) as (port, requests):
            self.assert_no_effects(self.run_driver("occupied", port), requests)
            self.assertEqual(requests, [])

    def test_live_child_without_listener_evidence_fails_closed(self):
        with spy(self.work) as (port, requests):
            (self.retained / "server.log").write_text(json.dumps({
                "msg": "artifactweave listening", "addr": f"127.0.0.1:{port}"}) + "\n")
            self.assert_no_effects(self.run_driver("no-event", port), requests)
            self.assertEqual(requests, [])
            self.assertTrue((self.retained / "server.log").exists())

    def test_listener_event_must_name_a_private_loopback_address(self):
        with spy(self.work) as (port, requests):
            self.assert_no_effects(self.run_driver("wrong-event", port), requests)
            self.assertEqual(requests, [])

    def test_child_exit_during_readiness_fails_before_authentication(self):
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        self.assert_no_effects(self.run_driver("exit-on-readiness", port), [])
        self.assertTrue((self.work / "readiness-started").exists())

    def test_owned_listener_readiness_timeout_fails_before_authentication(self):
        with spy(self.work) as (port, _):
            pass  # Release only this test's port before its child binds it.
        result = self.run_driver("timeout", port)
        requests = [json.loads(line) for line in (self.work / "requests.jsonl").read_text().splitlines()]
        self.assert_no_effects(result, requests)

    def test_success_keeps_configuration_private_and_retains_caller_evidence(self):
        with spy(self.work) as (port, _):
            pass
        poison = self.work / "ambient"
        poison.mkdir()
        (poison / "artifactweave.yaml").write_text("unknown: must-not-be-loaded")
        extra = {"ARTIFACTWEAVE_DATABASE_DRIVER": "postgres",
                 "ARTIFACTWEAVE_DATABASE_DSN": "postgres://invalid/never-connect",
                 "ARTIFACTWEAVE_BLOB_BACKEND": "s3", "ARTIFACTWEAVE_BLOB_FS_ROOT": str(poison),
                 "ARTIFACTWEAVE_COLD_BLOB_BACKEND": "fs",
                 "ARTIFACTWEAVE_COLD_BLOB_FS_ROOT": str(poison),
                 "ARTIFACTWEAVE_BOOTSTRAP_DESCRIPTOR": str(poison / "descriptor.yaml"),
                 "ARTIFACTWEAVE_LOG_LEVEL": "error", "ARTIFACTWEAVE_CONFIG": str(poison / "artifactweave.yaml")}
        result = self.run_driver("success", port, extra)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        child_env = json.loads((self.work / "child-env.json").read_text())
        self.assertFalse(any(k.startswith("ARTIFACTWEAVE_") for k in child_env), child_env.keys())
        self.assertEqual(child_env.get("HOME"), os.environ.get("HOME"))
        self.assertEqual(child_env["GOCOVERDIR"], str(self.work / "coverage"))
        cfg = json.loads((self.work / "child-config.json").read_text())
        data = Path(cfg["data_dir"])
        self.assertTrue(data.is_relative_to(self.retained.resolve()))
        self.assertEqual(cfg["database"], {"driver": "sqlite", "dsn": str(data / "artifactweave.db")})
        self.assertEqual(cfg["blob"], {"backend": "fs", "fs": {"root": str(data / "blobs")}})
        self.assertEqual(cfg["cold_blob"], {"backend": ""})
        calls = [json.loads(line)[0] for line in (self.work / "terraform.jsonl").read_text().splitlines()]
        self.assertEqual(calls, ["init", "validate", "apply", "plan", "destroy"])
        self.assertEqual(list(poison.iterdir()), [poison / "artifactweave.yaml"])

    def test_caller_token_and_automatic_workspace_cleanup_are_preserved(self):
        with spy(self.work) as (port, _):
            pass
        result = self.run_driver("success", port,
                                 {"TERRAFORM_ACCESS_TOKEN": "owned-caller-fixture-token", "TERRAFORM_WORK": ""})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        requests = [json.loads(line) for line in (self.work / "requests.jsonl").read_text().splitlines()]
        self.assertFalse(any(r["method"] == "POST" for r in requests), requests)
        run = Path(next(line.split(": ", 1)[1] for line in result.stdout.splitlines()
                        if line.startswith("Terraform provider workspace: ")))
        self.assertFalse(run.exists(), "successful automatic workspace was retained")

    def test_all_fixture_clients_bypass_an_ambient_proxy(self):
        with spy(self.work) as (port, _):
            pass
        with spy(self.work) as (proxy_port, requests):
            proxy = f"http://127.0.0.1:{proxy_port}"
            result = self.run_driver("success", port,
                                     {"HTTP_PROXY": proxy, "http_proxy": proxy,
                                      "DRIVER_TEST_PROBE_CLIENT": "1"})
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(requests, [])

    def test_automatic_failure_evidence_is_retained(self):
        with spy(self.work) as (port, requests):
            result = self.run_driver("exit", port, {"TERRAFORM_WORK": ""})
            self.assert_no_effects(result, requests)
        run = Path(next(line.split(": ", 1)[1] for line in result.stdout.splitlines()
                        if line.startswith("Terraform provider workspace: ")))
        self.addCleanup(shutil.rmtree, run)
        self.assertTrue((run / "server.log").exists())
        self.assertIn("retained private failure evidence", result.stderr)

    def test_validate_does_not_start_a_child(self):
        result = self.run_driver("exit", 1, mode="validate")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse((self.work / "child-pid").exists())


if __name__ == "__main__":
    unittest.main()
