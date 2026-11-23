"""Qualify the pinned LDAP group setting resource through the server's answers.

artifactory_ldap_group_setting_v2 writes and reads the Access API, so an
empty plan alone would accept a server that echoed each setting back from
its own store. Each check therefore reads both settings through the Access
API list, requires the XML descriptor to carry no ldapGroupSettings (7.161.26's
SecurityType has none; its PATCH moves them to Access), and asks for the
effect: a refresh of each setting reaches the group sync, which refuses it
because the connection they read through is disabled. The deprecated
artifactory_ldap_group_setting reads the XML descriptor back, which therefore
finds nothing, here as on 7.x from 7.57.1, so the fixture does not use it.
Only the isolated runner's loopback application is contacted.
"""

import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.parse import quote, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
import xml.etree.ElementTree as ET


SCENARIO = "deploy/artifactweave/terraform/ldap-groups"
RESOURCES = ["artifactory_ldap_setting_v2", "artifactory_ldap_group_setting_v2", "artifactory_ldap_group_setting_v2"]
GROUPS = "/access/api/v1/ldap/groups"


def inventory(root):
    fixture = root / SCENARIO
    declarations = re.findall(r'^resource "([^"]+)"', (fixture / "main.tf").read_text(), re.M)
    assert declarations == RESOURCES, "LDAP group fixture must manage the connection and two v2 group settings"
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
    assert scenarios and SCENARIO in scenarios.group(), "LDAP groups omitted from required provider scenarios"
    runner = (root / "test/terraform/run.sh").read_text()
    assert SCENARIO + ") apply_ldap_groups ;;" in runner, "LDAP groups scenario has no native apply steps"
    for mode in ["--check", "--deleted"]:
        assert 'python3 -I "$here/check-ldap-groups.py" ' + mode in runner, "LDAP groups runner omits required effect or deletion checks"
    release = re.search(r'^ARTIFACTORY_VERSION=([0-9.]+)$', runner, re.M)
    assert release and release.group(1) == version, "artifactory release pin missing from provider mirror"
    print("LDAP groups provider inventory: the connection, two v2 group settings and required native lane PASS")


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(method, path):
    base = os.environ["TF_VAR_url"]
    parsed = urlsplit(base)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port, "LDAP group proof requires the owned loopback application"
    req = Request(base + path, method=method, headers={"Authorization": "Bearer " + os.environ["TF_VAR_access_token"]})
    try:
        with build_opener(ProxyHandler({}), NoRedirects()).open(req, timeout=30) as response:
            return response.status, response.read(1 << 20)
    except HTTPError as error:
        return error.code, error.read(1 << 20)


def names():
    prefix = os.environ["TF_VAR_name_prefix"]
    return prefix + "-teams", prefix + "-units"


def access_settings():
    status, body = request("GET", GROUPS)
    assert status == 200, f"group settings list: status {status}"
    return {s["name"]: s for s in json.loads(body)}


def local(tag):
    return tag.rsplit("}", 1)[-1]


def no_descriptor_settings():
    status, body = request("GET", "/artifactory/api/system/configuration")
    assert status == 200, f"XML descriptor: status {status}"
    security = [e for e in ET.fromstring(body) if local(e.tag) == "security"]
    assert len(security) == 1, "XML descriptor has no security element"
    names = [local(e.tag) for e in security[0].iter()]
    assert "ldapGroupSettings" not in names and "ldapGroupSetting" not in names, \
        f"the XML descriptor carries group settings, which 7.161.26's SecurityType does not: {names}"


def refused_refresh(name):
    status, body = request("POST", GROUPS + "/" + quote(name, safe="") + "/refresh?operation=UPDATE")
    assert status == 400 and b"enabled_ldap names no enabled LDAP connection" in body, \
        f"refresh of {name} through the disabled connection: status {status}: {body[:200]!r}"


def check(strategy, sub_tree):
    assert strategy in ("STATIC", "DYNAMIC") and sub_tree in ("true", "false"), "expected STATIC|DYNAMIC and true|false"
    teams, units = names()
    key = os.environ["TF_VAR_connection_key"]
    member = "uniqueMember" if strategy == "STATIC" else "memberOf"
    access = access_settings()
    no_descriptor_settings()
    for name in (teams, units):
        assert name in access, f"{name} missing from the Access API"
    t = access[teams]
    assert (t["strategy"], t["group_member_attribute"], t["sub_tree"], t["enabled_ldap"]) == \
        (strategy, member, sub_tree == "true", key), f"{teams} read back {t}"
    u = access[units]
    assert (u["strategy"], u["group_member_attribute"], u["sub_tree"], u["enabled_ldap"], u["group_base_dn"]) == \
        ("HIERARCHICAL", "ou", False, key, ""), f"{units} read back {u}"
    status, body = request("GET", "/access/api/v1/ldap/settings/" + quote(key, safe=""))
    assert status == 200 and json.loads(body)["enabled"] is False, f"connection {key}: status {status}"
    for name in (teams, units):
        refused_refresh(name)
    print(f"LDAP groups: {strategy} teams with sub_tree {sub_tree} and HIERARCHICAL units read back through the Access API, absent from the XML descriptor, and synced PASS")


def deleted():
    teams, units = names()
    access = access_settings()
    no_descriptor_settings()
    for name in (teams, units):
        assert name not in access, f"{name} survived destroy"
        status, _ = request("GET", GROUPS + "/" + quote(name, safe=""))
        assert status == 404, f"{name} after destroy: status {status}"
    status, _ = request("GET", "/access/api/v1/ldap/settings/" + quote(os.environ["TF_VAR_connection_key"], safe=""))
    assert status == 404, f"connection after destroy: status {status}"
    print("LDAP groups destroy: both settings and the connection removed PASS")


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--inventory":
        inventory(Path(sys.argv[2]))
    elif len(sys.argv) == 4 and sys.argv[1] == "--check":
        check(*sys.argv[2:])
    elif len(sys.argv) == 2 and sys.argv[1] == "--deleted":
        deleted()
    else:
        raise SystemExit("expected --inventory ROOT, --check STRATEGY SUB_TREE, or --deleted")
