"""Qualify pinned-provider policies against real cleanup and native Maven bytes."""

import csv
import hashlib
import io
import json
import os
from pathlib import Path
import re
import sys
import time
from urllib.error import HTTPError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen
import zipfile

SCENARIO = "deploy/artifactweave/terraform/package-cleanup"
POLICIES = "/artifactory/api/cleanup/packages/policies/"
RUNS = "/artifactory/api/cleanup/policies/runs/"


def inventory(root):
    runner = (root / "test/terraform/run.sh").read_text()
    assert SCENARIO in (root / "Makefile").read_text(), "cleanup scenario omitted from required provider runs"
    assert SCENARIO + ") apply_package_cleanup ;;" in runner, "cleanup apply handler omitted"
    for mode in ("--prepare", "--check", "--deleted"):
        assert '"$here/check-cleanup.py" ' + mode in runner, "cleanup runtime assertion omitted: " + mode
    main = (root / SCENARIO / "main.tf").read_text()
    assert 'resource "artifactory_package_cleanup_policy"' in main, "pinned policy resource omitted"
    assert 'resource "artifactory_local_maven_repository"' in main, "native Maven fixture omitted"
    versions = (root / SCENARIO / "versions.tf").read_text()
    assert re.search(r'required_version\s*=\s*">=\s*1\.17"', versions), "Terraform minimum must be >=1.17"
    lock = (root / SCENARIO / ".terraform.lock.hcl").read_text()
    assert 'provider "registry.terraform.io/jfrog/artifactory"' in lock and 'version     = "12.11.14"' in lock, "cleanup provider lock changed"
    assert lock.count('"h1:') == 4, "cleanup provider platform hashes omitted"
    print("Required pinned cleanup provider scenario and runtime assertions PASS")


def request(path, method="GET", body=None, want=200, raw=False):
    token = os.environ["TF_VAR_access_token"]
    data = body if isinstance(body, bytes) else None if body is None else json.dumps(body).encode()
    headers = {"Authorization": "Bearer " + token}
    if data is not None:
        headers["Content-Type"] = "application/octet-stream" if isinstance(body, bytes) else "application/json"
    req = Request(os.environ["TF_VAR_url"] + path, method=method, data=data, headers=headers)
    try:
        with urlopen(req, timeout=15) as response:
            status, media, payload = response.status, response.headers.get_content_type(), response.read((16 << 20) + 1)
    except HTTPError as error:
        status, media, payload = error.code, error.headers.get_content_type(), error.read((16 << 20) + 1)
    assert len(payload) <= 16 << 20, "fixture API response exceeded bound"
    assert token.encode() not in payload, "cleanup response disclosed caller credential"
    assert status == want, f"{method} {path}: status {status}, expected {want}"
    if raw:
        return media, payload
    if not payload:
        return None
    assert media == "application/json", "cleanup metadata response has wrong media type"
    return json.loads(payload)


def native_files(version):
    base = "com/acme/provider/" + version + "/provider-" + version
    pom = ("<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId>"
           "<artifactId>provider</artifactId><version>" + version + "</version></project>").encode()
    jar = io.BytesIO()
    with zipfile.ZipFile(jar, "w") as archive:
        archive.writestr(zipfile.ZipInfo("fixture.txt", (1980, 1, 1, 0, 0, 0)), "cleanup provider " + version)
    return {base + ".pom": pom, base + ".jar": jar.getvalue()}


def item_path(fixture, path):
    return "/artifactory/" + quote(fixture["repository_key"], safe="") + "/" + quote(path, safe="/")


def check_config(fixture):
    revision, key = fixture["revision"], fixture["policy_key"]
    value = request(POLICIES + quote(key, safe=""))
    expected = {"key": key, "enabled": revision == 2, "description": "Pinned provider cleanup revision " + str(revision),
                "durationInMinutes": revision, "skipTrashcan": False,
                "cronExp": "0 0 " + ("2" if revision == 1 else "3") + " 1 1 ? 2099"}
    for name, want in expected.items():
        assert value.get(name) == want, "provider policy field mismatch: " + name
    criteria = value["searchCriteria"]
    for name, want in {"packageTypes": ["maven"], "repos": [fixture["repository_key"]], "includedPackages": ["**"],
                       "includedProjects": ["default"], "includeAllProjects": False, "keepLastNVersions": 1}.items():
        assert criteria.get(name) == want, "provider criteria mismatch: " + name


def run_policy(fixture, dry):
    key = quote(fixture["policy_key"], safe="")
    admitted = request(POLICIES + key + "/run?dryRun=" + str(dry).lower(), "PUT", want=202)
    trigger = admitted.get("triggerId")
    assert type(trigger) is int and trigger > 0, "run admission omitted stable integer trigger"
    deadline = time.monotonic() + 30
    while True:
        run = request(RUNS + key + "/" + str(trigger))
        if run.get("status") in ("COMPLETED", "COMPLETED_WITH_ERRORS", "STOPPED", "FAILED"):
            break
        assert run.get("status") in ("IN_PROGRESS", "IN_PROGRESS_WITH_ERRORS", "STOPPING"), "unsupported/fabricated run status"
        assert time.monotonic() < deadline, "cleanup worker did not settle the admitted run"
        time.sleep(0.1)
    assert run["status"] == "COMPLETED", "fixture cleanup did not complete successfully"
    assert run["runId"] == trigger and run["dryRun"] == dry and run["totalItems"] == 2, "exact run identity or selection count changed"
    assert run["cleanedItems"] == (0 if dry else 2), "dry and real deletion counts were confused"
    return trigger


def check_report(fixture, trigger, dry):
    selected = native_files("1.0")
    size = sum(len(value) for value in selected.values())
    content_size = sum(len(value) for value in {hashlib.sha256(value).hexdigest(): value for value in selected.values()}.values())
    query = urlencode({"policyKey": fixture["policy_key"], "triggerId": trigger})
    summary = request(RUNS + "summaryReports?" + query)
    assert summary["policySummary"]["runId"] == trigger and summary["policySummary"]["dryRun"] == dry, "summary identity changed"
    totals = summary["processedItemsSummary"]
    expected = {"totalItems": 2, "processedItems": 2, "failedItems": 0,
                "processedVirtualSizeBytes": size, "processedPhysicalSizeBytes": content_size}
    for name, want in expected.items():
        assert totals.get(name) == want, "summary disagrees with retained outcomes: " + name
    media, payload = request(RUNS + "downloadReport?" + urlencode({"policyKey": fixture["policy_key"], "runId": trigger}), raw=True)
    assert media == "application/octet-stream", "CSV download changed authored media type"
    reader = csv.DictReader(io.StringIO(payload.decode()))
    assert reader.fieldnames == ["repository", "path", "size", "sha256", "outcome", "reason"], "CSV layout changed"
    rows = list(reader)
    assert len(rows) == 2 and {row["path"] for row in rows} == set(selected), "CSV lost/duplicated native item outcomes"
    for row in rows:
        body = selected[row["path"]]
        assert row["repository"] == fixture["repository_key"] and int(row["size"]) == len(body), "CSV path/size changed"
        assert row["sha256"] == hashlib.sha256(body).hexdigest(), "CSV checksum differs from actual native bytes"
        assert row["outcome"] == ("selected" if dry else "deleted"), "CSV invented selected/deleted effects"
    return hashlib.sha256(payload).hexdigest()


def check_log(fixture, log, deleted=False):
    rows = [json.loads(line) for line in log.read_bytes().splitlines()]
    path = POLICIES + fixture["policy_key"]
    def calls(method, endpoint, status):
        return sum(row.get("method") == method and row.get("path") == endpoint and row.get("status") == status for row in rows)
    assert calls("POST", path, 201) == 1, "provider did not create the policy once"
    enablements = calls("POST", path + "/enablement", 200)
    assert enablements == (0 if fixture["revision"] == 1 else 1), "provider bypassed/repeated separate enablement"
    if fixture["revision"] == 2:
        assert calls("PUT", path, 200) == 1, "provider did not update policy configuration once"
    if deleted:
        assert calls("DELETE", path, 200) == 1, "provider deletion did not receive required200"


def prepare(fixture, state, log):
    assert fixture["revision"] == 1
    check_config(fixture)
    for version in ("1.0", "2.0"):
        for path, body in native_files(version).items():
            request(item_path(fixture, path), "PUT", body, want=201, raw=True)
    trigger = run_policy(fixture, True)
    digest = check_report(fixture, trigger, True)
    for version in ("1.0", "2.0"):
        for path, body in native_files(version).items():
            assert request(item_path(fixture, path), raw=True)[1] == body, "dry run changed native bytes"
    check_log(fixture, log)
    state.write_text(json.dumps({"dry": trigger, "dry_csv_sha256": digest}) + "\n")
    print("Pinned disabled policy: real dry run, native bytes and retained CSV PASS")


def check(fixture, state, log):
    assert fixture["revision"] == 2
    check_config(fixture)
    data = json.loads(state.read_text())
    trigger = run_policy(fixture, False)
    digest = check_report(fixture, trigger, False)
    assert check_report(fixture, data["dry"], True) == data["dry_csv_sha256"], "real deletion rewrote prior dry report"
    for path in native_files("1.0"):
        request(item_path(fixture, path), want=404, raw=True)
    for path, body in native_files("2.0").items():
        assert request(item_path(fixture, path), raw=True)[1] == body, "cleanup changed protected latest version"
    metadata = request(item_path(fixture, "com/acme/provider/maven-metadata.xml"), raw=True)[1]
    assert b"<version>1.0</version>" not in metadata and b"<version>2.0</version>" in metadata, "native metadata lost its normal cleanup effect"
    view = request("/artifactory/api/cleanup/policies/view?" + urlencode({"itemTypes": "package", "policyKey": fixture["policy_key"]}))
    assert len(view["policies"]) == 1 and view["policies"][0]["lastRunData"]["reportDownloaded"] is True, "completed CSV download marker missing"
    check_log(fixture, log)
    data.update(actual=trigger, actual_csv_sha256=digest)
    state.write_text(json.dumps(data) + "\n")
    print("Pinned update/enablement: real native cleanup, latest-version protection and CSV PASS")


def deleted(fixture, state, log):
    request(POLICIES + quote(fixture["policy_key"], safe=""), want=404)
    request("/artifactory/api/repositories/" + quote(fixture["repository_key"], safe=""), want=404)
    data = json.loads(state.read_text())
    for name, dry in (("dry", True), ("actual", False)):
        assert check_report(fixture, data[name], dry) == data[name + "_csv_sha256"], "destroy rewrote retained run history"
    check_log(fixture, log, deleted=True)
    evidence = {"application_commit": os.environ["TERRAFORM_APPLICATION_COMMIT"], "terraform_cli": os.environ["TERRAFORM_CLI_VERSION"],
                "provider": "jfrog/artifactory12.11.14", "policy": fixture["policy_key"], "repository": fixture["repository_key"],
                "dry_trigger": data["dry"], "actual_trigger": data["actual"], "retained_reports": True, "delete_status": 200}
    (Path(os.environ["GOCOVERDIR"]) / "cleanup-provider-evidence.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print("Pinned provider destroy200, missing resources and retained run/CSV history PASS")


if __name__ == "__main__":
    if sys.argv[1] == "--inventory":
        inventory(Path(sys.argv[2]))
    else:
        fixture = json.loads(Path(sys.argv[2]).read_text())
        operation = {"--prepare": prepare, "--check": check, "--deleted": deleted}[sys.argv[1]]
        operation(fixture, Path(sys.argv[3]), Path(sys.argv[4]))
