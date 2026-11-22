"""Exercise the real shell driver with owned children and loopback spies only."""

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCENARIO = "deploy/artifactweave/terraform/oidc"
APP = r'''#!/usr/bin/env python3
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json, os, pathlib, signal, sys, time
args = sys.argv
work = pathlib.Path(os.environ["DRIVER_TEST_ROOT"])
(work / "child-pid").write_text(str(os.getpid()))
behavior = os.environ["DRIVER_TEST_BEHAVIOR"]
if behavior == "exit":
    sys.exit(1)
address = args[args.index("--listen") + 1]
port = int(address.rsplit(":", 1)[1])
if "--config" in args:
    cfg = json.loads(pathlib.Path(args[args.index("--config") + 1]).read_text())
    (work / "child-config.json").write_text(json.dumps(cfg))
(work / "child-env.json").write_text(json.dumps({k: v for k, v in os.environ.items()
    if k.startswith("ARTIFACTWEAVE_") or k in ("HOME", "GOCOVERDIR")}))
if behavior in ("no-event", "wrong-event", "exit-on-readiness"):
    if behavior != "no-event":
        print(json.dumps({"msg": "artifactweave listening", "addr": address
            if behavior == "exit-on-readiness" else "127.0.0.1:1"}), flush=True)
    if behavior == "exit-on-readiness":
        while not (work / "readiness-started").exists(): time.sleep(.01)
        sys.exit(1)
    while True: time.sleep(1)
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def reply(self, data, status=200):
        with (work / "requests.jsonl").open("a") as out:
            out.write(json.dumps({"method": self.command, "path": self.path}) + "\n")
        self.send_response(status)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def do_GET(self): self.reply(b"ready", 503 if behavior == "timeout" else 200)
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.reply(b'{"access_token":"owned-test-token"}')
server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
print(json.dumps({"msg": "artifactweave listening", "addr": address}), flush=True)
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
server.serve_forever()
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
    if os.environ.get("DRIVER_TEST_PROBE_CLIENT") and sys.argv[2] in ("apply", "plan", "destroy"):
        import urllib.request
        urllib.request.urlopen(os.environ["TF_VAR_url"] + "/artifactory/api/v1/system/readiness", timeout=2).read()
    with (pathlib.Path(os.environ["DRIVER_TEST_ROOT"]) / "terraform.jsonl").open("a") as out:
        out.write(json.dumps(sys.argv[2:]) + "\n")
'''


@contextmanager
def spy(work):
    requests = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            requests.append({"method": "GET", "path": self.path})
            (work / "readiness-started").touch()
            if (work / "child-pid").exists():
                pid = int((work / "child-pid").read_text())
                deadline = time.monotonic() + 2
                while alive(pid) and time.monotonic() < deadline:
                    time.sleep(.01)
            self.reply(b"ready")

        def do_POST(self):
            requests.append({"method": "POST", "path": self.path})
            self.rfile.read(int(self.headers.get("Content-Length", "0")))
            self.reply(b'{"access_token":"owned-test-token"}')

        def reply(self, data):
            self.send_response(200)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server.server_port, requests
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


class DriverTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="provider-driver-test-")
        self.addCleanup(self.temp.cleanup)
        self.work = Path(self.temp.name)
        self.bin = self.work / "bin"
        self.bin.mkdir()
        for name, source in (("go", GO), ("terraform", TERRAFORM), ("app", APP)):
            path = self.bin / name
            path.write_text(source)
            path.chmod(0o700)
        self.retained = self.work / "retained"
        self.retained.mkdir()
        (self.retained / "existing-evidence").write_text("keep")

    def run_driver(self, behavior, port, extra=None, mode="apply"):
        env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                   DRIVER_TEST_ROOT=str(self.work), DRIVER_TEST_APP=str(self.bin / "app"),
                   DRIVER_TEST_BEHAVIOR=behavior, TERRAFORM_WORK=str(self.retained),
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

    def test_listener_event_must_name_the_requested_address(self):
        with spy(self.work) as (port, requests):
            self.assert_no_effects(self.run_driver("wrong-event", port), requests)
            self.assertEqual(requests, [])

    def test_child_exit_during_readiness_fails_before_authentication(self):
        with spy(self.work) as (port, requests):
            self.assert_no_effects(self.run_driver("exit-on-readiness", port), requests)

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
