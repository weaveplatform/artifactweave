"""Qualify artifactory_general_security through the server's own answers.

The pinned provider reads its two settings back from /api/securityconfig, so
an empty plan alone would accept a server that echoed them without acting on
them. Each check therefore also asks the effect: a request without
credentials, and the bootstrap administrator's password over HTTP Basic in
clear text and encrypted. Only the isolated runner's loopback application is
contacted; the encrypted password is kept in the run's private workspace.
"""

import base64
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


SCENARIO = "deploy/artifactweave/terraform/general-security"
POLICIES = ("SUPPORTED", "REQUIRED", "UNSUPPORTED")
# A verified caller with no API key gets 404 here and one with a key 200;
# refused credentials get 401, so the status alone says whether Basic took
# the password.
LOGIN = "/artifactory/api/security/apiKey"
# Repository listing is not a public route: without credentials it answers
# 401 while anonymous access is off and the anonymous user's view otherwise.
ANONYMOUS = "/artifactory/api/repositories"


def inventory(root):
    fixture = root / SCENARIO
    declarations = re.findall(r'^resource "([^"]+)"', (fixture / "main.tf").read_text(), re.M)
    assert declarations == ["artifactory_general_security"], "general security fixture must manage exactly the one resource"
    versions = (fixture / "versions.tf").read_text()
    provider = re.search(r'artifactory\s*=\s*\{([^}]+)\}', versions)
    assert provider and '"jfrog/artifactory"' in provider.group(), "artifactory provider missing"
    version = re.search(r'version\s*=\s*"([0-9.]+)"', provider.group()).group(1)
    locked = (fixture / ".terraform.lock.hcl").read_text()
    pinned = re.search(r'provider "registry.terraform.io/jfrog/artifactory" \{(.*?)\n\}', locked, re.S)
    locked_version = re.search(r'version\s*=\s*"([0-9.]+)"', pinned.group()) if pinned else None
    assert locked_version and locked_version.group(1) == version, "artifactory provider lock missing or changed"
    makefile = (root / "Makefile").read_text()
    scenarios = re.search(r'^TERRAFORM_SCENARIOS\s*\?=.*?(?=\n\n)', makefile, re.M | re.S)
    assert scenarios and SCENARIO in scenarios.group(), "general security omitted from required provider scenarios"
    runner = (root / "test/terraform/run.sh").read_text()
    assert SCENARIO + ") apply_general_security ;;" in runner, "general security scenario has no native apply steps"
    for mode in ["--check", "--deleted"]:
        assert 'python3 -I "$here/check-general-security.py" ' + mode in runner, "general security runner omits required effect or default checks"
    release = re.search(r'^ARTIFACTORY_VERSION=([0-9.]+)$', runner, re.M)
    assert release and release.group(1) == version, "artifactory release pin missing from provider mirror"
    print("general security provider inventory: one pinned resource and required native lane PASS")


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(path, authorization=None):
    base = os.environ["TF_VAR_url"]
    parsed = urlsplit(base)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port, "general security proof requires the owned loopback application"
    headers = {"Authorization": authorization} if authorization else {}
    req = Request(base + path, headers=headers)
    try:
        with build_opener(ProxyHandler({}), NoRedirects()).open(req, timeout=30) as response:
            return response.status, response.read(1 << 20)
    except HTTPError as error:
        return error.code, error.read(1 << 20)


def admin():
    return "Bearer " + os.environ["TF_VAR_access_token"]


def basic(password):
    credential = "admin:" + password
    return "Basic " + base64.b64encode(credential.encode()).decode()


def settings():
    status, body = request("/artifactory/api/securityconfig", admin())
    assert status == 200, f"security configuration read: status {status}"
    return json.loads(body)


def logs_in(password):
    status, body = request(LOGIN, basic(password))
    assert status in (200, 401, 404), f"login probe: status {status}: {body[:200]!r}"
    return status != 401


def effects(anonymous, policy, secret_file):
    doc = settings()
    assert doc["anonAccessEnabled"] is anonymous, f"anonAccessEnabled {doc['anonAccessEnabled']}, applied {anonymous}"
    password_settings = doc["passwordSettings"]
    assert password_settings["encryptionPolicy"] == policy, f"encryptionPolicy {password_settings['encryptionPolicy']}, applied {policy}"
    assert password_settings["encryptionRequired"] is (policy == "REQUIRED"), "encryptionRequired disagrees with the policy"
    assert password_settings["encryptionEnabled"] is (policy != "UNSUPPORTED"), "encryptionEnabled disagrees with the policy"

    status, _ = request(ANONYMOUS)
    assert status == (200 if anonymous else 401), f"request without credentials: status {status}, anonymous access {anonymous}"

    clear = os.environ["GENERAL_SECURITY_ADMIN_PASSWORD"]
    if not secret_file.exists():
        # Only a password login may ask for the encrypted password, and the
        # first apply leaves the clear-text one usable, so it is fetched once.
        assert policy != "REQUIRED", "the encrypted password must be fetched before REQUIRED"
        status, body = request("/artifactory/api/security/encryptedPassword", basic(clear))
        assert status == 200 and body, f"encrypted password: status {status}"
        secret_file.write_bytes(body)
        secret_file.chmod(0o600)
    encrypted = secret_file.read_text()
    assert encrypted != clear, "the encrypted password is the clear-text one"
    assert logs_in(clear) is (policy != "REQUIRED"), f"clear-text password under {policy}"
    assert logs_in(encrypted) is (policy != "UNSUPPORTED"), f"encrypted password under {policy}"
    print(f"general security: anonymous access {str(anonymous).lower()} and encryption policy {policy} read back and enforced PASS")


def check(anonymous, policy, secret_file):
    assert anonymous in ("true", "false") and policy in POLICIES, "expected true|false and a policy"
    effects(anonymous == "true", policy, Path(secret_file))


def deleted(secret_file):
    # The pinned provider's delete sends no request: the settings stay as
    # the last apply left them, which the runner sets to the defaults.
    effects(False, "SUPPORTED", Path(secret_file))
    print("general security destroy: defaults remain in force PASS")


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--inventory":
        inventory(Path(sys.argv[2]))
    elif len(sys.argv) == 5 and sys.argv[1] == "--check":
        check(*sys.argv[2:])
    elif len(sys.argv) == 3 and sys.argv[1] == "--deleted":
        deleted(sys.argv[2])
    else:
        raise SystemExit("expected --inventory ROOT, --check ANONYMOUS POLICY SECRET, or --deleted SECRET")
