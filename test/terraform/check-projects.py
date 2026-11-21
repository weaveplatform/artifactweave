"""Qualify pinned project resources with current API and content effects.

Only the isolated runner's loopback application is contacted. Terraform state
and credentials stay private; retained evidence contains names, sizes, digests
and assertion results. A checksum deploy proves logical quota accounting with
one bounded-memory 1 GiB source upload rather than two physical copies.
"""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


SCENARIO = "deploy/artifactweave/terraform/projects"
PROJECTS = "/access/api/v1/projects"
RESOURCES = {
    "project", "project_project", "project_user", "project_group", "project_role",
    "project_environment", "project_repository", "project_share_repository",
    "project_share_repository_with_all",
}
GIB = 1 << 30
QUOTA_BYTES = GIB - (1 << 20)
CHUNK = b"artifactweave-project-quota-proof\n" * 2048


def inventory(root):
    fixture = root / SCENARIO
    declarations = set(re.findall(r'^resource "([^"]+)"', (fixture / "main.tf").read_text(), re.M))
    assert RESOURCES <= declarations, "project fixture omits a planned pinned resource"
    versions = (fixture / "versions.tf").read_text()
    minimum = re.search(r'required_version\s*=\s*">=\s*(\d+)\.(\d+)(?:\.\d+)?"', versions)
    assert minimum and tuple(map(int, minimum.groups())) >= (1, 17), "project scenario requires Terraform >=1.17"
    provider = re.search(r'project\s*=\s*\{([^}]+)\}', versions)
    assert provider and '"jfrog/project"' in provider.group(), "project provider missing"
    version = re.search(r'version\s*=\s*"([0-9.]+)"', provider.group()).group(1)
    locked = (fixture / ".terraform.lock.hcl").read_text()
    project = re.search(r'provider "registry.terraform.io/jfrog/project" \{(.*?)\n\}', locked, re.S)
    locked_version = re.search(r'version\s*=\s*"([0-9.]+)"', project.group()) if project else None
    assert locked_version and locked_version.group(1) == version, "project provider lock missing or changed"
    makefile = (root / "Makefile").read_text()
    scenarios = re.search(r'^TERRAFORM_SCENARIOS\s*\?=.*?(?=\n\n)', makefile, re.M | re.S)
    assert scenarios and SCENARIO in scenarios.group(), "projects omitted from required provider scenarios"
    runner = (root / "test/terraform/run.sh").read_text()
    assert SCENARIO + ") apply_projects ;;" in runner, "project scenario has no native apply steps"
    for mode in ["--check", "--deleted"]:
        assert 'python3 -I "$here/check-projects.py" ' + mode in runner, "project runner omits required effect or destruction checks"
    release = re.search(r'^PROJECT_VERSION=([0-9.]+)$', runner, re.M)
    assert release and release.group(1) == version, "project release pin missing from provider mirror"
    print("project provider inventory: nine planned resource types and required native lane PASS")
    return version


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(path, method="GET", data=None, want=200, actor=None, fixture=None, headers=None):
    base = os.environ["TF_VAR_url"]
    parsed = urlsplit(base)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port, "project proof requires the owned loopback application"
    authorization = "Bearer " + os.environ["TF_VAR_access_token"]
    if actor:
        credential = fixture["users"][actor] + ":" + os.environ["TF_VAR_user_password"]
        authorization = "Basic " + base64.b64encode(credential.encode()).decode()
    fields = {"Authorization": authorization, "Content-Type": "application/octet-stream"}
    fields.update(headers or {})
    req = Request(base + path, data=data, method=method, headers=fields)
    try:
        with build_opener(ProxyHandler({}), NoRedirects()).open(req, timeout=180) as response:
            status, payload = response.status, response.read(1 << 20)
    except HTTPError as error:
        status, payload = error.code, error.read(1 << 20)
    assert status == want, f"{method} {path}: status {status}, expected {want}"
    return payload


def document(path, **kwargs):
    payload = request(path, **kwargs)
    return json.loads(payload) if payload else None


def member_roles(project, kind, name):
    members = document(f"{PROJECTS}/{project}/{kind}")["members"]
    matched = [member for member in members if member["name"] == name]
    assert len(matched) == 1, "project member omitted or duplicated"
    return set(matched[0]["roles"])


def current_configuration(fixture):
    revision = fixture["revision"]
    owner = fixture["projects"]["owner"]
    policy = document(PROJECTS + "/" + owner)
    assert policy["storage_quota_bytes"] == GIB, "provider quota is not the configured binary GiB"
    assert policy["soft_limit"] is (revision == 2), "provider hard/soft quota mapping changed"
    assert policy["storage_quota_email_notification"] is False, "fixture must not send quota mail"
    for key in fixture["projects"].values():
        assert document(PROJECTS + "/" + key)["project_key"] == key, "project alias did not create the expected project"
    for name, repository in fixture["repositories"].items():
        expected = fixture["projects"]["outsider"] if name == "foreign" else owner
        actual = document("/artifactory/api/repositories/" + repository)
        assert actual["projectKey"] == expected, "repository attachment was not synchronously visible"
        assert actual["environments"] == ["DEV"], "repository environment changed at attachment"
    expected_group_role = fixture["writer"] if revision == 1 else fixture["reader"]
    assert member_roles(owner, "groups", fixture["group"]) == {expected_group_role}, "group membership did not update"
    assert member_roles(owner, "users", fixture["users"]["owner"]) == {fixture["reader"]}, "direct role unexpectedly bypasses the group proof"
    assert member_roles(owner, "users", fixture["users"]["quota"]) == {fixture["writer"]}, "quota actor lost its native deploy authority"
    for name in ["consumer", "outsider"]:
        assert member_roles(fixture["projects"][name], "users", fixture["users"][name]) == {fixture["client_role"]}, "client role assignment missing"
    environments = {row["name"] for row in document(f"{PROJECTS}/{owner}/environments")}
    assert fixture["environment"] in environments, "project environment did not refresh"
    if revision == 2:
        assert owner + "-lab" not in environments, "environment rename left its old identity alive"


def native_access(fixture):
    revision = fixture["revision"]
    repos = fixture["repositories"]
    content = b"current project authority from the pinned provider"
    route = lambda name, path: "/artifactory/" + repos[name] + "/" + path
    if revision == 1:
        for name in ["attached", "shared", "all"]:
            request(route(name, "proof.txt"), "PUT", content, 201, "owner", fixture)
        assert request(route("shared", "proof.txt"), actor="consumer", fixture=fixture) == content
        request(route("shared", "refused.txt"), "PUT", b"refused", 403, "consumer", fixture)
        request(route("shared", "proof.txt"), want=403, actor="outsider", fixture=fixture)
        assert request(route("all", "proof.txt"), actor="outsider", fixture=fixture) == content
        request(route("all", "refused.txt"), "PUT", b"refused", 403, "consumer", fixture)
    else:
        # Same existing identities: neither a cached Basic caller nor its old
        # group role/share may keep the authority removed by the second apply.
        assert request(route("attached", "proof.txt"), actor="owner", fixture=fixture) == content
        request(route("attached", "revoked.txt"), "PUT", b"refused", 403, "owner", fixture)
        request(route("shared", "proof.txt"), want=403, actor="consumer", fixture=fixture)
        request(route("all", "writable.txt"), "PUT", b"now permitted", 201, "consumer", fixture)
        request(route("all", "still-refused.txt"), "PUT", b"refused", 403, "outsider", fixture)
    request(route("foreign", "proof.txt"), "PUT", b"refused", 403, "owner", fixture)
    for name, path in [("attached", "revoked.txt"), ("shared", "refused.txt"), ("all", "refused.txt"), ("all", "still-refused.txt")]:
        request(route(name, path), want=404)
    print("project native access: revision", revision, "group role, standard share and cross-project effects PASS")


def quota_effects(fixture, evidence):
    repository = fixture["repositories"]["attached"]
    route = "/artifactory/" + repository + "/quota-source.bin"
    if fixture["revision"] == 1:
        digest = hashlib.sha256()

        def body():
            remaining = QUOTA_BYTES
            while remaining:
                data = CHUNK[:min(remaining, len(CHUNK))]
                digest.update(data)
                remaining -= len(data)
                yield data

        request(route, "PUT", body(), 201, "quota", fixture, {"Content-Length": str(QUOTA_BYTES)})
        evidence["quota"] = {"bytes": QUOTA_BYTES, "sha256": digest.hexdigest()}
    quota = evidence["quota"]
    metadata = document("/artifactory/api/storage/" + repository + "/quota-source.bin")
    assert int(metadata["size"]) == quota["bytes"] and metadata["checksums"]["sha256"] == quota["sha256"], "quota source bytes were not committed correctly"
    prefix = request(route, want=206, actor="quota", fixture=fixture, headers={"Range": "bytes=0-15"})
    assert prefix == CHUNK[:16], "quota source is not natively readable"
    copy = "/artifactory/" + repository + "/quota-copy.bin"
    headers = {"X-Checksum-Deploy": "true", "X-Checksum-Sha256": quota["sha256"]}
    revision = fixture["revision"]
    result = request(copy, "PUT", b"", 403 if revision == 1 else 201, "quota", fixture, headers)
    if revision == 1:
        refusal = json.loads(result)
        assert refusal["errors"] == [{"message": "project storage quota exceeded", "status": 403}], "hard quota failed for a different reason"
        request(copy, want=404)
    else:
        metadata = document("/artifactory/api/storage/" + repository + "/quota-copy.bin")
        assert int(metadata["size"]) == quota["bytes"] and metadata["checksums"]["sha256"] == quota["sha256"], "soft quota did not commit the real content reference"
        assert request(copy, want=206, actor="quota", fixture=fixture, headers={"Range": "bytes=0-15"}) == CHUNK[:16]
    print("project native quota: revision", revision, "hard refusal" if revision == 1 else "soft-limit publication", "PASS")


def check(root, fixture_file, state_file, evidence_file):
    provider_version = inventory(root)
    fixture = json.loads(fixture_file.read_text())
    state = json.loads(state_file.read_text())
    present = {row["type"] for row in state["resources"] if row.get("mode") == "managed"}
    expected = RESOURCES - ({"project_share_repository"} if fixture["revision"] == 2 else set())
    assert expected <= present, "native Terraform state omits a required project resource"
    evidence = json.loads(evidence_file.read_text()) if evidence_file.exists() else {
        "revisions": [], "application_commit": os.environ["TERRAFORM_APPLICATION_COMMIT"],
        "terraform_minimum": ">=1.17", "terraform_cli": os.environ["TERRAFORM_CLI_VERSION"],
        "project_provider": provider_version,
    }
    assert evidence["revisions"] == list(range(1, fixture["revision"])), "project proof revisions must complete in order exactly once"
    current_configuration(fixture)
    native_access(fixture)
    quota_effects(fixture, evidence)
    evidence["revisions"].append(fixture["revision"])
    evidence["fixture"] = fixture
    evidence_file.write_text(json.dumps(evidence, indent=2) + "\n")
    evidence_file.chmod(0o600)


def deleted(fixture_file, evidence_file):
    fixture = json.loads(fixture_file.read_text())
    for key in fixture["projects"].values():
        request(PROJECTS + "/" + key, want=404)
        for family in ["users", "groups", "roles", "environments"]:
            request(f"{PROJECTS}/{key}/{family}", want=404)
    for repository in fixture["repositories"].values():
        request("/artifactory/api/repositories/" + repository, want=404)
        request(PROJECTS + "/_/repositories/" + repository, want=404)
    for user in fixture["users"].values():
        request("/artifactory/api/security/users/" + user, want=404)
    request("/artifactory/api/security/groups/" + fixture["group"], want=404)
    evidence = json.loads(evidence_file.read_text())
    assert evidence["revisions"] == [1, 2], "destroy cannot complete an unqualified project proof"
    evidence["destroy_verified"] = True
    evidence_file.write_text(json.dumps(evidence, indent=2) + "\n")
    print("project provider destroy: no live project, membership, role, environment, repository, share, user or group PASS")


if __name__ == "__main__":
    if sys.argv[1] == "--inventory" and len(sys.argv) == 3:
        inventory(Path(sys.argv[2]))
    elif sys.argv[1] == "--check" and len(sys.argv) == 6:
        check(*(Path(value) for value in sys.argv[2:]))
    elif sys.argv[1] == "--deleted" and len(sys.argv) == 4:
        deleted(*(Path(value) for value in sys.argv[2:]))
    else:
        raise SystemExit("expected --inventory ROOT, --check ROOT FIXTURE STATE EVIDENCE, or --deleted FIXTURE EVIDENCE")
