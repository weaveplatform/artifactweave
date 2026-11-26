"""Qualify the pinned federated provider resources against three owned instances."""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import sys
import time
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

SCENARIO = "deploy/artifactweave/terraform/federated-repositories"
NAMES = "alpine ansible bower cargo chef cocoapods composer conan conda cran debian docker docker_v2 gems generic gitlfs go gradle helm helmoci huggingfaceml ivy maven npm nuget oci opkg puppet pypi rpm sbt swift terraform_module terraform_provider vagrant".split()
RESOURCES = ["artifactory_federated_" + name + "_repository" for name in NAMES]
EXCLUDED = {
    "artifactory_federated_docker_v1_repository": "Docker Registry V1 is superseded; no V1 protocol is served",
    "artifactory_federated_releasebundles_repository": "No local releasebundles implementation before P15",
    "terraformbackend": "Excluded by 7.161.26; no federated provider resource",
}
STEPS = ["apply-1", "plan-1", "read-1", "apply-2", "plan-2", "read-2", "destroy", "deleted"]


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(url, token, method, path, data=None, basic=False):
    parsed = urlsplit(url)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port, "requires owned loopback instances"
    authorization = ("Basic " + base64.b64encode(b"admin:terraform-admin").decode()) if basic else "Bearer " + token
    headers = {"Authorization": authorization}
    if data is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(data).encode()
    req = Request(url + path, data=data, method=method, headers=headers)
    try:
        with build_opener(ProxyHandler({}), NoRedirects()).open(req, timeout=30) as response:
            return response.status, response.read(4 << 20)
    except HTTPError as error:
        return error.code, error.read(4 << 20)


def call(url, token, method, path, data=None, basic=False):
    status, body = request(url, token, method, path, data, basic)
    assert status == 200, f"{method} {path}: status {status}"
    return json.loads(body) if body else None


def members():
    values = json.loads(Path(os.environ["P13_MEMBERS_FILE"]).read_text())
    assert len(values) == 3 and len({x["url"] for x in values}) == 3
    return values


def setup(directory):
    urls = json.loads(os.environ["TERRAFORM_FIXTURE_ORIGINS"])
    assert len(urls) == 3
    entries = []
    for index, url in enumerate(urls):
        token = os.environ["TF_VAR_access_token"] if index == 0 else call(url, "", "POST", "/access/api/v1/tokens",
            {"scope": "applied-permissions/admin", "expires_in": 3600}, basic=True)["access_token"]
        entries.append({"url": url, "token": token})
    for i, issuer in enumerate(entries):
        for redeemer in entries[i + 1:]:
            issued = call(issuer["url"], issuer["token"], "POST", "/artifactory/api/v1/service_trust/pairing/federated-repo/*")
            call(redeemer["url"], redeemer["token"], "PUT", "/artifactory/api/v1/service_trust/pairing/federated-repo/*",
                 {"pairing_token": issued["pairing_token"]})
    Path(os.environ["P13_MEMBERS_FILE"]).write_text(json.dumps(entries))
    (directory / "federation.auto.tfvars.json").write_text(json.dumps({
        "member_urls": urls, "member_access_tokens": {entry["url"]: entry["token"] for entry in entries},
    }))
    print("Federated provider fixture: three owned members paired")


def key(name):
    return os.environ.get("TF_VAR_name_prefix", "fed") + "-" + name.replace("_", "-")


def record(step):
    with Path(os.environ["P13_LEDGER"]).open("a") as out:
        for resource in RESOURCES:
            out.write(json.dumps({"resource": resource, "step": step}) + "\n")


def sync_calls(revision):
    events = []
    for line in Path(os.environ["P13_SERVER_LOG"]).read_text().splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if row.get("method") == "POST" and row.get("path", "").startswith("/artifactory/api/federation/configSync/"):
            events.append(row)
    for name in NAMES:
        selected = [row for row in events if row["path"].endswith("/" + key(name))]
        assert len(selected) == revision and all(row.get("status") == 200 for row in selected), \
            f"{key(name)} configSync: expected {revision} successful provider calls"
    assert not any(row["path"].endswith("/" + key("unpaired")) for row in events), "unpaired create reached configSync"


def check(revision):
    entries = members()
    pending = {(entry["url"], name) for entry in entries for name in NAMES}
    deadline = time.monotonic() + 90
    while pending and time.monotonic() < deadline:
        for entry in entries:
            for name in NAMES:
                pair = entry["url"], name
                if pair not in pending:
                    continue
                status, body = request(entry["url"], entry["token"], "GET", "/artifactory/api/repositories/" + quote(key(name)))
                if status in (400, 404):
                    continue
                assert status == 200, f"read {key(name)}: status {status}"
                data = json.loads(body)
                expected_urls = {x["url"] + "/artifactory/" + key(name) for x in entries}
                actual = data.get("members", [])
                assert all(entry["token"].encode() not in body for entry in entries), "member token in repository JSON"
                assert all("access_token" not in member and "accessToken" not in member for member in actual), "member token in repository JSON"
                package = "docker" if name == "docker_v2" else "terraform" if name.startswith("terraform_") else name
                terraform_type = name.removeprefix("terraform_").upper() if name.startswith("terraform_") else None
                if (data.get("rclass") == "federated" and data.get("packageType") == package
                    and (terraform_type is None or data.get("terraformType") == terraform_type)
                    and len(actual) == 3 and data.get("description") == f"Three-member federation revision {revision}"
                    and {x["url"] for x in actual} == expected_urls and all(x.get("enabled") in (True, "true") for x in actual)):
                    pending.remove(pair)
        if pending:
            time.sleep(.1)
    assert not pending, f"{len(pending)} federated member readbacks did not converge"
    sync_calls(revision)
    record("read-" + str(revision))
    print(f"Federated provider revision {revision}: 35 resources read on three members; all configSync calls succeeded")


def deleted():
    entries = members()
    for entry in entries:
        remaining = call(entry["url"], entry["token"], "GET", "/artifactory/api/repositories")
        keys = {row["key"] for row in remaining}
        assert not keys.intersection({key(name) for name in NAMES}), "cleanup_on_delete left a member repository"
    record("deleted")
    print("Federated provider cleanup_on_delete: all 35 resources absent from all three members")


def unpaired(log, state):
    text = " ".join(log.read_text().split())
    expected = f"Federated member 'https://unpaired.invalid/artifactory/{key('unpaired')}' is not a paired peer: pair the instances for federated-repo/* first"
    assert expected in text and re.search(r'"status"\s*:\s*400', text), "provider did not print the unpaired-member 400"
    assert not any(line.startswith("artifactory_federated_generic_repository.unpaired") for line in state.read_text().splitlines()), "failed create wrote Terraform state"
    for entry in members():
        rows = call(entry["url"], entry["token"], "GET", "/artifactory/api/repositories")
        assert key("unpaired") not in {row["key"] for row in rows}, "failed create left a repository"
    sync_calls(2)
    Path(os.environ["P13_UNPAIRED_RESULT"]).write_text(json.dumps({"plan": "accepted", "apply_status": 400, "state_written": False, "config_sync_called": False}))
    print("Unpaired member: plan accepted, apply printed 400, no state or configSync")


def inventory(root):
    fixture = root / SCENARIO
    declared = set(re.findall(r'^resource "([^"]+)"', (fixture / "main.tf").read_text(), re.M))
    assert declared == set(RESOURCES) and len(RESOURCES) == 35
    assert SCENARIO in (root / "Makefile").read_text(), "federated scenario omitted from the required provider lane"
    assert 'deploy/artifactweave/terraform/federated-repositories) apply_federated_repositories ;;' in (root / "test/terraform/run.sh").read_text()
    assert 'version = "12.11.14"' in (fixture / "versions.tf").read_text()
    assert 'version     = "12.11.14"' in (fixture / ".terraform.lock.hcl").read_text()
    print("Federated provider inventory: 35 pinned resources and three explicit exclusions")


def complete(root, mirror, work, coverage, scenarios):
    if SCENARIO not in scenarios:
        return
    recorded = {}
    for line in Path(os.environ["P13_LEDGER"]).read_text().splitlines():
        row = json.loads(line)
        recorded.setdefault(row["resource"], []).append(row["step"])
    assert set(recorded) == set(RESOURCES)
    for resource in RESOURCES:
        assert recorded[resource] == STEPS, f"{resource}: incomplete or duplicated qualification steps"
    control = json.loads(Path(os.environ["P13_UNPAIRED_RESULT"]).read_text())
    assert control == {"plan": "accepted", "apply_status": 400, "state_written": False, "config_sync_called": False}
    for log in [work / "server.log", *work.glob("federation-peer-*/server.log")]:
        content = log.read_text()
        assert all(entry["token"] not in content for entry in members()), "member credential in runtime log"
    evidence = {
        "application_commit": os.environ["TERRAFORM_APPLICATION_COMMIT"],
        "terraform_version": os.environ["TERRAFORM_CLI_VERSION"],
        "provider_version": "12.11.14",
        "member_count": 3,
        "successful_config_sync_calls": 70,
        "created_resources": 35, "updated_resources": 35, "destroyed_resources": 35,
        "empty_refreshed_plans": 2,
        "resources": {resource: {"steps": recorded[resource], "member_readbacks": 3, "config_sync_statuses": [200, 200]} for resource in RESOURCES},
        "exclusions": EXCLUDED,
        "unpaired_member": control,
        "provider_zips": {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(mirror.rglob("*artifactory*.zip"))},
        "scenario": {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted((root / SCENARIO).iterdir())
                     if path.suffix == ".tf" or path.name == ".terraform.lock.hcl"},
        "inputs": {name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in [
            "test/terraform/run.sh", "test/terraform/check-p13.py", "test/terraform/federated-driver.sh", "test/terraform/federated-unpaired.tf.in", "test/terraform/fixture-server.py"]},
    }
    serialized = json.dumps(evidence, indent=2, sort_keys=True) + "\n"
    for value in [entry["token"] for entry in members()] + ["terraform-admin"]:
        assert value not in serialized, "credential in public provider evidence"
    coverage.mkdir(parents=True, exist_ok=True)
    (coverage / "p13-provider-evidence.json").write_text(serialized)
    print("Federated provider qualification: all 35 resources, three members and unpaired control PASS")


if __name__ == "__main__":
    args = sys.argv[1:]
    if args[0] == "--ports":
        held = []
        for _ in range(2):
            sock = socket.socket(); sock.bind(("127.0.0.1", 0)); held.append(sock)
        ports = [sock.getsockname()[1] for sock in held]
        assert len(set(ports + [int(args[1])])) == 3
        print(*ports)
    elif args[0] == "--origins":
        print(json.dumps(["http://127.0.0.1:" + port for port in args[1:]]))
    elif args[0] == "--setup": setup(Path(args[1]))
    elif args[0] == "--step": record(args[1])
    elif args[0] == "--check": check(int(args[1]))
    elif args[0] == "--deleted": deleted()
    elif args[0] == "--unpaired": unpaired(Path(args[1]), Path(args[2]))
    elif args[0] == "--inventory": inventory(Path(args[1]))
    elif args[0] == "--complete": complete(*(Path(x) for x in args[1:5]), args[5:])
    else: raise SystemExit("unknown federation qualification command")
