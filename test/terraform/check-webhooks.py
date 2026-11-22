"""Qualify every pinned webhook resource without logging credentials or state.

The native runner invokes inventory validation before installing providers. Live
checks exercise administration only; they never claim a receiver was contacted.
"""

import base64
import copy
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

DOMAINS = {
    "artifact": "deployed", "artifact_property": "added",
    "artifact_lifecycle": "archive", "docker": "pushed", "build": "uploaded",
    "release_bundle": "signed", "distribution": "distribute_started",
    "artifactory_release_bundle": "received", "destination": "received",
    "user": "locked", "release_bundle_v2": "release_bundle_v2_started",
    "release_bundle_v2_promotion": "release_bundle_v2_promotion_started",
}
COLLECTION = "/event/api/v1/subscriptions"
SECRETS = ("Terraform-webhook-secret-1", "Terraform-webhook-secret-2")


def inventory(root):
    rows = json.loads((root / "test/terraform/webhook-cases.json").read_text())
    assert rows["provider"] == "jfrog/artifactory" and rows["version"] == "12.11.14", "unpinned webhook inventory"
    required = {
        ("artifactory_" + domain + ("_custom" if handler == "custom" else "") + "_webhook", domain, handler, event)
        for domain, event in DOMAINS.items() for handler in ("standard", "custom")
    }
    actual = {(r["type"], r["domain"], r["handler"], r["event"]) for r in rows["resources"]}
    assert len(rows["resources"]) == 24 and actual == required, "webhook inventory must contain all 24 pinned resources exactly once"
    fixture = root / "deploy/artifactweave/terraform/webhooks"
    declarations = re.findall(r'^resource "([^"]+)" "([^"]+)"', (fixture / "main.tf").read_text(), re.M)
    expected = {(r["type"], r["domain"] + "_" + r["handler"]) for r in rows["resources"]}
    assert len(declarations) == 24 and set(declarations) == expected, "webhook fixture is missing, repeating or substituting a resource"
    makefile = (root / "Makefile").read_text()
    scenarios = re.search(r'^TERRAFORM_SCENARIOS\s*\?=.*?(?=\n\n)', makefile, re.M | re.S)
    assert scenarios, "default provider scenarios missing"
    assert "deploy/artifactweave/terraform/webhooks" in scenarios.group(), "webhooks omitted from required provider scenarios"
    return rows["resources"]


def request(path, method="GET", body=None, want=200, authorization=None):
    data = None if body is None else json.dumps(body).encode()
    token = os.environ["TF_VAR_access_token"]
    req = Request(os.environ["TF_VAR_url"] + path, data=data, method=method,
                  headers={"Authorization": authorization or "Bearer " + token, "Content-Type": "application/json"})
    try:
        with urlopen(req, timeout=20) as response:
            status, payload = response.status, response.read()
    except HTTPError as error:
        status, payload = error.code, error.read()
    assert all(value.encode() not in payload for value in SECRETS), "API returned a fixture secret"
    assert status == want, f"{method} {path}: status {status}, expected {want}"
    return json.loads(payload) if payload else None


def fields(actual, expected, context):
    for name, value in expected.items():
        assert actual.get(name) == value, context + ": field mismatch: " + name


def check_log(log_file):
    content = log_file.read_bytes()
    assert all(value.encode() not in content for value in SECRETS), "server log disclosed a webhook fixture secret"


def check(root, keys_file, state_file, data_dir, log_file):
    rows = inventory(root)
    keys = json.loads(keys_file.read_text())
    assert set(keys) == {r["type"] for r in rows} and len(set(keys.values())) == 24, "native output omitted a resource or reused a key"
    state = json.loads(state_file.read_text())
    resources = [r for r in state["resources"] if r.get("mode") == "managed"]
    assert len(resources) == 24 and {r["type"] for r in resources} == set(keys), "native state does not contain exactly the required 24 resources"
    revision = int(os.environ["TF_VAR_revision"])
    listed = {s["key"]: s for s in request(COLLECTION)}
    for row in rows:
        case = row["domain"] + "_" + row["handler"]
        key = keys[row["type"]]
        view = request(COLLECTION + "/" + key)
        assert listed.get(key) == view, case + ": GET/list disagree"
        fields(view, {"key": "tf-webhook-" + case, "description": case + " revision " + str(revision), "enabled": True}, case)
        fields(view["event_filter"], {"domain": row["domain"], "event_types": [row["event"]]}, case)
        domain = row["domain"]
        criteria = view["event_filter"].get("criteria", {})
        if domain not in ("user", "artifact_lifecycle", "release_bundle_v2_promotion"):
            fields(criteria, {"includePatterns": ["org/**" if revision == 1 else "release/**"], "excludePatterns": ["**/temporary/**"]}, case)
        if domain in ("artifact", "artifact_property", "docker"):
            fields(criteria, {"anyLocal": True, "anyRemote": False, "anyFederated": False}, case)
            assert not criteria.get("repoKeys"), case + ": unexpected repository selector"
        elif domain == "build":
            fields(criteria, {"anyBuild": False, "selectedBuilds": ["fixture-build"]}, case)
        elif domain in ("release_bundle", "distribution", "artifactory_release_bundle", "destination", "release_bundle_v2"):
            fields(criteria, {"anyReleaseBundle": False}, case)
            if domain == "release_bundle_v2":
                fields(criteria, {"selectedReleaseBundles": {"release-bundles-v2": ["fixture-bundle"]}}, case)
            else:
                fields(criteria, {"registeredReleaseBundlesNames": ["fixture-bundle"]}, case)
        elif domain == "release_bundle_v2_promotion":
            fields(criteria, {"selectedEnvironments": ["DEV" if revision == 1 else "PROD"]}, case)
        assert len(view["handlers"]) == 1, case + ": unexpected handler count"
        handler = view["handlers"][0]
        fields(handler, {"url": os.environ["TF_VAR_receiver_url"].rstrip("/") + "/" + case + "/" + str(revision)}, case)
        if row["handler"] == "standard":
            fields(handler, {"handler_type": "webhook", "secret": "", "secret_configured": True, "use_secret_for_signing": revision == 2}, case)
            headers = handler.get("custom_http_headers", [])
        else:
            fields(handler, {"handler_type": "custom-webhook", "method": "POST" if revision == 1 else "PATCH"}, case)
            fields(json.loads(handler["payload"]), {"fixture": case, "revision": revision}, case)
            assert handler.get("secrets") == [{"name": "credential"}], case + ": named secret projection changed"
            headers = handler.get("http_headers", [])
        assert {h["name"]: h["value"] for h in headers} == {"X-Fixture": case, "X-Revision": str(revision)}, case + ": headers changed"
        print(row["type"] + ": revision " + str(revision) + " GET/list/fields/redaction PASS")
    # Include SQLite WAL pages and previous values after rotation. Terraform's
    # own sensitive state is deliberately excluded; the provider retains inputs.
    for path in data_dir.rglob("*"):
        if path.is_file():
            content = path.read_bytes()
            assert all(value.encode() not in content for value in SECRETS), "fixture secret persisted without sealing"
    check_log(log_file)
    print("24 webhook resources: revision", revision, "administration, sealed storage and log redaction PASS")


def controls(log_file):
    projects = ("tfwebalpha", "tfwebbeta")
    user = "tf-webhook-project-admin"
    for project in projects:
        request("/access/api/v1/projects", "POST", {"project_key": project, "display_name": project}, 201)
    request("/access/api/v2/users", "POST", {"username": user, "email": user + "@example.invalid", "password": "Terraform-webhook-user-1"}, 201)
    request("/access/api/v1/projects/" + projects[0] + "/users/" + user, "PUT", {"roles": ["Project Admin"]})
    basic = "Basic " + base64.b64encode((user + ":Terraform-webhook-user-1").encode()).decode()
    base_body = {"enabled": True, "event_filter": {"domain": "artifact", "event_types": ["deployed"], "criteria": {}},
                 "handlers": [{"handler_type": "webhook", "url": os.environ["TF_VAR_receiver_url"], "secret": SECRETS[1]}]}
    bodies = []
    for i, project in enumerate(projects):
        body = dict(base_body, key="tf-webhook-scope-" + str(i), project_key=project)
        request(COLLECTION, "POST", body, 201)
        bodies.append(body)
    own = COLLECTION + "/" + bodies[0]["key"]
    foreign = COLLECTION + "/" + bodies[1]["key"]
    request(own, authorization=basic)
    before = request(foreign)
    request(foreign, authorization=basic, want=403)
    request(foreign, "PUT", bodies[1], 403, basic)
    request(foreign, "DELETE", want=403, authorization=basic)
    request(COLLECTION, "POST", dict(bodies[1], key="tf-webhook-foreign-denied"), 403, basic)
    assert request(foreign) == before, "denied administration changed foreign subscription"
    own_before = request(own)
    request(own, "PUT", dict(bodies[0], project_key=projects[1]), 400)
    invalid = copy.deepcopy(bodies[0])
    invalid["handlers"][0]["url"] = "not-an-absolute-url"
    request(own, "PUT", invalid, 400)
    invalid["handlers"][0] = {"handler_type": "custom-webhook", "url": os.environ["TF_VAR_receiver_url"], "method": "PATCH", "payload": "{{"}
    request(own, "PUT", invalid, 400)
    assert request(own) == own_before, "rejected scope/handler update changed subscription"
    visible = {s["key"] for s in request(COLLECTION, authorization=basic)}
    assert bodies[0]["key"] in visible and bodies[1]["key"] not in visible, "project list crossed scope"
    for body in bodies:
        request(COLLECTION + "/" + body["key"], "DELETE", want=204)
    request("/access/api/v1/projects/" + projects[0] + "/users/" + user, "DELETE", want=204)
    request("/access/api/v2/users/" + user, "DELETE", want=204)
    for project in projects:
        request("/access/api/v1/projects/" + project, "DELETE", want=204)
    check_log(log_file)
    print("Project isolation, immutable scope, malformed URL/template rollback and log redaction: PASS (no delivery assertion)")


def deleted(keys_file, log_file):
    keys = json.loads(keys_file.read_text())
    listed = {s["key"] for s in request(COLLECTION)}
    for kind, key in keys.items():
        request(COLLECTION + "/" + key, want=404)
        assert key not in listed, kind + ": deleted subscription remains listed"
        print(kind + ": destroy GET 404/list absent PASS")
    assert not any(key.startswith("tf-webhook-") for key in listed), "fixture subscriptions remain"
    check_log(log_file)


if __name__ == "__main__":
    mode = sys.argv[1]
    if mode == "--inventory":
        inventory(Path(sys.argv[2]))
        print("Pinned webhook inventory: all 24 required configurations present")
    elif mode == "--check":
        check(*(Path(value) for value in sys.argv[2:]))
    elif mode == "--controls":
        controls(Path(sys.argv[2]))
    elif mode == "--deleted":
        deleted(Path(sys.argv[2]), Path(sys.argv[3]))
    else:
        raise SystemExit("unknown webhook conformance mode")
